package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/approval"
	"github.com/wen/opentalon/internal/platform"
	storagepkg "github.com/wen/opentalon/internal/storage"
	"github.com/wen/opentalon/internal/workflow"
)

func TestPlanPolicyPersistsAndDecidesActionApproval(t *testing.T) {
	processor, instance, service := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
	})
	database, err := storagepkg.OpenSQLite(context.Background(), t.TempDir()+"/approvals.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	processor, err = NewPlanProcessor(service, instance, WithApprovalStore(database.Approvals()))
	require.NoError(t, err)

	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	pending, err := processor.ListPendingApprovals(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, decisions[0].ActionID, pending[0].ActionID)
	assert.Equal(t, decisions[0].ActionDigest, pending[0].ActionDigest)
	assert.Equal(t, "rollback_mapping", pending[0].ToolName)

	_, err = processor.Approve(context.Background(), approvalRequest(decisions[0], "oncall", "verified"))
	require.NoError(t, err)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	pending, err = processor.ListPendingApprovals(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
	persisted, err := database.Approvals().Get(context.Background(), approval.RequestID(decisions[0].ActionID))
	require.NoError(t, err)
	assert.Equal(t, approval.StatusApproved, persisted.Status)
	assert.Equal(t, "oncall", persisted.DecidedBy)
}

func TestPlanPolicyAutoApprovesExplicitLowRiskAction(t *testing.T) {
	processor, instance, service := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "low", RequiresApproval: false,
	})

	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, workflow.PlanPolicyAutoApproved, decisions[0].Outcome)
	assert.Equal(t, "low_risk_auto_approved", decisions[0].ReasonCode)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	require.Len(t, instance.Snapshot().PlanPolicies, 1)
	assert.Empty(t, instance.Snapshot().PlanApprovals)

	repeated, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, decisions, repeated)
	assert.Equal(t, 1, service.capabilitiesCalls)
}

func TestPlanPolicyRequiresApprovalAndBindsDecisionToAction(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
	})

	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	decision := decisions[0]
	assert.Equal(t, workflow.PlanPolicyApprovalRequired, decision.Outcome)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	_, err = processor.Approve(context.Background(), ApprovalRequest{
		PlanID: "another-plan", ActionID: decision.ActionID, ActionDigest: decision.ActionDigest, Approver: "oncall@example.com",
	})
	require.Error(t, err)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)
	_, err = processor.Approve(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, ActionID: decision.ActionID, ActionDigest: "tampered-digest", Approver: "oncall@example.com",
	})
	require.ErrorContains(t, err, "does not match persisted action")

	request := approvalRequest(decision, "oncall@example.com", "rollback scope verified")
	approval, err := processor.Approve(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, decision.ActionID, approval.ActionID)
	assert.Equal(t, workflow.PlanApprovalApproved, approval.Decision)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	require.Len(t, instance.Snapshot().PlanApprovals, 1)

	repeated, err := processor.Approve(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, approval, repeated)
	request.Approver = "different@example.com"
	_, err = processor.Approve(context.Background(), request)
	require.ErrorContains(t, err, "already decided")
}

func TestPlanPolicyWaitsForEveryRequiredActionApproval(t *testing.T) {
	instance := plannedWorkflowWithActions(t, "incident-multi", []workflow.PlannedAction{
		{ToolName: "rollback_mapping", Arguments: map[string]any{"idempotency_key": "rollback-001"}},
		{ToolName: "restart_service", Arguments: map[string]any{"idempotency_key": "restart-001"}},
	})
	service := &recordingPlatform{
		operation: platform.Operation{ID: "dry-run", Status: platform.OperationSucceeded},
		capabilities: []platform.RemediationCapability{
			{Name: "rollback_mapping", Risk: "medium", RequiresApproval: true},
			{Name: "restart_service", Risk: "high", RequiresApproval: true},
		},
	}
	database, err := storagepkg.OpenSQLite(context.Background(), t.TempDir()+"/approvals.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	processor, err := NewPlanProcessor(service, instance, WithApprovalStore(database.Approvals()))
	require.NoError(t, err)
	results, err := processor.DryRun(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 2)
	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 2)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	_, err = processor.Approve(context.Background(), approvalRequest(decisions[0], "oncall-a", "first action checked"))
	require.NoError(t, err)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)
	require.Len(t, instance.Snapshot().PlanApprovals, 1)

	_, err = processor.Approve(context.Background(), approvalRequest(decisions[1], "oncall-b", "second action checked"))
	require.NoError(t, err)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	require.Len(t, instance.Snapshot().PlanApprovals, 2)
}

func TestPlanPolicyHumanActionRejectionReturnsToReinvestigation(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
	})
	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	decision := decisions[0]

	_, err = processor.Reject(context.Background(), approvalRequest(decision, "oncall@example.com", ""))
	require.ErrorContains(t, err, "reason is required")
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	approval, err := processor.Reject(context.Background(), approvalRequest(decision, "oncall@example.com", "blast radius is too large"))
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanApprovalRejected, approval.Decision)
	assert.Equal(t, workflow.StateInvestigating, instance.Snapshot().State)
}

func TestPlanPolicyRejectsUnavailableActionCapability(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t)

	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, workflow.PlanPolicyRejected, decisions[0].Outcome)
	assert.Equal(t, "capability_not_available", decisions[0].ReasonCode)
	assert.Equal(t, workflow.StateBlocked, instance.Snapshot().State)
}

func TestPlanPolicyUsesApprovalAsSafeDefaultForUnknownRisk(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "", RequiresApproval: false,
	})

	decisions, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, workflow.PlanPolicyApprovalRequired, decisions[0].Outcome)
	assert.Equal(t, "unknown", decisions[0].Risk)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)
}

func TestPlanPolicyRequiresEveryActionDryRun(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{"idempotency_key": "rollback-001"})
	service := &recordingPlatform{capabilities: []platform.RemediationCapability{{Name: "rollback_mapping", Risk: "low"}}}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	_, err = processor.EvaluatePolicy(context.Background())
	require.ErrorContains(t, err, "successful dry run")
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)
	assert.Zero(t, service.capabilitiesCalls)
}

func TestPlanPolicyRefusesHumanApprovalWithoutPersistentStore(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{"idempotency_key": "rollback-001"})
	service := &recordingPlatform{
		operation: platform.Operation{ID: "dry-run-001", Status: platform.OperationSucceeded},
		capabilities: []platform.RemediationCapability{{
			Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
		}},
	}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)
	_, err = processor.DryRun(context.Background())
	require.NoError(t, err)

	_, err = processor.EvaluatePolicy(context.Background())
	require.ErrorContains(t, err, "approval store is required")
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)
	assert.Empty(t, instance.Snapshot().PlanPolicies)
}

func TestPlanPolicyPlatformFailureKeepsPlanPlanned(t *testing.T) {
	processor, instance, service := processorWithSuccessfulDryRun(t)
	service.capabilitiesErr = errors.New("catalog unavailable")

	_, err := processor.EvaluatePolicy(context.Background())
	require.ErrorContains(t, err, "catalog unavailable")
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)
	assert.Empty(t, instance.Snapshot().PlanPolicies)
}

func processorWithSuccessfulDryRun(t *testing.T, capabilities ...platform.RemediationCapability) (*PlanProcessor, *workflow.IncidentWorkflow, *recordingPlatform) {
	t.Helper()
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{
		operation:    platform.Operation{ID: "operation-dry-run-001", Status: platform.OperationSucceeded, Message: "dry run completed"},
		capabilities: capabilities,
	}
	database, err := storagepkg.OpenSQLite(context.Background(), t.TempDir()+"/approvals.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	processor, err := NewPlanProcessor(service, instance, WithApprovalStore(database.Approvals()))
	require.NoError(t, err)
	_, err = processor.DryRun(context.Background())
	require.NoError(t, err)
	return processor, instance, service
}

func plannedWorkflowWithActions(t *testing.T, incidentID string, actions []workflow.PlannedAction) *workflow.IncidentWorkflow {
	t.Helper()
	instance, err := workflow.NewIncidentWorkflow(workflow.Config{IncidentID: incidentID})
	require.NoError(t, err)
	_, err = instance.Apply(workflow.Event{Type: workflow.EventStartInvestigation, Actor: workflow.ActorController})
	require.NoError(t, err)
	_, err = instance.SubmitPlan(workflow.PlanDraft{
		Summary: "remediate incident", RootCause: "confirmed regression", EvidenceRefs: []string{"trace:001"},
		Stages: []workflow.PlanStageDraft{{StageID: "remediate", Goal: "remediate incident", Actions: actions,
			CheckpointPolicy: workflow.CheckpointPolicy{DefaultDecision: workflow.CheckpointSucceeded}}},
	})
	require.NoError(t, err)
	return instance
}

func approvalRequest(decision workflow.PlanPolicyDecision, approver, reason string) ApprovalRequest {
	return ApprovalRequest{
		PlanID: decision.PlanID, ActionID: decision.ActionID, ActionDigest: decision.ActionDigest,
		Approver: approver, Reason: reason,
	}
}
