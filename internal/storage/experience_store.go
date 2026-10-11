package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/wen/opentalon/internal/experience"
)

type experienceStore struct {
	db     *sql.DB
	driver Driver
}

func (s *Storage) Experiences() experience.Store {
	return &experienceStore{s.db, s.checkpoints.(*sqlCheckpointStore).driver}
}
func (s *experienceStore) Get(ctx context.Context, kind, key string) (json.RawMessage, int64, error) {
	var b []byte
	var rev int64
	e := s.db.QueryRowContext(ctx, bindSQL(s.driver, `SELECT payload, revision FROM experience_objects WHERE kind=? AND object_key=?`), kind, key).Scan(&b, &rev)
	if errors.Is(e, sql.ErrNoRows) {
		e = experience.ErrNotFound
	}
	return b, rev, e
}
func (s *experienceStore) Put(ctx context.Context, kind, key string, expected int64, b json.RawMessage) error {
	var r sql.Result
	var e error
	if expected == 0 {
		r, e = s.db.ExecContext(ctx, bindSQL(s.driver, `INSERT INTO experience_objects(kind,object_key,revision,payload) VALUES(?,?,1,?) ON CONFLICT(kind,object_key) DO NOTHING`), kind, key, string(b))
	} else {
		r, e = s.db.ExecContext(ctx, bindSQL(s.driver, `UPDATE experience_objects SET payload=?, revision=revision+1 WHERE kind=? AND object_key=? AND revision=?`), string(b), kind, key, expected)
	}
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n != 1 {
		return experience.ErrConflict
	}
	return e
}
func (s *experienceStore) List(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, e := s.db.QueryContext(ctx, bindSQL(s.driver, `SELECT payload FROM experience_objects WHERE kind=? ORDER BY object_key`), kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}
