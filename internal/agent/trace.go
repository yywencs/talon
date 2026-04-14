package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/utils"
)

var (
	traceMgr  *TraceManager
	traceOnce sync.Once
)

type TraceManager struct {
	traceDir string
	runID    string
	mu       sync.Mutex
	stepSeq  int
}

type TraceRequest struct {
	Timestamp string            `json:"timestamp"`
	RunID     string            `json:"run_id"`
	Step      int               `json:"step"`
	Endpoint  string            `json:"endpoint"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      any               `json:"body"`
}

type TraceResponse struct {
	Timestamp  string            `json:"timestamp"`
	RunID      string            `json:"run_id"`
	Step       int               `json:"step"`
	Endpoint   string            `json:"endpoint"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       any               `json:"body"`
}

func getTraceManager() *TraceManager {
	traceOnce.Do(func() {
		traceMgr = &TraceManager{
			runID: generateRunID(),
		}
		traceMgr.ensureTraceDir()
	})
	return traceMgr
}

func generateRunID() string {
	return fmt.Sprintf("run-%d-%03d", time.Now().UnixMilli(), time.Now().Nanosecond()%1000)
}

func (tm *TraceManager) ensureTraceDir() {
	var baseDir string
	if config.IsDebug() {
		workspaceRoot, err := utils.FindWorkspaceRoot()
		if err != nil {
			workspaceRoot = "."
		}
		baseDir = filepath.Join(workspaceRoot, ".opentalon", "traces")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		baseDir = filepath.Join(home, ".opentalon", "traces")
	}
	tm.traceDir = filepath.Join(baseDir, tm.runID)
	if err := os.MkdirAll(tm.traceDir, 0755); err != nil {
		fmt.Printf("警告: 创建追踪目录失败: %v\n", err)
	}
}

func (tm *TraceManager) nextStep() int {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.stepSeq++
	return tm.stepSeq
}

func (tm *TraceManager) TraceRequest(step int, endpoint, method string, headers map[string]string, body any) error {
	if tm.traceDir == "" {
		return nil
	}

	cleanedHeaders := cleanSensitiveHeaders(headers)
	trace := TraceRequest{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		RunID:     tm.runID,
		Step:      step,
		Endpoint:  endpoint,
		Method:    method,
		Headers:   cleanedHeaders,
		Body:      body,
	}

	return tm.writeTrace(step, "req", trace)
}

func (tm *TraceManager) TraceResponse(step int, endpoint string, statusCode int, headers map[string]string, body any) error {
	if tm.traceDir == "" {
		return nil
	}

	trace := TraceResponse{
		Timestamp:  time.Now().Format(time.RFC3339Nano),
		RunID:      tm.runID,
		Step:       step,
		Endpoint:   endpoint,
		StatusCode: statusCode,
		Headers:    headers,
		Body:       body,
	}

	return tm.writeTrace(step, "resp", trace)
}

func (tm *TraceManager) writeTrace(step int, phase string, data any) error {
	filename := fmt.Sprintf("step-%04d-%s.json", step, phase)
	filePath := filepath.Join(tm.traceDir, filename)

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化追踪数据失败: %w", err)
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return fmt.Errorf("写入追踪文件失败: %w", err)
	}

	return nil
}

func cleanSensitiveHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	cleaned := make(map[string]string)
	for k, v := range headers {
		switch {
		case strings.EqualFold(k, "Authorization"), strings.EqualFold(k, "X-API-Key"), strings.EqualFold(k, "Api-Key"):
			cleaned[k] = "***REDACTED***"
		default:
			cleaned[k] = v
		}
	}
	return cleaned
}

type traceRoundTripper struct {
	base http.RoundTripper
}

func newTraceRoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &traceRoundTripper{
		base: base,
	}
}

func (t *traceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tm := getTraceManager()
	step := tm.nextStep()
	reqHeaders := flattenHeaders(req.Header)
	reqBody, _ := cloneRequestBody(req)
	_ = tm.TraceRequest(step, req.URL.String(), req.Method, reqHeaders, decodeTraceBody(reqBody))

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		_ = tm.TraceResponse(step, req.URL.String(), 0, nil, map[string]any{"error": err.Error()})
		return nil, err
	}

	respHeaders := flattenHeaders(resp.Header)
	resp.Body = &traceCaptureReadCloser{
		ReadCloser: resp.Body,
		onClose: func(raw []byte) {
			_ = tm.TraceResponse(step, req.URL.String(), resp.StatusCode, respHeaders, decodeTraceBody(raw))
		},
	}
	return resp, nil
}

type traceCaptureReadCloser struct {
	io.ReadCloser
	buf     bytes.Buffer
	closed  bool
	onClose func([]byte)
}

func (t *traceCaptureReadCloser) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		_, _ = t.buf.Write(p[:n])
	}
	return n, err
}

func (t *traceCaptureReadCloser) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	err := t.ReadCloser.Close()
	if t.onClose != nil {
		raw := append([]byte(nil), t.buf.Bytes()...)
		t.onClose(raw)
	}
	return err
}

func cloneRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		return io.ReadAll(body)
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if closeErr := req.Body.Close(); closeErr != nil {
		return nil, closeErr
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	return raw, nil
}

func flattenHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	out := make(map[string]string, len(header))
	for k, vals := range header {
		out[k] = strings.Join(vals, ", ")
	}
	return out
}

func decodeTraceBody(raw []byte) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	var body any
	if err := json.Unmarshal(trimmed, &body); err == nil {
		return body
	}
	return string(trimmed)
}

func GetRunID() string {
	return getTraceManager().runID
}
