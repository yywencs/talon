package runartifact

import (
	"fmt"
	"strings"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/workflow"
)

// 证据维度是提交修复意图或升级交接前的确定性完整性约束：模型可以自由形成结论，
// 但结论引用的证据必须来自真实查询并覆盖全部观测维度。维度按只读工具家族划分；
// get_services 只用于初始拓扑发现、不携带维度语义，故意不参与映射。
const (
	DimensionMetrics     = "metrics"
	DimensionLogs        = "logs"
	DimensionTrace       = "trace"
	DimensionConfigState = "config_state"
)

var evidenceDimensionTools = map[string]string{
	"query_metrics":           DimensionMetrics,
	"query_logs":              DimensionLogs,
	"query_traces":            DimensionTrace,
	"get_change_records":      DimensionConfigState,
	"get_config_versions":     DimensionConfigState,
	"get_connection_metadata": DimensionConfigState,
	"get_credential_metadata": DimensionConfigState,
	"get_providers":           DimensionConfigState,
	"get_routes":              DimensionConfigState,
}

// RequiredEvidenceDimensions 是修复意图引用必须覆盖的全部维度，固定顺序用于稳定输出。
var RequiredEvidenceDimensions = []string{DimensionMetrics, DimensionLogs, DimensionTrace, DimensionConfigState}

var dimensionLabels = map[string]string{
	DimensionMetrics:     "指标（query_metrics）",
	DimensionLogs:        "日志（query_logs）",
	DimensionTrace:       "链路（query_traces）",
	DimensionConfigState: "配置状态（get_change_records/get_config_versions/get_connection_metadata/get_credential_metadata/get_providers/get_routes）",
}

// EvidenceDimensionCoverage 统计两组维度：consulted 是这些运行中成功只读调用
// 已获得的维度；cited 是给定引用实际覆盖的维度。引用可以用调用 ID 或
// evidence_ref 命中，与 ValidateEvidenceRefs 的口径保持一致。该函数不依赖
// Recorder 内部状态，便于对历史 Artifact 离线回放门禁效果。
func EvidenceDimensionCoverage(runs []AgentRun, refs []string) (consulted map[string]struct{}, cited map[string]struct{}) {
	citedRefs := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if value := strings.TrimSpace(ref); value != "" {
			citedRefs[value] = struct{}{}
		}
	}
	consulted = make(map[string]struct{})
	cited = make(map[string]struct{})
	for _, run := range runs {
		for _, call := range run.ToolCalls {
			if call.Status != "succeeded" || call.Action != workflow.AgentActionRead {
				continue
			}
			dimension, known := evidenceDimensionTools[call.Name]
			if !known {
				continue
			}
			consulted[dimension] = struct{}{}
			_, byCallID := citedRefs[call.CallID]
			_, byEvidenceRef := citedRefs[call.EvidenceRef]
			if byCallID || byEvidenceRef {
				cited[dimension] = struct{}{}
			}
		}
	}
	return consulted, cited
}

// ValidateIntentEvidence 是 submit_execution_intent 的证据门禁：引用必须全部
// 真实，且覆盖四类观测维度。某维度无法获得不构成降低标准的理由——那正是
// critical_telemetry_missing 升级的适用场景：先升级人工，而不是带着不完整的
// 证据修复。
func (r *Recorder) ValidateIntentEvidence(refs []string) error {
	if r == nil {
		return fmt.Errorf("run artifact recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateEvidenceRefsLocked(refs, r.artifact.AgentRuns); err != nil {
		return err
	}
	_, cited := EvidenceDimensionCoverage(r.artifact.AgentRuns, refs)
	missing := make([]string, 0, len(RequiredEvidenceDimensions))
	for _, dimension := range RequiredEvidenceDimensions {
		if _, covered := cited[dimension]; !covered {
			missing = append(missing, dimensionLabels[dimension])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("证据引用缺少维度：%s。修复意图必须先查询并引用四类观测证据（指标、日志、链路、配置状态），把对应工具结果返回的 evidence_ref 加入 evidence_refs（时间窗以 harness_facts.virtual_time 为基准）；若某维度确实无法获得，不得提交修复，应调用 escalate_incident 并使用 reason_code=critical_telemetry_missing", strings.Join(missing, "、"))
	}
	return nil
}

// ValidateEscalationEvidence 是 escalate_incident 的证据门禁：引用必须全部真实、
// 不得遗漏调查中已获得的任何维度，并按 reasonCode 做升级前置校验——
//   - Gate A（先探测）：断言"无安全修复手段"或"凭据变更需人工"前，必须至少
//     探测过一次当前状态。历史窗口的错误只证明过去发生过故障，瞬时故障可能
//     已自愈，未探测不得断言无路可走。
//   - Gate B（如实申报预算）：能力目录中全部授权修复动作都已尝试时，自治修复
//     额度即已耗尽，reason_code 必须是 workflow_budget_exhausted——修复手段
//     存在且已试过，谎报为 no_safe_remediation_available 会误导人工判断。
//
// 维度不完备本身不是升级的障碍——查不齐正说明需要人工介入。
func (r *Recorder) ValidateEscalationEvidence(reasonCode platform.EscalationReasonCode, refs []string, authorizedTools []string) error {
	if r == nil {
		return fmt.Errorf("run artifact recorder is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateEvidenceRefsLocked(refs, r.artifact.AgentRuns); err != nil {
		return err
	}
	consulted, cited := EvidenceDimensionCoverage(r.artifact.AgentRuns, refs)
	missing := make([]string, 0, len(consulted))
	for _, dimension := range RequiredEvidenceDimensions {
		if _, obtained := consulted[dimension]; !obtained {
			continue
		}
		if _, covered := cited[dimension]; !covered {
			missing = append(missing, dimensionLabels[dimension])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("本次调查已获得以下维度的证据，但升级引用未包含：%s。升级交接必须完整移交全部已获得的证据，请把对应查询返回的 evidence_ref 加入 evidence_refs 后重试", strings.Join(missing, "、"))
	}
	probeAttempted := false
	attempted := make(map[string]struct{})
	for _, operation := range r.artifact.Operations {
		switch operation.Kind {
		case platform.OperationProbe:
			probeAttempted = true
		case platform.OperationRemediation:
			attempted[operation.Name] = struct{}{}
		}
	}
	if (reasonCode == platform.EscalationReasonNoSafeRemediationAvailable ||
		reasonCode == platform.EscalationReasonCredentialChangeRequiresHuman) && !probeAttempted {
		return fmt.Errorf("升级被拒：reason_code=%s 断言没有安全自治路径，但本次运行从未探测过当前状态。历史窗口的错误只证明过去发生过故障，瞬时故障可能已经自愈；请先提交一个包含 request_probe Stage 的有界执行意图（checkpoint_policy 使用 fail-closed 默认决策）验证故障当前状态，再基于探测结果决定恢复流量或提交升级", reasonCode)
	}
	if len(authorizedTools) > 0 && reasonCode != platform.EscalationReasonWorkflowBudgetExhausted {
		exhausted := true
		for _, name := range authorizedTools {
			if _, ok := attempted[name]; !ok {
				exhausted = false
				break
			}
		}
		if exhausted {
			return fmt.Errorf("升级被拒：能力目录中的全部授权修复动作（%s）都已尝试且未恢复，自治修复轮次已耗尽。此时必须如实使用 reason_code=workflow_budget_exhausted 升级，并在 handoff 中记录已尝试的动作与失败结果，供人工决策；不得谎报为无安全修复手段", strings.Join(authorizedTools, "、"))
		}
	}
	return nil
}
