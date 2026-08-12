package approval

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDatabasePathUsesOpenTalonConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENTALON_CONFIG_DIR", root)

	path, err := DefaultDatabasePath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "talon.db"), path)
}

func TestOpenDefaultSQLiteStorePersistsApproval(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENTALON_CONFIG_DIR", root)
	store, err := OpenDefaultSQLiteStore()
	require.NoError(t, err)
	_, err = store.Create(context.Background(), testRequest())
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := OpenDefaultSQLiteStore()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	_, err = reopened.Get(context.Background(), testRequest().ID)
	require.NoError(t, err)
}
