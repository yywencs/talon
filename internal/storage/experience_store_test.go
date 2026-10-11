package storage

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
	"path/filepath"
	"testing"
)

func TestExperienceCASAndDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "experience.db")
	db, e := OpenSQLite(ctx, path)
	require.NoError(t, e)
	s := db.Experiences()
	require.NoError(t, s.Put(ctx, "pointer", "active", 0, json.RawMessage(`{"release":"v1"}`)))
	require.ErrorIs(t, s.Put(ctx, "pointer", "active", 0, json.RawMessage(`{}`)), experience.ErrConflict)
	require.NoError(t, s.Put(ctx, "pointer", "active", 1, json.RawMessage(`{"release":"v2"}`)))
	require.ErrorIs(t, s.Put(ctx, "pointer", "active", 1, json.RawMessage(`{}`)), experience.ErrConflict)
	require.NoError(t, db.Close())
	db, e = OpenSQLite(ctx, path)
	require.NoError(t, e)
	defer db.Close()
	b, rev, e := db.Experiences().Get(ctx, "pointer", "active")
	require.NoError(t, e)
	require.EqualValues(t, 2, rev)
	require.Contains(t, string(b), "v2")
}
