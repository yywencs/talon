package storage

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/wen/opentalon/internal/runmeta"
	"github.com/wen/opentalon/internal/workflow"
)

func TestLoadPostgresConfigFromEnvRequiresDSN(t *testing.T) {
	t.Setenv(EnvDatabaseDSN, "")
	_, err := LoadPostgresConfigFromEnv()
	require.ErrorContains(t, err, EnvDatabaseDSN)
}

func TestLoadPostgresConfigFromEnvUsesPostgresWithoutAutomaticMigration(t *testing.T) {
	t.Setenv(EnvDatabaseDSN, "postgres://talon:secret@localhost:5432/talon?sslmode=disable")

	config, err := LoadPostgresConfigFromEnv()
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

func TestSQLiteAutoMigrationRenamesLegacyPlanColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	for _, name := range []string{
		"001_create_approval_requests.up.sql",
		"002_create_action_executions.up.sql",
		"003_add_action_polling_schedule.up.sql",
	} {
		script, readErr := os.ReadFile(filepath.Join("..", "..", "docs", "sql", "sqlite", name))
		require.NoError(t, readErr)
		_, execErr := db.ExecContext(ctx, string(script))
		require.NoError(t, execErr)
	}
	require.NoError(t, db.Close())

	storage, err := OpenSQLite(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	for _, table := range []string{"approval_requests", "action_executions"} {
		rows, queryErr := storage.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		require.NoError(t, queryErr)
		columns := map[string]bool{}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
			columns[name] = true
		}
		require.NoError(t, rows.Close())
		assert.True(t, columns["intent_id"], "%s must contain intent_id", table)
		assert.False(t, columns["plan_id"], "%s must not retain plan_id", table)
	}
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
	recorder := runartifact.New("scenario-artifact-store", runmeta.Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, runmeta.Config{})
	running := recorder.Snapshot()
	require.NoError(t, store.Upsert(ctx, running))
	persisted, err := store.Get(ctx, running.RunID)
	require.NoError(t, err)
	assert.Equal(t, "running", persisted.Outcome)
	assert.Equal(t, running.RunID, persisted.RunID)

	recorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	recorder.RecordToolCall("call-1", "query_logs", workflow.AgentActionRead, `{}`, `{"data":[{"code":"failed"}]}`, time.Now(), nil, false)
	recorder.EndAgentRun(workflow.Snapshot{State: workflow.StateValidating}, nil)
	completed := recorder.Finish("resolved", workflow.Snapshot{State: workflow.StateResolved}, nil)
	require.NoError(t, store.Upsert(ctx, completed))
	persisted, err = store.Get(ctx, completed.RunID)
	require.NoError(t, err)
	assert.Equal(t, "completed", persisted.Outcome)
	assert.Equal(t, "resolved", persisted.StopReason)
	require.Len(t, persisted.AgentRuns, 1)
	assert.NotEmpty(t, persisted.AgentRuns[0].NewEvidenceRefs)
	listed, err := store.List(ctx, runartifact.VersionFilter{
		SchemaVersion: runartifact.SchemaVersion, CodeVersion: "test",
		DatasetVersion: "toolops-v1", Outcome: "completed",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, completed.RunID, listed[0].RunID)

	_, err = store.Get(ctx, "00000000-0000-4000-8000-000000000000")
	assert.ErrorIs(t, err, runartifact.ErrNotFound)

	// 模型输出的非法 Unicode 转义（NUL / 孤立代理对）经 RawMessage 透传进入
	// Artifact 时，不得让整次运行无法入库：持久化边界应替换为 U+FFFD。
	escape := func(hex string) string { return string(unicodeEscapeProbe) + hex }
	poisonRecorder := runartifact.New("scenario-artifact-poison", runmeta.Provenance{CodeVersion: "test", DatasetVersion: "toolops-v1"}, runmeta.Config{})
	poisonRecorder.BeginAgentRun("investigate", workflow.Snapshot{State: workflow.StateInvestigating})
	poisonRecorder.RecordToolCall("call-poison", "query_logs", workflow.AgentActionRead,
		`{"q":"`+escape("0000")+`"}`, `{"data":[{"note":"x`+escape("d83d")+`y"}]}`, time.Now(), nil, false)
	poisoned := poisonRecorder.Finish("failed", workflow.Snapshot{State: workflow.StateInvestigating}, errors.New("synthetic failure"))
	require.NoError(t, store.Upsert(ctx, poisoned))
	persistedPoison, err := store.Get(ctx, poisoned.RunID)
	require.NoError(t, err)
	require.Len(t, persistedPoison.AgentRuns, 1)
	persistedPoisonText, err := json.Marshal(persistedPoison)
	require.NoError(t, err)
	assert.NotContains(t, string(persistedPoisonText), escape("0000"))
	assert.NotContains(t, string(persistedPoisonText), escape("d83d"))
	// 修复后的 Artifact 仍可正常解码回结构；无效码位已替换为 U+FFFD。
	// 注意表示形式因驱动而异：JSONB 读回是原始 U+FFFD 字符，SQLite 原样
	// 保留替换转义文本，因此断言解码后的语义值而非字节形式。
	assert.Equal(t, poisoned.RunID, persistedPoison.RunID)
	require.Len(t, persistedPoison.AgentRuns[0].ToolCalls, 1)
	replacementChar := string(rune(0xFFFD))
	var arguments map[string]any
	require.NoError(t, json.Unmarshal(persistedPoison.AgentRuns[0].ToolCalls[0].Arguments, &arguments))
	assert.Equal(t, replacementChar, arguments["q"])
	var output struct {
		Data []struct {
			Note string `json:"note"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(persistedPoison.AgentRuns[0].ToolCalls[0].Output, &output))
	require.Len(t, output.Data, 1)
	assert.Equal(t, "x"+replacementChar+"y", output.Data[0].Note)
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
		ID: request.ID, IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
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
		{ID: request.ID, IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest, Status: approval.StatusApproved, DecidedBy: "a"},
		{ID: request.ID, IntentID: request.IntentID, ActionID: request.ActionID, ActionDigest: request.ActionDigest, Status: approval.StatusRejected, DecidedBy: "b", DecisionReason: "reject"},
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
	actionID := prefix + "-intent-action-1"
	return approval.Request{
		ID: approval.RequestID(actionID), IncidentID: prefix + "-incident", IntentID: prefix + "-intent",
		ActionID: actionID, ActionDigest: "digest", DryRunOperationID: prefix + "-dry-run",
		ToolName: "rollback_mapping", Arguments: map[string]any{"target_version": "mapping-v1"},
		Risk: "medium", PolicyReason: "approval required",
	}
}
