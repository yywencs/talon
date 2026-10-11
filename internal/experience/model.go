// Package experience implements the offline learning and versioned retrieval boundary.
// It never executes repairs and never treats historical evidence as current evidence.
package experience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const Schema = "talon.experience/v1"

var ErrNotFound = errors.New("experience object not found")
var ErrConflict = errors.New("experience object changed or immutable ID reused")

// Store persists immutable objects and a CAS-protected active release pointer.
// expected=0 creates, expected>0 replaces that revision. Domain objects use create only.
type Store interface {
	Get(context.Context, string, string) (json.RawMessage, int64, error)
	Put(context.Context, string, string, int64, json.RawMessage) error
	List(context.Context, string) ([]json.RawMessage, error)
}
type Source struct {
	RunID          string   `json:"run_id"`
	ScenarioID     string   `json:"scenario_id"`
	DatasetVersion string   `json:"dataset_version"`
	SnapshotDigest string   `json:"snapshot_digest"`
	EvidenceRefs   []string `json:"evidence_refs"`
}
type Check struct {
	Observe  string `json:"observe"`
	Supports string `json:"supports"`
	RulesOut string `json:"rules_out"`
}
type Card struct {
	Schema            string   `json:"schema"`
	ID                string   `json:"id"`
	Version           int      `json:"version"`
	Title             string   `json:"title"`
	Symptoms          []string `json:"symptoms"`
	Component         string   `json:"component"`
	ContextTags       []string `json:"context_tags"`
	CauseFamily       string   `json:"cause_family"`
	ApplicableWhen    []string `json:"applicable_when"`
	Checks            []Check  `json:"checks"`
	ConditionalAction string   `json:"conditional_action"`
	Verification      string   `json:"verification"`
	Exceptions        []string `json:"exceptions"`
	Sources           []Source `json:"sources"`
	Supersedes        []string `json:"supersedes,omitempty"`
}

func (c Card) Key() string { return fmt.Sprintf("%s@%d", c.ID, c.Version) }
func (c Card) SearchText() string {
	parts := []string{c.Title, c.Component}
	parts = append(parts, c.Symptoms...)
	parts = append(parts, c.ContextTags...)
	parts = append(parts, c.ApplicableWhen...)
	for _, check := range c.Checks {
		parts = append(parts, check.Observe, check.Supports, check.RulesOut)
	}
	parts = append(parts, c.Exceptions...)
	return strings.Join(parts, "\n")
}

func (c Card) Validate() error {
	if c.Schema != Schema || !validID(c.ID) || c.Version < 1 || strings.TrimSpace(c.Title) == "" || len(c.Symptoms) == 0 || len(c.ApplicableWhen) == 0 || len(c.Checks) == 0 || c.CauseFamily == "" || c.Verification == "" || len(c.Exceptions) == 0 || len(c.Sources) == 0 {
		return errors.New("incomplete experience card")
	}
	b, _ := json.Marshal(c)
	if len(b) > 24000 {
		return errors.New("experience exceeds 24KB; split into focused cards")
	}
	for _, v := range c.Checks {
		if v.Observe == "" || v.Supports == "" || v.RulesOut == "" {
			return errors.New("checks require observation, supporting and excluding signals")
		}
	}
	for _, s := range c.Sources {
		if s.RunID == "" || s.SnapshotDigest == "" || len(s.EvidenceRefs) == 0 {
			return errors.New("source references required")
		}
	}
	return nil
}
func validID(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func Save(ctx context.Context, s Store, kind, key string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	old, _, e := s.Get(ctx, kind, key)
	if e == nil {
		if Digest(json.RawMessage(old)) == Digest(json.RawMessage(b)) {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(e, ErrNotFound) {
		return e
	}
	return s.Put(ctx, kind, key, 0, b)
}
func Load[T any](ctx context.Context, s Store, kind, key string) (T, error) {
	var v T
	b, _, e := s.Get(ctx, kind, key)
	if e == nil {
		e = json.Unmarshal(b, &v)
	}
	return v, e
}

type Release struct {
	ID      string `json:"id"`
	Parent  string `json:"parent"`
	Cards   []Card `json:"cards"`
	Profile string `json:"profile"`
}
type Pointer struct {
	Release  string `json:"release"`
	Previous string `json:"previous"`
}
type Query struct {
	Query            string   `json:"query" jsonschema:"required,description=当前观察的故障摘要；不要把未经证实的根因作为事实"`
	Symptoms         []string `json:"symptoms,omitempty"`
	Observations     []string `json:"observations,omitempty"`
	Component        string   `json:"component,omitempty"`
	ContextTags      []string `json:"context_tags,omitempty"`
	AttemptedActions []string `json:"attempted_actions,omitempty"`
	Limit            int      `json:"limit,omitempty"`
}

func (q Query) Text() string {
	parts := []string{q.Query, q.Component}
	parts = append(parts, q.Symptoms...)
	parts = append(parts, q.Observations...)
	parts = append(parts, q.ContextTags...)
	parts = append(parts, q.AttemptedActions...)
	return strings.Join(parts, "\n")
}

type Summary struct {
	ID             string   `json:"id"`
	Version        int      `json:"version"`
	Title          string   `json:"title"`
	ApplicableWhen []string `json:"applicable_when"`
	Checks         []Check  `json:"checks"`
	Exceptions     []string `json:"exceptions"`
}
type SearchResult struct {
	Release string    `json:"release"`
	Trust   string    `json:"trust"`
	Items   []Summary `json:"items"`
}
type Reader interface {
	Search(context.Context, Query) (SearchResult, error)
	Get(context.Context, string, int) (Card, error)
	Evidence(context.Context, string, int, string, string) (json.RawMessage, error)
}
type Index interface {
	Profile() string
	Build(context.Context, Release) error
	Search(context.Context, Release, Query) ([]string, error)
}
type Ranker interface {
	Rank(context.Context, string, []Card) ([]int, error)
}
