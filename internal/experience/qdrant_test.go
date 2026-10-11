package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type embedding struct{}

func (embedding) Profile() string                                  { return "embed" }
func (embedding) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
func TestQdrantUsesBothFilteredPrefetchesAndRRF(t *testing.T) {
	q := &Qdrant{HTTP: HTTP{URL: "http://qdrant", Client: &http.Client{Transport: transport(func(req *http.Request) (*http.Response, error) {
		var b map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&b))
		require.True(t, strings.HasSuffix(req.URL.Path, "/points/query"))
		pre := b["prefetch"].([]any)
		require.Len(t, pre, 2)
		for _, p := range pre {
			filter := p.(map[string]any)["filter"].(map[string]any)
			require.Equal(t, "v1", filter["must"].([]any)[0].(map[string]any)["match"].(map[string]any)["value"])
		}
		require.Equal(t, "rrf", b["query"].(map[string]any)["fusion"])
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"result":{"points":[{"payload":{"key":"exp@1","release":"v1"}}]}}`))}, nil
	})}}, Embedder: embedding{}, Dimensions: 2}
	keys, e := q.Search(context.Background(), Release{ID: "v1"}, Query{Query: "连接超时 CONNECTION_TIMEOUT"})
	require.NoError(t, e)
	require.Equal(t, []string{"exp@1"}, keys)
}
func TestLexicalStableCJKAndIdentifiers(t *testing.T) {
	a := Lexical("连接超时 CONNECTION_TIMEOUT")
	b := Lexical("连接超时 connection_timeout")
	require.Equal(t, a, b)
	require.Greater(t, len(a.Indices), 4)
	for i := 1; i < len(a.Indices); i++ {
		require.Less(t, a.Indices[i-1], a.Indices[i])
	}
}
func TestHTTPModelRejectsInvalidDimensionsAndRankIndices(t *testing.T) {
	h := HTTP{URL: "http://models", Client: &http.Client{Transport: transport(func(req *http.Request) (*http.Response, error) {
		raw := `{"data":[{"embedding":[1]}]}`
		if req.URL.Path == "/rerank" {
			raw = `{"results":[{"index":99,"relevance_score":0.9}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
	})}}
	_, e := (&HTTPEmbedder{HTTP: h, Dimensions: 2}).Embed(context.Background(), "test")
	require.ErrorContains(t, e, "dimension")
	_, e = (&HTTPRanker{HTTP: h}).Rank(context.Background(), "test", []Card{{Title: "a"}})
	require.ErrorContains(t, e, "invalid reranker")
}

// Explicit opt-in: creates and removes only a uniquely named test collection.
func TestLiveQdrant(t *testing.T) {
	endpoint := os.Getenv("QDRANT_TEST_URL")
	if endpoint == "" {
		t.Skip("set QDRANT_TEST_URL for real Qdrant acceptance")
	}
	q := &Qdrant{HTTP: HTTP{URL: endpoint, Key: os.Getenv("QDRANT_TEST_API_KEY")}, Embedder: embedding{}, Dimensions: 2}
	r := Release{ID: fmt.Sprintf("acceptance-%d", time.Now().UnixNano()), Profile: q.Profile(), Cards: []Card{{ID: "e", Version: 1, Title: "连接超时 connection timeout", Symptoms: []string{"timeout"}}}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, q.Do(ctx, "DELETE", "/collections/"+q.collection(r), nil, nil))
	})
	require.NoError(t, q.Build(context.Background(), r))
	keys, e := q.Search(context.Background(), r, Query{Query: "连接超时"})
	require.NoError(t, e)
	require.Equal(t, []string{"e@1"}, keys)
	require.NoError(t, q.Build(context.Background(), r))
}
