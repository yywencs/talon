package experience

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type HTTP struct {
	URL, Key string
	Client   *http.Client
}

func (h HTTP) Do(ctx context.Context, method, path string, input, output any) error {
	b, e := json.Marshal(input)
	if e != nil {
		return e
	}
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.URL, "/")+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Key != "" {
		req.Header.Set("Authorization", "Bearer "+h.Key)
		req.Header.Set("api-key", h.Key)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 4*1024*1024+1))
	if e != nil {
		return e
	}
	if len(raw) > 4*1024*1024 {
		return fmt.Errorf("HTTP response too large")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &HTTPError{res.StatusCode}
	}
	if output != nil {
		return json.Unmarshal(raw, output)
	}
	return nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("upstream HTTP status %d", e.Status) }

type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
	Profile() string
}
type HTTPEmbedder struct {
	HTTP
	Model      string
	Dimensions int
}

func (e *HTTPEmbedder) Profile() string {
	return Digest(struct {
		Model      string
		Dimensions int
		Endpoint   string
	}{e.Model, e.Dimensions, e.URL})
}
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	err := e.Do(ctx, "POST", "/embeddings", map[string]any{"model": e.Model, "input": text}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Data) != 1 || len(out.Data[0].Embedding) != e.Dimensions {
		return nil, fmt.Errorf("embedding dimension mismatch")
	}
	norm := float64(0)
	for _, v := range out.Data[0].Embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("non-finite embedding")
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return nil, fmt.Errorf("zero embedding")
	}
	return out.Data[0].Embedding, nil
}

type HTTPRanker struct {
	HTTP
	Model    string
	MinScore float64
}

func (r *HTTPRanker) Rank(ctx context.Context, query string, cards []Card) ([]int, error) {
	docs := []string{}
	for _, c := range cards {
		docs = append(docs, c.SearchText())
	}
	var out struct {
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if e := r.Do(ctx, "POST", "/rerank", map[string]any{"model": r.Model, "query": query, "documents": docs, "top_n": len(docs)}, &out); e != nil {
		return nil, e
	}
	indices := []int{}
	seen := map[int]bool{}
	for _, v := range out.Results {
		if v.Index < 0 || v.Index >= len(cards) || seen[v.Index] || math.IsNaN(v.Score) || math.IsInf(v.Score, 0) {
			return nil, fmt.Errorf("invalid reranker response")
		}
		seen[v.Index] = true
		if v.Score >= r.MinScore {
			indices = append(indices, v.Index)
		}
	}
	return indices, nil
}
func ServiceFromEnv(store Store) (*Service, error) {
	required := []string{"QDRANT_URL", "EXPERIENCE_EMBEDDING_URL", "EXPERIENCE_EMBEDDING_MODEL", "EXPERIENCE_EMBEDDING_DIMENSIONS", "EXPERIENCE_RERANK_URL", "EXPERIENCE_RERANK_MODEL", "EXPERIENCE_RERANK_MIN_SCORE"}
	for _, key := range required {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			return nil, fmt.Errorf("%s is required", key)
		}
	}
	for _, key := range []string{"QDRANT_URL", "EXPERIENCE_EMBEDDING_URL", "EXPERIENCE_RERANK_URL"} {
		u, e := url.Parse(os.Getenv(key))
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" {
			return nil, fmt.Errorf("invalid %s", key)
		}
	}
	dim, e := strconv.Atoi(os.Getenv("EXPERIENCE_EMBEDDING_DIMENSIONS"))
	if e != nil || dim < 1 || dim > 65536 {
		return nil, fmt.Errorf("invalid embedding dimensions")
	}
	score, e := strconv.ParseFloat(os.Getenv("EXPERIENCE_RERANK_MIN_SCORE"), 64)
	if e != nil || math.IsNaN(score) || math.IsInf(score, 0) {
		return nil, fmt.Errorf("invalid rerank threshold")
	}
	embed := &HTTPEmbedder{HTTP: HTTP{URL: os.Getenv("EXPERIENCE_EMBEDDING_URL"), Key: os.Getenv("EXPERIENCE_EMBEDDING_KEY")}, Model: os.Getenv("EXPERIENCE_EMBEDDING_MODEL"), Dimensions: dim}
	rank := &HTTPRanker{HTTP: HTTP{URL: os.Getenv("EXPERIENCE_RERANK_URL"), Key: os.Getenv("EXPERIENCE_RERANK_KEY")}, Model: os.Getenv("EXPERIENCE_RERANK_MODEL"), MinScore: score}
	q := &Qdrant{Store: store, HTTP: HTTP{URL: os.Getenv("QDRANT_URL"), Key: os.Getenv("QDRANT_API_KEY")}, Embedder: embed, Dimensions: dim, RankingProfile: Digest(struct {
		URL, Model string
		Threshold  float64
	}{rank.URL, rank.Model, score})}
	return &Service{Store: store, Index: q, Ranker: rank}, nil
}
