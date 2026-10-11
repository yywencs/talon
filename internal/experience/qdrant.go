package experience

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Each immutable release has its own collection. Sparse IDF statistics therefore
// cannot change when a future candidate is indexed; rollback stays reproducible.
type Qdrant struct {
	Store Store
	HTTP
	Embedder       Embedder
	Dimensions     int
	RankingProfile string
}

func (q *Qdrant) Profile() string {
	return Digest([]string{"dense-lexical-hybrid/v1", q.Embedder.Profile(), q.RankingProfile})
}
func (q *Qdrant) collection(r Release) string { return "talon_exp_" + Digest(r)[:32] }

type sparse struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

// Lexical uses lower-cased identifier tokens and CJK bigrams, with log TF and
// Qdrant's collection-local IDF. This is explicitly not advertised as BM25.
func Lexical(text string) sparse {
	counts := map[uint32]int{}
	add := func(t string) { sum := sha256.Sum256([]byte(t)); counts[binary.BigEndian.Uint32(sum[:4])]++ }
	var word []rune
	var previous rune
	flush := func() {
		if len(word) > 0 {
			add(string(word))
			word = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			add(string(r))
			if previous != 0 {
				add(string([]rune{previous, r}))
			}
			previous = r
		} else {
			previous = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	v := sparse{Indices: []uint32{}, Values: []float32{}}
	for id := range counts {
		v.Indices = append(v.Indices, id)
	}
	sort.Slice(v.Indices, func(i, j int) bool { return v.Indices[i] < v.Indices[j] })
	for _, id := range v.Indices {
		v.Values = append(v.Values, float32(1+math.Log(float64(counts[id]))))
	}
	return v
}
func (q *Qdrant) Build(ctx context.Context, r Release) error {
	path := "/collections/" + q.collection(r)
	var exists any
	e := q.Do(ctx, "GET", path, nil, &exists)
	if e != nil {
		var he *HTTPError
		if !errors.As(e, &he) || he.Status != 404 {
			return e
		}
		if e = q.Do(ctx, "PUT", path, map[string]any{"vectors": map[string]any{"dense": map[string]any{"size": q.Dimensions, "distance": "Cosine"}}, "sparse_vectors": map[string]any{"lexical": map[string]any{"modifier": "idf"}}}, nil); e != nil {
			return e
		}
	}
	for _, field := range []string{"release", "key", "component", "symptoms", "context_tags"} {
		if e = q.Do(ctx, "PUT", path+"/index?wait=true", map[string]any{"field_name": field, "field_schema": "keyword"}, nil); e != nil {
			return e
		}
	}
	for i, c := range r.Cards {
		dense, e := q.frozenEmbedding(ctx, r, c)
		if e != nil {
			return e
		}
		point := map[string]any{"id": i + 1, "vector": map[string]any{"dense": dense, "lexical": Lexical(c.SearchText())}, "payload": map[string]any{"release": r.ID, "key": c.Key(), "component": c.Component, "symptoms": c.Symptoms, "context_tags": c.ContextTags}}
		if e = q.Do(ctx, "PUT", path+"/points?wait=true", map[string]any{"points": []any{point}}, nil); e != nil {
			return e
		}
	}
	var count struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if e = q.Do(ctx, "POST", path+"/points/count", map[string]any{"exact": true}, &count); e != nil {
		return e
	}
	if count.Result.Count != len(r.Cards) {
		return fmt.Errorf("indexed count mismatch")
	}
	return nil
}
func (q *Qdrant) Search(ctx context.Context, r Release, input Query) ([]string, error) {
	dense, e := q.Embedder.Embed(ctx, input.Text())
	if e != nil {
		return nil, e
	}
	filter := map[string]any{"must": []any{map[string]any{"key": "release", "match": map[string]any{"value": r.ID}}}}
	// Component and symptom guesses remain soft relevance signals in query text.
	prefetch := []any{map[string]any{"query": dense, "using": "dense", "filter": filter, "limit": 30}, map[string]any{"query": Lexical(input.Text()), "using": "lexical", "filter": filter, "limit": 30}}
	var out struct {
		Result struct {
			Points []struct {
				Payload struct {
					Key     string `json:"key"`
					Release string `json:"release"`
				} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	e = q.Do(ctx, "POST", "/collections/"+q.collection(r)+"/points/query", map[string]any{"prefetch": prefetch, "query": map[string]any{"fusion": "rrf"}, "filter": filter, "limit": 30, "with_payload": true}, &out)
	if e != nil {
		return nil, e
	}
	keys := []string{}
	for _, p := range out.Result.Points {
		if p.Payload.Release != r.ID {
			return nil, fmt.Errorf("Qdrant release mismatch")
		}
		keys = append(keys, p.Payload.Key)
	}
	return keys, nil
}

// Persist the first generated vector before writing Qdrant. Concurrent retries
// replay identical points instead of changing an already validated collection.
func (q *Qdrant) frozenEmbedding(ctx context.Context, r Release, c Card) ([]float32, error) {
	key := Digest([]string{Digest(r), c.Key(), q.Profile()})
	if q.Store == nil {
		return q.Embedder.Embed(ctx, c.SearchText())
	}
	v, e := Load[[]float32](ctx, q.Store, "embedding", key)
	if e == nil {
		return v, nil
	}
	if !errors.Is(e, ErrNotFound) {
		return nil, e
	}
	v, e = q.Embedder.Embed(ctx, c.SearchText())
	if e != nil {
		return nil, e
	}
	e = Save(ctx, q.Store, "embedding", key, v)
	if e != nil && !errors.Is(e, ErrConflict) {
		return nil, e
	}
	return Load[[]float32](ctx, q.Store, "embedding", key)
}
