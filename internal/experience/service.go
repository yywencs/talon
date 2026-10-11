package experience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

type Service struct {
	Store  Store
	Index  Index
	Ranker Ranker
}

func (s *Service) Active(ctx context.Context) (string, error) {
	p, e := Load[Pointer](ctx, s.Store, "pointer", "active")
	if errors.Is(e, ErrNotFound) {
		return "", nil
	}
	return p.Release, e
}
func (s *Service) Stage(ctx context.Context, id, parent string, keys []string) (Release, error) {
	r := Release{ID: id, Parent: parent, Profile: s.Index.Profile(), Cards: []Card{}}
	if !validID(id) || id == parent || len(keys) == 0 {
		return r, errors.New("new release ID and candidate keys required")
	}
	if parent != "" {
		if _, e := Load[json.RawMessage](ctx, s.Store, "published", parent); e != nil {
			return r, e
		}
		old, e := Load[Release](ctx, s.Store, "release", parent)
		if e != nil {
			return r, e
		}
		r.Cards = append(r.Cards, old.Cards...)
	}
	for _, key := range keys {
		c, e := Load[Card](ctx, s.Store, "candidate", key)
		if e != nil {
			return r, e
		}
		if e = c.Validate(); e != nil {
			return r, e
		}
		if e = CheckSources(ctx, s.Store, c); e != nil {
			return r, e
		}
		review, e := Load[Review](ctx, s.Store, "review", ReviewKey(c, parent, keys...))
		if e != nil {
			return r, fmt.Errorf("review required for %s: %w", key, e)
		}
		if !review.Supported || review.CardDigest != Digest(c) || review.Parent != parent {
			return r, errors.New("candidate review rejected or stale")
		}

		kept := r.Cards[:0]
		for _, old := range r.Cards {
			remove := old.ID == c.ID
			for _, k := range c.Supersedes {
				remove = remove || old.Key() == k
			}
			if !remove {
				kept = append(kept, old)
			}
		}
		r.Cards = append(kept, c)
	}
	sort.Slice(r.Cards, func(i, j int) bool { return r.Cards[i].Key() < r.Cards[j].Key() })
	seen := map[string]bool{}
	for _, c := range r.Cards {
		d := Digest(c.Checks)
		if seen[d] {
			return r, errors.New("duplicate diagnostic checks; merge candidates before staging")
		}
		seen[d] = true
	}
	return r, Save(ctx, s.Store, "release", id, r)
}
func (s *Service) IndexRelease(ctx context.Context, id string) error {
	r, e := Load[Release](ctx, s.Store, "release", id)
	if e != nil {
		return e
	}
	if r.Profile != s.Index.Profile() {
		return errors.New("index profile mismatch")
	}
	if _, err := Load[string](ctx, s.Store, "indexed", id); err == nil {
		return s.checkIndex(ctx, r)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if e = s.Index.Build(ctx, r); e != nil {
		return e
	}
	return Save(ctx, s.Store, "indexed", id, Digest(r))
}
func (s *Service) Publish(ctx context.Context, id, reportID string) error {
	r, e := Load[Release](ctx, s.Store, "release", id)
	if e != nil {
		return e
	}
	report, e := Load[Report](ctx, s.Store, "report", reportID)
	if e != nil {
		return e
	}
	if report.ReleaseDigest != Digest(r) || report.Release != id || report.Baseline != r.Parent || !report.Passed {
		return errors.New("matching passing validation report required")
	}
	if e = s.checkIndex(ctx, r); e != nil {
		return e
	}
	if e = Save(ctx, s.Store, "published", id, struct{ Digest, Report string }{Digest(r), reportID}); e != nil {
		return e
	}
	return s.activate(ctx, id, r.Parent)
}
func (s *Service) Rollback(ctx context.Context, id, expected string) error {
	if _, e := s.Open(ctx, id, false); e != nil {
		return e
	}
	return s.activate(ctx, id, expected)
}
func (s *Service) activate(ctx context.Context, id, expected string) error {
	b, rev, e := s.Store.Get(ctx, "pointer", "active")
	if errors.Is(e, ErrNotFound) {
		rev = 0
		b = []byte(`{}`)
	} else if e != nil {
		return e
	}
	var p Pointer
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	if p.Release == id {
		return nil
	}
	if p.Release != expected {
		return ErrConflict
	}
	out, _ := json.Marshal(Pointer{id, p.Release})
	return s.Store.Put(ctx, "pointer", "active", rev, out)
}
func (s *Service) checkIndex(ctx context.Context, r Release) error {
	d, e := Load[string](ctx, s.Store, "indexed", r.ID)
	if e != nil {
		return e
	}
	if d != Digest(r) || r.Profile != s.Index.Profile() {
		return errors.New("index manifest or profile mismatch")
	}
	return nil
}
func (s *Service) Open(ctx context.Context, id string, validation bool) (*Session, error) {
	if id == "" {
		return nil, errors.New("explicit release required")
	}
	r, e := Load[Release](ctx, s.Store, "release", id)
	if e != nil {
		return nil, e
	}
	if !validation {
		if _, e = Load[json.RawMessage](ctx, s.Store, "published", id); e != nil {
			return nil, errors.New("release is not published")
		}
	}
	if e = s.checkIndex(ctx, r); e != nil {
		return nil, e
	}
	return &Session{service: s, release: r}, nil
}

type Session struct {
	service *Service
	release Release
}

func (s *Session) ReleaseID() string { return s.release.ID }
func (s *Session) Search(ctx context.Context, q Query) (SearchResult, error) {
	out := SearchResult{Release: s.release.ID, Trust: "historical_reference_not_current_evidence", Items: []Summary{}}
	if q.Query == "" || len(q.Text()) > 8000 {
		return out, errors.New("query required, maximum 8KB")
	}
	if q.Limit <= 0 {
		q.Limit = 5
	}
	if q.Limit > 5 {
		q.Limit = 5
	}
	keys, e := s.service.Index.Search(ctx, s.release, q)
	if e != nil {
		return out, e
	}
	cards := []Card{}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, c := range s.release.Cards {
			if c.Key() == key {
				cards = append(cards, c)
				break
			}
		}
		if len(cards) >= 30 {
			break
		}
	}
	if len(cards) == 0 {
		return out, nil
	}
	if s.service.Ranker == nil {
		return out, errors.New("reranker required")
	}
	order, e := s.service.Ranker.Rank(ctx, q.Text(), cards)
	if e != nil {
		return out, e
	}
	used := map[int]bool{}
	budget := 0
	for _, i := range order {
		if i < 0 || i >= len(cards) || used[i] {
			return out, errors.New("invalid reranker index")
		}
		used[i] = true
		c := cards[i]
		v := Summary{c.ID, c.Version, c.Title, c.ApplicableWhen, c.Checks, c.Exceptions}
		b, _ := json.Marshal(v)
		if budget+len(b) > 12000 {
			continue
		}
		budget += len(b)
		out.Items = append(out.Items, v)
		if len(out.Items) >= q.Limit {
			break
		}
	}
	return out, nil
}
func (s *Session) Get(_ context.Context, id string, v int) (Card, error) {
	for _, c := range s.release.Cards {
		if c.ID == id && c.Version == v {
			return c, nil
		}
	}
	return Card{}, ErrNotFound
}
func (s *Session) Evidence(ctx context.Context, id string, v int, run, ref string) (json.RawMessage, error) {
	c, e := s.Get(ctx, id, v)
	if e != nil {
		return nil, e
	}
	for _, src := range c.Sources {
		if src.RunID != run {
			continue
		}
		for _, allowed := range src.EvidenceRefs {
			if ref != allowed {
				continue
			}
			snap, e := Load[Snapshot](ctx, s.service.Store, "snapshot", src.SnapshotDigest)
			if e != nil {
				return nil, e
			}
			if Digest(snap) != src.SnapshotDigest {
				return nil, errors.New("snapshot digest mismatch")
			}
			for _, ev := range snap.Evidence {
				if ev.Ref == ref {
					if len(ev.Content) > 32000 {
						return nil, fmt.Errorf("evidence exceeds online size limit; inspect offline snapshot %s", src.SnapshotDigest)
					}
					return ev.Content, nil
				}
			}
		}
	}
	return nil, ErrNotFound
}
