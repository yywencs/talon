package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

func TestPlanPolicyAutoApprovesExplicitLowRiskCapability(t *testing.T) {
	processor, instance, service := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "low", RequiresApproval: false,
	})

	decision, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanPolicyAutoApproved, decision.Outcome)
	assert.Equal(t, "low_risk_auto_approved", decision.ReasonCode)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	require.NotNil(t, instance.Snapshot().PlanPolicy)
	assert.Nil(t, instance.Snapshot().PlanApproval)

	repeated, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, decision, repeated)
	assert.Equal(t, 1, service.capabilitiesCalls)
}

func TestPlanPolicyRequiresApprovalAndBindsHumanDecisionToPlan(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
	})

	decision, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanPolicyApprovalRequired, decision.Outcome)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	_, err = processor.Approve(context.Background(), ApprovalRequest{
		PlanID: "another-plan", Approver: "oncall@example.com",
	})
	require.Error(t, err)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	approval, err := processor.Approve(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, Approver: "oncall@example.com", Reason: "rollback scope verified",
	})
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanApprovalApproved, approval.Decision)
	assert.Equal(t, "oncall@example.com", approval.Approver)
	assert.Equal(t, workflow.StateRemediating, instance.Snapshot().State)
	require.NotNil(t, instance.Snapshot().PlanApproval)

	repeated, err := processor.Approve(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, Approver: "oncall@example.com", Reason: "rollback scope verified",
	})
	require.NoError(t, err)
	assert.Equal(t, approval, repeated)

	_, err = processor.Approve(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, Approver: "different@example.com",
	})
	require.ErrorContains(t, err, "immutable decision")
}

func TestPlanPolicyHumanRejectionReturnsToReinvestigation(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "medium", RequiresApproval: true,
	})
	decision, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)

	_, err = processor.Reject(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, Approver: "oncall@example.com",
	})
	require.ErrorContains(t, err, "reason is required")
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)

	approval, err := processor.Reject(context.Background(), ApprovalRequest{
		PlanID: decision.PlanID, Approver: "oncall@example.com", Reason: "blast radius is too large",
	})
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanApprovalRejected, approval.Decision)
	assert.Equal(t, workflow.StateReinvestigating, instance.Snapshot().State)
}

func TestPlanPolicyRejectsUnavailableCapability(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t)

	decision, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanPolicyRejected, decision.Outcome)
	assert.Equal(t, "capability_not_available", decision.ReasonCode)
	assert.Equal(t, workflow.StateReinvestigating, instance.Snapshot().State)
}

func TestPlanPolicyUsesApprovalAsSafeDefaultForUnknownRisk(t *testing.T) {
	processor, instance, _ := processorWithSuccessfulDryRun(t, platform.RemediationCapability{
		Name: "rollback_mapping", Risk: "", RequiresApproval: false,
	})

	decision, err := processor.EvaluatePolicy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, workflow.PlanPolicyApprovalRequired, decision.Outcome)
	assert.Equal(t, "unknown", decision.Risk)
	assert.Equal(t, "non_low_risk_requires_approval", decision.ReasonCode)
	assert.Equal(t, workflow.StateAwaitingApproval, instance.Snapshot().State)
}

func TestPlanPolicyRequiresSuccessfulDryRun(t *testing.T) {
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{capabilities: []platform.RemediationCapability{{Name: "rollback_mapping", Risk: "low"}}}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)

	_, err = processor.EvaluatePolicy(context.Background())
	require.ErrorContains(t, err, "requires a successful dry run")
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)
	assert.Zero(t, service.capabilitiesCalls)
}

func TestPlanPolicyPlatformFailureKeepsPlanPlanned(t *testing.T) {
	processor, instance, service := processorWithSuccessfulDryRun(t)
	service.capabilitiesErr = errors.New("catalog unavailable")

	_, err := processor.EvaluatePolicy(context.Background())
	require.ErrorContains(t, err, "catalog unavailable")
	assert.Equal(t, workflow.StatePlanned, instance.Snapshot().State)
	assert.Nil(t, instance.Snapshot().PlanPolicy)
}

func processorWithSuccessfulDryRun(t *testing.T, capabilities ...platform.RemediationCapability) (*PlanProcessor, *workflow.IncidentWorkflow, *recordingPlatform) {
	t.Helper()
	instance := plannedWorkflow(t, "incident-001", map[string]any{
		"tool_id": "generate_image", "target_version": "mapping-v1",
		"expected_version": "mapping-v2", "idempotency_key": "rollback-001",
	})
	service := &recordingPlatform{
		operation: platform.Operation{
			ID: "operation-dry-run-001", Status: platform.OperationSucceeded,
			Message: "dry run completed", Result: map[string]any{"dry_run": true},
		},
		capabilities: capabilities,
	}
	processor, err := NewPlanProcessor(service, instance)
	require.NoError(t, err)
	_, err = processor.DryRun(context.Background())
	require.NoError(t, err)
	return processor, instance, service
}
