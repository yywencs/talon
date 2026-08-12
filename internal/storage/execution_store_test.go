package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/execution"
)

func TestExecutionStoreLeaseTakeoverAndStrictOrder(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	store := database.executions.(*sqlExecutionStore)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	specs := executionSpecsForTest("lease")
	records, err := store.Prepare(ctx, specs)
	require.NoError(t, err)
	require.Len(t, records, 2)

	first, err := store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "worker-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, specs[0].ActionID, first.ActionID)
	assert.Equal(t, 1, first.Attempt)
	_, err = store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "worker-b", LeaseDuration: time.Minute})
	assert.ErrorIs(t, err, execution.ErrNoClaimable)

	now = now.Add(2 * time.Minute)
	takenOver, err := store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "worker-b", LeaseDuration: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, specs[0].ActionID, takenOver.ActionID)
	assert.Equal(t, 2, takenOver.Attempt)
	_, err = store.Complete(ctx, first.ActionID, "worker-a", "operation-old", "succeeded", execution.StatusSucceeded, "")
	assert.ErrorIs(t, err, execution.ErrLeaseNotOwned)
	_, err = store.Complete(ctx, takenOver.ActionID, "worker-b", "operation-1", "succeeded", execution.StatusSucceeded, "")
	require.NoError(t, err)

	second, err := store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "worker-c", LeaseDuration: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, specs[1].ActionID, second.ActionID)
}

func TestExecutionStoreSameOwnerReusesLiveLeaseWithoutNewAttempt(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	store := database.executions.(*sqlExecutionStore)
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	spec := executionSpecsForTest("owned")[:1]
	_, err = store.Prepare(ctx, spec)
	require.NoError(t, err)
	first, err := store.ClaimNext(ctx, execution.Claim{PlanID: spec[0].PlanID, OwnerID: "worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	repeated, err := store.ClaimNext(ctx, execution.Claim{PlanID: spec[0].PlanID, OwnerID: "worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, first.ActionID, repeated.ActionID)
	assert.Equal(t, first.Attempt, repeated.Attempt)
}

func TestExecutionStoreConcurrentClaimHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "talon.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	spec := executionSpecsForTest("race")[:1]
	_, err = database.Executions().Prepare(ctx, spec)
	require.NoError(t, err)

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, owner := range []string{"worker-a", "worker-b"} {
		wait.Add(1)
		go func(worker string) {
			defer wait.Done()
			_, claimErr := database.Executions().ClaimNext(ctx, execution.Claim{
				PlanID: spec[0].PlanID, OwnerID: worker, LeaseDuration: time.Minute,
			})
			results <- claimErr
		}(owner)
	}
	wait.Wait()
	close(results)
	var success, busy int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, execution.ErrNoClaimable) {
			busy++
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 1, busy)
}

func runExecutionStoreContract(t *testing.T, store execution.Store, prefix string) {
	t.Helper()
	ctx := context.Background()
	specs := executionSpecsForTest(prefix + "-contract")
	records, err := store.Prepare(ctx, specs)
	require.NoError(t, err)
	require.Len(t, records, 2)
	repeated, err := store.Prepare(ctx, specs)
	require.NoError(t, err)
	assert.Equal(t, records, repeated)

	first, err := store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "contract-worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = store.RecordOperation(ctx, first.ActionID, "contract-worker", "contract-operation-1", "running", execution.PollSchedule{
		NextPollAt: now.Add(time.Millisecond), OperationDeadline: now.Add(time.Minute),
	})
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	first, err = store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "contract-worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	first, err = store.Complete(ctx, first.ActionID, "contract-worker", "contract-operation-1", "succeeded", execution.StatusSucceeded, "")
	require.NoError(t, err)
	assert.Equal(t, execution.StatusSucceeded, first.Status)

	second, err := store.ClaimNext(ctx, execution.Claim{PlanID: specs[0].PlanID, OwnerID: "contract-worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, specs[1].ActionID, second.ActionID)
}

func executionSpecsForTest(prefix string) []execution.Spec {
	return []execution.Spec{
		{IncidentID: prefix + "-incident", PlanID: prefix + "-plan", ActionID: prefix + "-action-1", ActionDigest: "digest-1", Sequence: 1, ToolName: "first", IdempotencyKey: prefix + "-action-1:execute"},
		{IncidentID: prefix + "-incident", PlanID: prefix + "-plan", ActionID: prefix + "-action-2", ActionDigest: "digest-2", Sequence: 2, ToolName: "second", IdempotencyKey: prefix + "-action-2:execute"},
	}
}
