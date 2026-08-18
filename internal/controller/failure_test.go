package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

func TestClassifyControllerErrorUsesOnlyStableErrorIdentity(t *testing.T) {
	timeout := classifyControllerError(workflow.FailureStageProbe, "probe_submit_failed", "探测提交失败",
		"plan-1", "", platform.Operation{}, context.DeadlineExceeded)
	assert.Equal(t, workflow.FailureCategoryPlatformUnavailable, timeout.Category)
	assert.Equal(t, workflow.FailureNextRetry, timeout.NextAction)
	assert.True(t, timeout.Retryable)
	assert.False(t, timeout.Fallback)

	unknown := classifyControllerError(workflow.FailureStageProbe, "probe_submit_failed", "探测提交失败",
		"plan-1", "", platform.Operation{}, errors.New("brand new provider error"))
	assert.Equal(t, workflow.FailureCategoryUnclassified, unknown.Category)
	assert.Equal(t, workflow.FailureNextEscalate, unknown.NextAction)
	assert.False(t, unknown.Retryable)
	assert.True(t, unknown.Fallback)
}

func TestUnknownRemediationResultRequiresReconciliation(t *testing.T) {
	failure := unknownResultFailure(workflow.FailureStageRemediation, "remediation_result_unknown",
		"修复调用结果未知", "plan-1", "action-1", platform.Operation{ID: "operation-1"}, errors.New("connection lost"))

	assert.Equal(t, workflow.FailureCategoryResultUnknown, failure.Category)
	assert.Equal(t, workflow.FailureNextReconcile, failure.NextAction)
	assert.False(t, failure.Retryable)
}
