package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/workflow"
)

func TestLoadConfigFromEnvDefaultsToSQLite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENTALON_CONFIG_DIR", root)
	t.Setenv(EnvDatabaseDriver, "")
	t.Setenv(EnvDatabaseDSN, "")
	t.Setenv(EnvDatabaseAutoMigrate, "")

	config, err := LoadConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, DriverSQLite, config.Driver)
	assert.Equal(t, filepath.Join(root, "talon.db"), config.DSN)
	assert.True(t, config.AutoMigrate)
}

func TestLoadConfigFromEnvRequiresPostgresDSN(t *testing.T) {
	t.Setenv(EnvDatabaseDriver, "postgres")
	t.Setenv(EnvDatabaseDSN, "")
	_, err := LoadConfigFromEnv()
	require.ErrorContains(t, err, EnvDatabaseDSN)
}

func TestLoadConfigFromEnvUsesPostgresWithoutAutomaticMigration(t *testing.T) {
	t.Setenv(EnvDatabaseDriver, "postgres")
	t.Setenv(EnvDatabaseDSN, "postgres://talon:secret@localhost:5432/talon?sslmode=disable")
	t.Setenv(EnvDatabaseAutoMigrate, "")

	config, err := LoadConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, DriverPostgres, config.Driver)
	assert.False(t, config.AutoMigrate)
	assert.Equal(t, 10, config.MaxOpenConns)
}

func TestOpenWithoutMigrationReportsMissingSchema(t *testing.T) {
	_, err := Open(context.Background(), Config{
		Driver: DriverSQLite, DSN: filepath.Join(t.TempDir(), "empty.db"), MaxOpenConns: 1, MaxIdleConns: 1,
	})
	require.ErrorContains(t, err, "docs/sql")
}

func TestSQLiteApprovalStorePersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "talon.db")
	storage, err := OpenSQLite(ctx, path)
	require.NoError(t, err)
	created, err := storage.Approvals().Create(ctx, testApprovalRequest("persist"))
	require.NoError(t, err)
	require.NoError(t, storage.Close())

	reopened, err := OpenSQLite(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.Approvals().Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created, persisted)
}

func TestSQLiteApprovalStoreContract(t *testing.T) {
	ctx := context.Background()
	storage, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	runApprovalStoreContract(t, storage.Approvals(), "sqlite")
	runExecutionStoreContract(t, storage.Executions(), "sqlite")
	runArtifactStoreContract(t, storage.RunArtifacts())
}

// PostgreSQL 契约测试默认跳过；设置 TALON_TEST_POSTGRES_DSN 后会连接真实测试库。
func TestPostgresApprovalStoreContract(t *testing.T) {
	dsn := os.Getenv("TALON_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TALON_TEST_POSTGRES_DSN to run PostgreSQL storage contract tests")
	}
	ctx := context.Background()
	storage, err := Open(ctx, Config{Driver: DriverPostgres, DSN: dsn, AutoMigrate: true, MaxOpenConns: 4, MaxIdleConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	runApprovalStoreContract(t, storage.Approvals(), "postgres-"+time.Now().UTC().Format("150405.000000000"))
	runExecutionStoreContract(t, storage.Executions(), "postgres-"+time.Now().UTC().Format("150405.000000000"))
	runArtifactStoreContract(t, storage.RunArtifacts())
}

func runArtifactStoreContract(t *testing.T, store runartifact.Store) {
	t.Helper()
	ctx := context.Background()
	recorder := runartifact.New("scenario-artifact-store", runartifact.Versions{
		AgentVersion: "agent/v1", DatasetVersion: "dataset/v1",
	})
	running := recorder.Snapshot()
	require.NoError(t, store.Upsert(ctx, running))
	persisted, err := store.Get(ctx, running.RunID)
	require.NoError(t, err)
	assert.Equal(t, "running", persisted.Outcome)
	assert.Equal(t, running.RunID, persisted.RunID)

	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	recorder.RecordToolCall("call-1", "query_logs", workflow.AgentActionRead, `{}`, `{"data":[{"code":"failed"}]}`, time.Now(), nil, false)
	recorder.EndAgentRun(workflow.Snapshot{State: workflow.StatePlanned}, nil)
	completed := recorder.Finish("resolved", workflow.Snapshot{State: workflow.StateResolved}, nil)
	require.NoError(t, store.Upsert(ctx, completed))
	persisted, err = store.Get(ctx, completed.RunID)
	require.NoError(t, err)
	assert.Equal(t, "completed", persisted.Outcome)
	assert.Equal(t, "resolved", persisted.StopReason)
	require.Len(t, persisted.AgentRuns, 1)
	assert.NotEmpty(t, persisted.AgentRuns[0].NewEvidenceRefs)

	_, err = store.Get(ctx, "00000000-0000-4000-8000-000000000000")
	assert.ErrorIs(t, err, runartifact.ErrNotFound)
}

func runApprovalStoreContract(t *testing.T, store approval.Store, prefix string) {
	t.Helper()
	ctx := context.Background()
	request := testApprovalRequest(prefix)
	created, err := store.Create(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, approval.StatusPending, created.Status)
	repeated, err := store.Create(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, created, repeated)

	changed := request
	changed.ActionDigest = "changed-digest"
	_, err = store.Create(ctx, changed)
	assert.ErrorIs(t, err, approval.ErrConflict)
	pending, err := store.ListPending(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pending)

	decision := approval.Decision{
		ID: request.ID, PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
		Status: approval.StatusApproved, DecidedBy: "oncall", DecisionReason: "verified",
	}
	decided, err := store.Decide(ctx, decision)
	require.NoError(t, err)
	assert.Equal(t, approval.StatusApproved, decided.Status)
	repeatedDecision, err := store.Decide(ctx, decision)
	require.NoError(t, err)
	assert.Equal(t, decided, repeatedDecision)
	decision.Status = approval.StatusRejected
	decision.DecidedBy = "other"
	decision.DecisionReason = "too risky"
	_, err = store.Decide(ctx, decision)
	assert.ErrorIs(t, err, approval.ErrAlreadyDecided)
}

func TestSQLiteApprovalDecisionHasSingleConcurrentWinner(t *testing.T) {
	ctx := context.Background()
	storage, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	request := testApprovalRequest("concurrent")
	_, err = storage.Approvals().Create(ctx, request)
	require.NoError(t, err)

	decisions := []approval.Decision{
		{ID: request.ID, PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest, Status: approval.StatusApproved, DecidedBy: "a"},
		{ID: request.ID, PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest, Status: approval.StatusRejected, DecidedBy: "b", DecisionReason: "reject"},
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, decision := range decisions {
		wait.Add(1)
		go func(value approval.Decision) {
			defer wait.Done()
			_, decideErr := storage.Approvals().Decide(ctx, value)
			results <- decideErr
		}(decision)
	}
	wait.Wait()
	close(results)
	var succeeded, rejected int
	for result := range results {
		if result == nil {
			succeeded++
		} else if errors.Is(result, approval.ErrAlreadyDecided) {
			rejected++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, rejected)
}

func testApprovalRequest(prefix string) approval.Request {
	actionID := prefix + "-plan-action-1"
	return approval.Request{
		ID: approval.RequestID(actionID), IncidentID: prefix + "-incident", PlanID: prefix + "-plan",
		ActionID: actionID, ActionDigest: "digest", DryRunOperationID: prefix + "-dry-run",
		ToolName: "rollback_mapping", Arguments: map[string]any{"target_version": "mapping-v1"},
		Risk: "medium", PolicyReason: "approval required",
	}
}
