// Package githubapi 提供数据加工阶段使用的只读 GitHub Commit API 客户端。
package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL      = "https://api.github.com"
	apiVersion          = "2026-03-10"
	cacheSchemaVersion  = "v1"
	maxCommitResponse   = 16 << 20
	maxErrorBodyPreview = 4 << 10
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// Config 定义 GitHub API 地址、认证、缓存和 HTTP 行为。
type Config struct {
	Token      string
	CacheDir   string
	BaseURL    string
	HTTPClient *http.Client
}

// Client 只读取公开 GitHub Commit，并按 repository + SHA 缓存不可变响应。
type Client struct {
	token      string
	cacheDir   string
	baseURL    string
	httpClient *http.Client
}

// Commit 是预检所需的 GitHub Commit 元数据子集。
type Commit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  struct {
		Date string `json:"date,omitempty"`
	} `json:"author"`
	Committer struct {
		Date string `json:"date,omitempty"`
	} `json:"committer"`
	Parents []Parent    `json:"parents"`
	Stats   CommitStats `json:"stats"`
	Files   []File      `json:"files"`
}

// Parent 标识 Fix Commit 的一个父提交。
type Parent struct {
	SHA string `json:"sha"`
}

// CommitStats 是 GitHub 计算的提交级代码变更统计。
type CommitStats struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Total     int `json:"total"`
}

// File 保存文件级变更统计及 unified diff hunk。
type File struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	Patch            string `json:"patch,omitempty"`
}

// FetchResult 额外标识响应是否来自缓存，以及文件列表是否存在后续分页。
type FetchResult struct {
	Commit         Commit
	CacheHit       bool
	FilesTruncated bool
	RateRemaining  int
}

type apiCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Parents []Parent    `json:"parents"`
	Stats   CommitStats `json:"stats"`
	Files   []File      `json:"files"`
}

type cacheEntry struct {
	SchemaVersion  string          `json:"schema_version"`
	Repository     string          `json:"repository"`
	Ref            string          `json:"ref"`
	FilesTruncated bool            `json:"files_truncated"`
	Body           json.RawMessage `json:"body"`
}

// NewClient 创建只读客户端。Token 必须由调用方显式解析，避免客户端静默降级到低配额匿名请求。
func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, fmt.Errorf("githubapi: token is required")
	}
	if strings.TrimSpace(config.CacheDir) == "" {
		return nil, fmt.Errorf("githubapi: cache directory is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("githubapi: invalid base URL: %w", err)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{token: strings.TrimSpace(config.Token), cacheDir: config.CacheDir, baseURL: baseURL, httpClient: httpClient}, nil
}

// GetCommit 优先读取本地缓存；缓存未命中时调用 GitHub JSON API，并原子保存响应。
func (c *Client) GetCommit(ctx context.Context, repository, commit string) (FetchResult, error) {
	if !repositoryPattern.MatchString(repository) {
		return FetchResult{}, fmt.Errorf("githubapi: invalid repository %q", repository)
	}
	if !commitPattern.MatchString(commit) {
		return FetchResult{}, fmt.Errorf("githubapi: invalid commit %q", commit)
	}
	commit = strings.ToLower(commit)
	cachePath := c.cachePath(repository, commit)
	if cached, err := readCache(cachePath); err == nil {
		parsed, err := decodeCommit(cached.Body)
		if err != nil {
			return FetchResult{}, fmt.Errorf("githubapi: decode cached commit: %w", err)
		}
		return FetchResult{Commit: parsed, CacheHit: true, FilesTruncated: cached.FilesTruncated, RateRemaining: -1}, nil
	} else if !os.IsNotExist(err) {
		return FetchResult{}, fmt.Errorf("githubapi: read cache: %w", err)
	}

	endpoint := c.baseURL + "/repos/" + repository + "/commits/" + commit + "?per_page=100"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("githubapi: create request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-GitHub-Api-Version", apiVersion)
	request.Header.Set("User-Agent", "OpenTalon-Dataset/1.0")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return FetchResult{}, fmt.Errorf("githubapi: get commit %s@%s: %w", repository, commit, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		preview, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyPreview))
		return FetchResult{}, fmt.Errorf("githubapi: get commit %s@%s: HTTP %d: %s", repository, commit, response.StatusCode, strings.TrimSpace(string(preview)))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCommitResponse+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("githubapi: read commit response: %w", err)
	}
	if len(body) > maxCommitResponse {
		return FetchResult{}, fmt.Errorf("githubapi: commit response exceeds %d bytes", maxCommitResponse)
	}
	parsed, err := decodeCommit(body)
	if err != nil {
		return FetchResult{}, fmt.Errorf("githubapi: decode commit response: %w", err)
	}
	filesTruncated := strings.Contains(response.Header.Get("Link"), `rel="next"`)
	entry := cacheEntry{
		SchemaVersion: cacheSchemaVersion, Repository: repository, Ref: commit,
		FilesTruncated: filesTruncated, Body: json.RawMessage(body),
	}
	if err := writeCacheAtomic(cachePath, entry); err != nil {
		return FetchResult{}, err
	}
	rateRemaining, _ := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining"))
	return FetchResult{Commit: parsed, FilesTruncated: filesTruncated, RateRemaining: rateRemaining}, nil
}

func decodeCommit(body []byte) (Commit, error) {
	var response apiCommit
	if err := json.Unmarshal(body, &response); err != nil {
		return Commit{}, err
	}
	if response.SHA == "" {
		return Commit{}, fmt.Errorf("response is missing commit SHA")
	}
	result := Commit{
		SHA: response.SHA, Message: response.Commit.Message,
		Parents: response.Parents, Stats: response.Stats, Files: response.Files,
	}
	result.Author.Date = response.Commit.Author.Date
	result.Committer.Date = response.Commit.Committer.Date
	if result.Parents == nil {
		result.Parents = make([]Parent, 0)
	}
	if result.Files == nil {
		result.Files = make([]File, 0)
	}
	return result, nil
}

func (c *Client) cachePath(repository, commit string) string {
	directory := strings.ReplaceAll(repository, "/", "__")
	return filepath.Join(c.cacheDir, directory, commit+".json")
}

func readCache(path string) (cacheEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, err
	}
	if entry.SchemaVersion != cacheSchemaVersion || len(entry.Body) == 0 {
		return cacheEntry{}, fmt.Errorf("unsupported or empty cache entry")
	}
	return entry, nil
}

func writeCacheAtomic(path string, entry cacheEntry) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("githubapi: create cache directory: %w", err)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("githubapi: encode cache entry: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".commit-*.tmp")
	if err != nil {
		return fmt.Errorf("githubapi: create cache file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("githubapi: write cache file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("githubapi: sync cache file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("githubapi: close cache file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("githubapi: replace cache file: %w", err)
	}
	return nil
}
