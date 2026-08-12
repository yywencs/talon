package approval

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteStorePersistsApprovalAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.db")
	store, err := NewSQLiteStore(path)
	require.NoError(t, err)
	fixed := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }

	created, err := store.Create(context.Background(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, StatusPending, created.Status)
	assert.Equal(t, fixed, created.RequestedAt)
	require.NoError(t, store.Close())

	reopened, err := NewSQLiteStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.Get(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created, persisted)
	pending, err := reopened.ListPending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, created.ID, pending[0].ID)
}

func TestSQLiteStoreCreateIsIdempotentAndDetectsDigestConflict(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "approvals.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.Create(context.Background(), testRequest())
	require.NoError(t, err)
	repeated, err := store.Create(context.Background(), testRequest())
	require.NoError(t, err)
	assert.Equal(t, first, repeated)

	changed := testRequest()
	changed.ActionDigest = "different-digest"
	_, err = store.Create(context.Background(), changed)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestSQLiteStoreDecisionIsAtomicAndImmutable(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "approvals.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Create(context.Background(), testRequest())
	require.NoError(t, err)

	decision := testDecision(StatusApproved, "oncall-a", "scope verified")
	approved, err := store.Decide(context.Background(), decision)
	require.NoError(t, err)
	assert.Equal(t, StatusApproved, approved.Status)
	require.NotNil(t, approved.DecidedAt)

	repeated, err := store.Decide(context.Background(), decision)
	require.NoError(t, err)
	assert.Equal(t, approved, repeated)
	_, err = store.Decide(context.Background(), testDecision(StatusRejected, "oncall-b", "too risky"))
	assert.ErrorIs(t, err, ErrAlreadyDecided)
	pending, err := store.ListPending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestSQLiteStoreConcurrentDecisionHasSingleWinner(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "approvals.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Create(context.Background(), testRequest())
	require.NoError(t, err)

	decisions := []Decision{
		testDecision(StatusApproved, "oncall-a", "approved"),
		testDecision(StatusRejected, "oncall-b", "rejected"),
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(decisions))
	for _, decision := range decisions {
		wait.Add(1)
		go func(value Decision) {
			defer wait.Done()
			_, decideErr := store.Decide(context.Background(), value)
			errorsFound <- decideErr
		}(decision)
	}
	wait.Wait()
	close(errorsFound)
	var succeeded, alreadyDecided int
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrAlreadyDecided) {
			alreadyDecided++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, alreadyDecided)
}

func testRequest() Request {
	return Request{
		ID: "incident-plan-2-action-1:approval", IncidentID: "incident-001",
		PlanID: "incident-plan-2", ActionID: "incident-plan-2-action-1", ActionDigest: "digest-001",
		DryRunOperationID: "dry-run-001", ToolName: "rollback_mapping",
		Arguments: map[string]any{"target_version": "mapping-v1"}, Risk: "medium",
		PolicyReason: "capability requires approval",
	}
}

func testDecision(status Status, approver, reason string) Decision {
	request := testRequest()
	return Decision{
		ID: request.ID, PlanID: request.PlanID, ActionID: request.ActionID, ActionDigest: request.ActionDigest,
		Status: status, DecidedBy: approver, DecisionReason: reason,
	}
}
