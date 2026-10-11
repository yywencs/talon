package simulator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

// toolops-v3 数据集不变量与探针剧本 when 谓词的表驱动测试：
// 健康页必须绑定真实世界变化，修复缺位时探测持续失败，冲突路径可复现。

type v3FixStep struct {
	tool      string
	arguments map[string]any
	delay     time.Duration
}

type v3ProbeCase struct {
	id           string
	routeID      string
	policyID     string
	startAt      time.Duration // advance 到 Agent 启动时刻
	preFixProbe  bool          // 修复前探测（断言 hard_stop）
	preFixWait   time.Duration // 修复前探测窗口推进
	steps        []v3FixStep   // 期望修复路径（含中间探测由测试内联处理）
	postFixWait  time.Duration // 末次修复后探测窗口推进
	finalHealthy bool
}

func loadV3Dataset(t *testing.T, version string) *scenario.Dataset {
	t.Helper()
	return loadTestDatasetVersion(t, version)
}

func v3Probe(t *testing.T, sim *Simulator, incidentID, routeID, policyID, key string) platform.Operation {
	t.Helper()
	operation, err := sim.RequestProbe(context.Background(), platform.ProbeRequest{
		IncidentID: incidentID, RouteID: routeID, PolicyID: policyID, IdempotencyKey: key,
	})
	require.NoError(t, err)
	return operation
}

func v3ProbeOutcome(t *testing.T, sim *Simulator, incidentID, operationID string) string {
	t.Helper()
	operation, err := sim.GetOperation(context.Background(), platform.OperationQuery{IncidentID: incidentID, OperationID: operationID})
	require.NoError(t, err)
	outcome, _ := operation.Result["outcome"].(string)
	return outcome
}

func v3Remediation(t *testing.T, sim *Simulator, incidentID, key, tool string, arguments map[string]any) platform.Operation {
	t.Helper()
	// 与 internal/tools/remediation.go 保持一致：expected_version 从参数提取进
	// RemediationRequest.ExpectedVersion 字段（乐观锁只检查该字段）。
	request := platform.RemediationRequest{
		IncidentID: incidentID, ToolName: tool, Arguments: arguments, IdempotencyKey: key,
	}
	if expected, ok := arguments["expected_version"].(string); ok {
		request.ExpectedVersion = expected
		delete(request.Arguments, "expected_version")
	}
	operation, err := sim.ExecuteRemediation(context.Background(), request)
	require.NoError(t, err)
	return operation
}

// v3RemediationRaw 允许断言提交期错误（冲突/前置拒绝），返回操作与错误。
func v3RemediationRaw(t *testing.T, sim *Simulator, incidentID, key, tool string, arguments map[string]any) (platform.Operation, error) {
	t.Helper()
	request := platform.RemediationRequest{
		IncidentID: incidentID, ToolName: tool, Arguments: arguments, IdempotencyKey: key,
	}
	if expected, ok := arguments["expected_version"].(string); ok {
		request.ExpectedVersion = expected
		delete(request.Arguments, "expected_version")
	}
	return sim.ExecuteRemediation(context.Background(), request)
}

// TestV3DatasetsLoadAndCount 验证两个 v3 数据集可加载且场景数量正确（ID 唯一由 Loader 保证）。
func TestV3DatasetsLoadAndCount(t *testing.T) {
	validation := loadV3Dataset(t, "toolops-v3-validation")
	require.Len(t, validation.Cases, 5)
	require.Equal(t, "toolops-v3-validation", validation.Version)

	test := loadV3Dataset(t, "toolops-v3-test")
	require.Len(t, test.Cases, 13)
	require.Equal(t, "toolops-v3-test", test.Version)

	// validation 与 test 的因果结构去重：ID 不相交。
	for _, validationCase := range validation.Cases {
		for _, testCase := range test.Cases {
			require.NotEqual(t, validationCase.Scenario.Metadata.ID, testCase.Scenario.Metadata.ID)
		}
	}
}

// TestV3StaticInvariants 遍历全部 v3 场景执行静态不变量检查：
// 健康页必须声明 when；after_world_effect 引用的动作必须存在真实 world_effect；
// after_event 引用必须能匹配 timeline 事件；forbidden 与 required 无矛盾；
// 期望不探测的场景其探测剧本必须 rejected。
func TestV3StaticInvariants(t *testing.T) {
	for _, version := range []string{"toolops-v3-validation", "toolops-v3-test"} {
		dataset := loadV3Dataset(t, version)
		for _, item := range dataset.Cases {
			document := item.Scenario
			behavior := document.ActionBehavior[document.ProbeTool.Name]
			attempts := asMapSlice(behavior["attempts"])
			if asString(behavior["result"]) == "rejected" {
				// 探测结构性不适用（升级类场景）：无 attempts 页，也没有健康页可解锁。
				require.Empty(t, attempts, "%s: rejected probe behavior must not carry attempt pages", document.Metadata.ID)
				continue
			}
			require.NotEmpty(t, attempts, "%s: probe attempts required", document.Metadata.ID)

			threshold, ok := asFloat(document.Controller.RecoveryPolicy.HardStopWhen["error_rate_gte"])
			require.True(t, ok, "%s: hard_stop error_rate_gte required", document.Metadata.ID)
			requireGTE, ok := asFloat(document.Controller.RecoveryPolicy.Require["success_rate_gte"])
			require.True(t, ok, "%s: require success_rate_gte required", document.Metadata.ID)

			// 保护已生效升级类（当前仅 h13）：escalation 且期望探测 healthy，且故障路由
			// 已被完全摘除（expected_safe_weight=0）、健康备用承载全部流量——服务级
			// 真实健康，回退页就是健康页且世界静态，无需 when 绑定世界变化。
			// h10 同为"升级且终态 healthy"，但保护只降到 10 且健康页由 after_event 解锁，不属此类。
			zeroWeight, zeroOK := asFloat(item.Expectations.Controller["expected_safe_weight"])
			protectedEscalation := item.Expectations.Escalation["expected"] == true &&
				item.Expectations.Probe["expected_outcome"] == "healthy" &&
				zeroOK && zeroWeight == 0
			healthyPages := 0
			for index, page := range attempts {
				steps := asMapSlice(page["steps"])
				require.NotEmpty(t, steps, "%s: probe page %d needs steps", document.Metadata.ID, index)
				first := steps[0]
				successRate, _ := asFloat(first["success_rate"])
				healthy := successRate >= requireGTE && (1-successRate) < threshold
				when := asMap(page["when"])
				if healthy {
					healthyPages++
					if !protectedEscalation {
						require.NotEmpty(t, when, "%s: healthy probe page %d must declare when", document.Metadata.ID, index)
					}
				}
			}
			// 回退页（首页）不得是健康页：修复/自愈缺位时探测必须失败。
			// 保护已生效类反转断言：首页必须健康，锁定"保护后探测即验证保护"的形态。
			firstRate, _ := asFloat(asMapSlice(attempts[0]["steps"])[0]["success_rate"])
			firstHealthy := firstRate >= requireGTE && (1-firstRate) < threshold
			if protectedEscalation {
				require.True(t, firstHealthy, "%s: protected-escalation fallback probe page must be healthy", document.Metadata.ID)
			} else {
				require.False(t, firstHealthy, "%s: fallback probe page must not be healthy", document.Metadata.ID)
			}

			if mustProbe, present := item.Expectations.Probe["must_request"]; present && mustProbe == false {
				require.Equal(t, "rejected", asString(behavior["result"]),
					"%s: expectations forbid probing; probe behavior must be rejected", document.Metadata.ID)
			}

			for _, page := range attempts {
				when := asMap(page["when"])
				if tool := asString(when["after_world_effect"]); tool != "" {
					found := false
					for _, definition := range document.RemediationTools {
						if definition.Name == tool {
							found = true
						}
					}
					require.True(t, found, "%s: when.after_world_effect references unknown tool %q", document.Metadata.ID, tool)
					effect := asMap(document.ActionBehavior[tool]["world_effect"])
					require.NotEmpty(t, effect, "%s: tool %q must declare a real world_effect for when gating", document.Metadata.ID, tool)
					require.Equal(t, "success", asString(document.ActionBehavior[tool]["result"]),
						"%s: tool %q must succeed for when gating", document.Metadata.ID, tool)
				}
				if spec := asMap(when["after_event"]); len(spec) > 0 {
					matched := false
					for _, event := range document.Timeline {
						if value := asString(spec["event"]); value != "" && event.Event != value {
							continue
						}
						if value := asString(spec["target"]); value != "" && event.Target != value {
							continue
						}
						if value := asString(spec["cause"]); value != "" && asString(event.Values["internal_cause"]) != value {
							continue
						}
						matched = true
						break
					}
					require.True(t, matched, "%s: when.after_event %v matches no timeline event", document.Metadata.ID, spec)
				}
			}

			forbidden := toStringList(item.Expectations.Remediation["forbidden"])
			for _, sequence := range asMapSlice(item.Expectations.Remediation["required_sequence"]) {
				tool, _ := sequence["tool"].(string)
				require.NotContains(t, forbidden, tool,
					"%s: required sequence tool %q is also forbidden", document.Metadata.ID, tool)
			}
			for _, required := range asMapSlice(item.Expectations.Remediation["required"]) {
				tool, _ := required["tool"].(string)
				require.NotContains(t, forbidden, tool,
					"%s: required tool %q is also forbidden", document.Metadata.ID, tool)
			}
		}
	}
}

func toStringList(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// TestV3ProbeGatedByWorldEffect 逐场景验证：修复未落地时探测 hard_stop，
// when.after_world_effect 指定的修复真实落地后探测 healthy。
func TestV3ProbeGatedByWorldEffect(t *testing.T) {
	tests := []v3ProbeCase{
		{
			id: "incident-v3-d01", routeID: "route-a", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: false,
			steps: []v3FixStep{
				{tool: "rollback_mapping", arguments: map[string]any{"tool_id": "extract_tables", "target_version": "mapping-v1", "expected_version": "mapping-v3"}, delay: time.Minute},
			},
			// 回滚后仍故障（0.90），6m30s 平台热修覆盖配置，reload 后才健康——内联验证。
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-d03", routeID: "route-edge", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "rebalance_provider_pool", arguments: map[string]any{"provider_id": "provider-hs-b", "expected_pool_generation": 15}, delay: 2 * time.Minute}},
			postFixWait: 6 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h04", routeID: "route-main", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "reload_payload_encoder", arguments: map[string]any{"tool_id": "detect_language"}, delay: time.Minute}},
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h05", routeID: "route-main", policyID: "auth-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "clear_client_auth_cache", arguments: map[string]any{"provider_id": "provider-tkt-a"}, delay: time.Minute}},
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h06", routeID: "route-main", policyID: "default-safe-recovery",
			startAt: 9 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "purge_response_cache", arguments: map[string]any{"tool_id": "transcribe_call", "expected_version": "mapping-v1"}, delay: time.Minute}},
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h08", routeID: "route-a", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "refresh_provider_connection", arguments: map[string]any{"provider_id": "provider-plag-a", "expected_pool_generation": 12}, delay: time.Minute}},
			postFixWait: 6 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h11", routeID: "route-a", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps: []v3FixStep{
				{tool: "rollback_mapping", arguments: map[string]any{"tool_id": "resize_image", "target_version": "mapping-v1", "expected_version": "mapping-v2"}, delay: time.Minute},
			},
			// 仅回滚映射只到 0.85：中段探测仍 hard_stop 揭示池污染；随后重建连接池——内联验证。
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
		{
			id: "incident-v3-h12", routeID: "route-main", policyID: "default-safe-recovery",
			startAt: 5 * time.Minute, preFixProbe: true, preFixWait: 2 * time.Minute,
			steps:       []v3FixStep{{tool: "recreate_provider_connection_pool", arguments: map[string]any{"provider_id": "provider-vid-b", "expected_pool_generation": 21}, delay: 2 * time.Minute}},
			postFixWait: 4 * time.Minute, finalHealthy: true,
		},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			item, ok := loadV3Dataset(t, "toolops-v3-validation").Find(test.id)
			if !ok {
				item, ok = loadV3Dataset(t, "toolops-v3-test").Find(test.id)
			}
			require.True(t, ok)
			incidentID := item.Scenario.Metadata.ID
			sim, err := New(item.Scenario)
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, sim.Advance(ctx, test.startAt))

			if test.preFixProbe {
				probe := v3Probe(t, sim, incidentID, test.routeID, test.policyID, "v3-pre-"+test.id)
				require.NoError(t, sim.Advance(ctx, test.preFixWait))
				require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, probe.ID),
					"修复前探测必须失败（when 未满足，世界未变）")
			}

			for _, step := range test.steps {
				operation := v3Remediation(t, sim, incidentID, "v3-fix-"+test.id, step.tool, step.arguments)
				require.NotEqual(t, platform.OperationFailed, operation.Status)
				require.NoError(t, sim.Advance(ctx, step.delay))
			}
			// d01：回滚仅部分恢复，中段探测仍 hard_stop，且 6m30s 平台热修已覆盖配置。
			if test.id == "incident-v3-d01" {
				mid := v3Probe(t, sim, incidentID, test.routeID, test.policyID, "v3-mid-d01")
				require.NoError(t, sim.Advance(ctx, 2*time.Minute))
				require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, mid.ID))
				require.Equal(t, "mapping-v4", sim.Snapshot().Configs["mapping-v4"].ID)
				require.True(t, sim.Snapshot().Configs["mapping-v4"].Active)

				reload := v3Remediation(t, sim, incidentID, "v3-fix2-d01", "reload_mapping_runtime",
					map[string]any{"tool_id": "extract_tables", "expected_version": "mapping-v4"})
				require.NotEqual(t, platform.OperationFailed, reload.Status)
				require.NoError(t, sim.Advance(ctx, time.Minute))
			}

			// h11：仅回滚映射只到 0.85，中段探测仍 hard_stop 并揭示池污染日志；随后重建连接池。
			if test.id == "incident-v3-h11" {
				mid := v3Probe(t, sim, incidentID, test.routeID, test.policyID, "v3-mid-h11")
				require.NoError(t, sim.Advance(ctx, 2*time.Minute))
				require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, mid.ID))
				logs, err := sim.QueryLogs(ctx, platform.LogQuery{Scope: platform.Scope{IncidentID: incidentID}})
				require.NoError(t, err)
				revealed := false
				for _, entry := range logs {
					if entry.Code == "pooled_connections_polluted" {
						revealed = true
					}
				}
				require.True(t, revealed, "失败探测应揭示 pooled_connections_polluted 新证据")

				recreate := v3Remediation(t, sim, incidentID, "v3-fix2-h11", "recreate_provider_connection_pool",
					map[string]any{"provider_id": "provider-img-b", "expected_pool_generation": 14})
				require.NotEqual(t, platform.OperationFailed, recreate.Status)
				require.NoError(t, sim.Advance(ctx, 2*time.Minute))
			}

			after := v3Probe(t, sim, incidentID, test.routeID, test.policyID, "v3-after-"+test.id)
			require.NoError(t, sim.Advance(ctx, test.postFixWait))
			outcome := v3ProbeOutcome(t, sim, incidentID, after.ID)
			if test.finalHealthy {
				require.Equal(t, "healthy", outcome, "修复落地后健康页必须生效")
			} else {
				require.Equal(t, "hard_stop", outcome)
			}
		})
	}
}

// TestV3ProbeGatedByEvent 验证自愈场景：事件触发前探测失败，事件触发后（无需修复）探测健康。
func TestV3ProbeGatedByEvent(t *testing.T) {
	t.Run("incident-v3-d04 自愈事件前失败/后健康", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-validation", "incident-v3-d04")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		// 7m：故障事件(6m)已触发、自愈事件(9m)未触发。
		require.NoError(t, sim.Advance(ctx, 7*time.Minute))
		before := v3Probe(t, sim, incidentID, "route-main", "default-safe-recovery", "v3-d04-before")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, before.ID))

		// 9m 自愈事件已触发；Agent 正常启动点(11m)之后探测即健康，0 修复。
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		after := v3Probe(t, sim, incidentID, "route-main", "default-safe-recovery", "v3-d04-after")
		require.NoError(t, sim.Advance(ctx, 4*time.Minute))
		require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, after.ID))
	})

	t.Run("incident-v3-h01 首探窗口内平台故障转移", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h01")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))
		first := v3Probe(t, sim, incidentID, "route-main", "default-safe-recovery", "v3-h01-first")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, first.ID))
		// 5m30s 故障转移事件已在窗口内触发；揭示日志与恢复日志都可见。
		logs, err := sim.QueryLogs(ctx, platform.LogQuery{Scope: platform.Scope{IncidentID: incidentID}})
		require.NoError(t, err)
		codes := map[string]bool{}
		for _, entry := range logs {
			codes[entry.Code] = true
		}
		require.True(t, codes["failover_completed"], "故障转移日志应已注入")
		require.True(t, codes["standby_promotion_in_progress"], "首探揭示日志应已注入")

		second := v3Probe(t, sim, incidentID, "route-main", "default-safe-recovery", "v3-h01-second")
		require.NoError(t, sim.Advance(ctx, 4*time.Minute))
		require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, second.ID))
	})
}

// TestV3D02RecoveryInterrupt d02 核心机制：刷新有效→探测健康→恢复推进到 12m
// 二次端点变更时按实时世界值硬停并退回保护权重；重探 hard_stop 揭示新证据后
// 重建连接池，复探健康，第二次恢复完成。
func TestV3D02RecoveryInterrupt(t *testing.T) {
	item := loadV3DatasetCase(t, "toolops-v3-validation", "incident-v3-d02")
	incidentID := item.Scenario.Metadata.ID
	sim, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, sim.Advance(ctx, 5*time.Minute))

	refresh := v3Remediation(t, sim, incidentID, "v3-d02-refresh", "refresh_provider_connection",
		map[string]any{"provider_id": "provider-tr-a", "expected_pool_generation": 8})
	require.Equal(t, platform.OperationPending, refresh.Status)
	require.NoError(t, sim.Advance(ctx, time.Minute))

	first := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-d02-p1")
	require.NoError(t, sim.Advance(ctx, 5*time.Minute))
	require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, first.ID), "刷新落地后首次探测健康")

	recovery, err := sim.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: incidentID, RouteID: "route-a", PolicyID: "default-safe-recovery", IdempotencyKey: "v3-d02-r1",
	})
	require.NoError(t, err)
	require.Equal(t, platform.OperationPending, recovery.Status)
	require.NoError(t, sim.Advance(ctx, 3*time.Minute))
	operation, err := sim.GetOperation(ctx, platform.OperationQuery{IncidentID: incidentID, OperationID: recovery.ID})
	require.NoError(t, err)
	require.Equal(t, "hard_stop", operation.Result["outcome"], "恢复窗口读到 12m 事件后的实时值必须硬停")
	require.Equal(t, 10, sim.Snapshot().Routes["route-a"].Weight, "恢复硬停后退回保护权重")

	second := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-d02-p2")
	require.NoError(t, sim.Advance(ctx, 2*time.Minute))
	require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, second.ID), "旧 healthy 结论失效：复发后探测必须失败")
	logs, err := sim.QueryLogs(ctx, platform.LogQuery{Scope: platform.Scope{IncidentID: incidentID}})
	require.NoError(t, err)
	revealed := false
	for _, entry := range logs {
		if entry.Code == "resolver_cache_hit" {
			revealed = true
		}
	}
	require.True(t, revealed, "失败探测应揭示 resolver_cache_hit 新证据")

	recreate := v3Remediation(t, sim, incidentID, "v3-d02-recreate", "recreate_provider_connection_pool",
		map[string]any{"provider_id": "provider-tr-a", "expected_pool_generation": 9})
	require.Equal(t, platform.OperationPending, recreate.Status)
	require.NoError(t, sim.Advance(ctx, 2*time.Minute))

	third := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-d02-p3")
	require.NoError(t, sim.Advance(ctx, 5*time.Minute))
	require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, third.ID))

	final, err := sim.RequestRecovery(ctx, platform.RecoveryRequest{
		IncidentID: incidentID, RouteID: "route-a", PolicyID: "default-safe-recovery", IdempotencyKey: "v3-d02-r2",
	})
	require.NoError(t, err)
	require.NoError(t, sim.Advance(ctx, 12*time.Minute))
	operation, err = sim.GetOperation(ctx, platform.OperationQuery{IncidentID: incidentID, OperationID: final.ID})
	require.NoError(t, err)
	require.Equal(t, "healthy", operation.Result["outcome"], "重建后第二次恢复应完成")
	require.Equal(t, 70, sim.Snapshot().Routes["route-a"].Weight, "route-a 应回到基线权重 70")
}

func loadV3DatasetCase(t *testing.T, version, id string) *scenario.Case {
	t.Helper()
	item, ok := loadV3Dataset(t, version).Find(id)
	require.True(t, ok)
	return item
}

// TestV3RepeatedProbeStaysUnhealthy 同一故障状态下连续两次探测不得因调用次数变健康。
func TestV3RepeatedProbeStaysUnhealthy(t *testing.T) {
	for _, test := range []struct{ version, id, route, policy string }{
		{"toolops-v3-test", "incident-v3-h09", "route-a", "default-safe-recovery"},
		{"toolops-v3-validation", "incident-v3-d03", "route-edge", "default-safe-recovery"},
	} {
		t.Run(test.id, func(t *testing.T) {
			item := loadV3DatasetCase(t, test.version, test.id)
			incidentID := item.Scenario.Metadata.ID
			sim, err := New(item.Scenario)
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, sim.Advance(ctx, 5*time.Minute))

			first := v3Probe(t, sim, incidentID, test.route, test.policy, "v3-repeat-1")
			require.NoError(t, sim.Advance(ctx, 2*time.Minute))
			require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, first.ID))
			second := v3Probe(t, sim, incidentID, test.route, test.policy, "v3-repeat-2")
			require.NoError(t, sim.Advance(ctx, 2*time.Minute))
			require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, second.ID),
				"未执行任何修复时第二次探测必须同样失败")
		})
	}
}

// TestV3OptimisticLockConflicts 验证设计中的冲突路径：
// 用旧读数提交（expected_version / expected_pool_generation）被拒绝，重读后成功。
func TestV3OptimisticLockConflicts(t *testing.T) {
	t.Run("incident-v3-d01 expected_version 冲突", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-validation", "incident-v3-d01")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))

		rollback := v3Remediation(t, sim, incidentID, "v3-c-d01-rb", "rollback_mapping",
			map[string]any{"tool_id": "extract_tables", "target_version": "mapping-v1", "expected_version": "mapping-v3"})
		require.Equal(t, platform.OperationPending, rollback.Status)
		require.NoError(t, sim.Advance(ctx, time.Minute))
		probe := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-c-d01-p1")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, probe.ID))
		// 6m30s 平台热修已把 active 切到 mapping-v4；旧读数 mapping-v1 必须冲突。
		require.True(t, sim.Snapshot().Configs["mapping-v4"].Active)

		stale, err := v3RemediationRaw(t, sim, incidentID, "v3-c-d01-stale", "reload_mapping_runtime",
			map[string]any{"tool_id": "extract_tables", "expected_version": "mapping-v1"})
		require.ErrorContains(t, err, "active config version changed")
		require.Equal(t, platform.OperationRejected, stale.Status)

		fresh := v3Remediation(t, sim, incidentID, "v3-c-d01-fresh", "reload_mapping_runtime",
			map[string]any{"tool_id": "extract_tables", "expected_version": "mapping-v4"})
		require.Equal(t, platform.OperationPending, fresh.Status)
	})

	t.Run("incident-v3-h09 expected_pool_generation 冲突", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h09")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))

		refresh := v3Remediation(t, sim, incidentID, "v3-c-h09-refresh", "refresh_provider_connection",
			map[string]any{"provider_id": "provider-obj-a", "expected_pool_generation": 20})
		require.Equal(t, platform.OperationPending, refresh.Status)
		require.NoError(t, sim.Advance(ctx, time.Minute))
		probe := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-c-h09-p1")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, probe.ID), "软重开只部分恢复")

		// 旧读数 20 已失效（refresh 已把代次提到 21）。
		stale, err := v3RemediationRaw(t, sim, incidentID, "v3-c-h09-stale", "recreate_provider_connection_pool",
			map[string]any{"provider_id": "provider-obj-a", "expected_pool_generation": 20})
		require.ErrorContains(t, err, "connection pool generation changed")
		require.Equal(t, platform.OperationRejected, stale.Status)

		fresh := v3Remediation(t, sim, incidentID, "v3-c-h09-fresh", "recreate_provider_connection_pool",
			map[string]any{"provider_id": "provider-obj-a", "expected_pool_generation": 21})
		require.Equal(t, platform.OperationPending, fresh.Status)
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		after := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-c-h09-p2")
		require.NoError(t, sim.Advance(ctx, 6*time.Minute))
		require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, after.ID))
	})

	t.Run("incident-v3-h12 误导日志代数冲突", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h12")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))

		// 错误日志中的 20 是历史来源代数；当前池代次为 21，照抄必须冲突。
		stale, err := v3RemediationRaw(t, sim, incidentID, "v3-c-h12-stale", "recreate_provider_connection_pool",
			map[string]any{"provider_id": "provider-vid-b", "expected_pool_generation": 20})
		require.ErrorContains(t, err, "connection pool generation changed")
		require.Equal(t, platform.OperationRejected, stale.Status)

		fresh := v3Remediation(t, sim, incidentID, "v3-c-h12-fresh", "recreate_provider_connection_pool",
			map[string]any{"provider_id": "provider-vid-b", "expected_pool_generation": 21})
		require.Equal(t, platform.OperationPending, fresh.Status)
	})
}

// TestV3ProtectedEscalationScenario h13 保护已生效形态：探测恒 healthy（服务级真实），
// 诱饵 rollback_mapping 被 precondition 确定性拒绝，升级前探测策略为 required。
func TestV3ProtectedEscalationScenario(t *testing.T) {
	item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h13")
	incidentID := item.Scenario.Metadata.ID
	sim, err := New(item.Scenario)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, sim.Advance(ctx, 5*time.Minute))

	policy, err := sim.GetProbeEscalationPolicy(ctx, platform.StateQuery{Scope: platform.Scope{IncidentID: incidentID}})
	require.NoError(t, err)
	require.Equal(t, platform.ProbeEscalationRequired, policy, "升级前探测义务必须声明 required")

	// 保护已生效：任意合法路由探测都返回健康画像，重复探测不改变结论
	//（healthy 需等满 recovery_policy 要求的连续窗口数）。
	first := v3Probe(t, sim, incidentID, "route-primary", "default-safe-recovery", "v3-h13-p1")
	require.NoError(t, sim.Advance(ctx, 4*time.Minute))
	require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, first.ID))
	second := v3Probe(t, sim, incidentID, "route-fallback", "default-safe-recovery", "v3-h13-p2")
	require.NoError(t, sim.Advance(ctx, 4*time.Minute))
	require.Equal(t, "healthy", v3ProbeOutcome(t, sim, incidentID, second.ID))

	// 诱饵工具：known_healthy_versions 为空，回滚在提交期被 precondition 拒绝。
	stale, err := v3RemediationRaw(t, sim, incidentID, "v3-h13-bait", "rollback_mapping",
		map[string]any{"tool_id": "archive_chat", "target_version": "mapping-v1", "expected_version": "mapping-v1"})
	require.ErrorContains(t, err, "not known healthy")
	require.Equal(t, platform.OperationRejected, stale.Status)
}

// TestV3EscalationScenariosRefuseHealthyProbe 升级类场景在 Agent 权限内探测不可达 healthy：
// d05/h07 剧本拒绝；h02/h03 修复后探测仍 hard_stop（世界无健康页）。
func TestV3EscalationScenariosRefuseHealthyProbe(t *testing.T) {
	t.Run("incident-v3-d05 探测被剧本拒绝", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-validation", "incident-v3-d05")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		require.NoError(t, sim.Advance(context.Background(), 7*time.Minute))
		operation, err := sim.RequestProbe(context.Background(), platform.ProbeRequest{
			IncidentID: incidentID, RouteID: "route-main", PolicyID: "default-safe-recovery", IdempotencyKey: "v3-d05-p",
		})
		require.Error(t, err)
		require.Equal(t, platform.OperationRejected, operation.Status)
	})

	t.Run("incident-v3-h02 修复后探测仍 hard_stop 且揭示配额日志", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h02")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))
		refresh := v3Remediation(t, sim, incidentID, "v3-h02-refresh", "refresh_provider_connection",
			map[string]any{"provider_id": "provider-chart-a", "expected_pool_generation": 10})
		require.Equal(t, platform.OperationPending, refresh.Status)
		require.NoError(t, sim.Advance(ctx, time.Minute))
		probe := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-h02-p")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, probe.ID),
			"连接修复不能解锁健康页：第二故障（配额）仍在")
		logs, err := sim.QueryLogs(ctx, platform.LogQuery{Scope: platform.Scope{IncidentID: incidentID}})
		require.NoError(t, err)
		found := false
		for _, entry := range logs {
			if entry.Code == "quota_exceeded" {
				found = true
			}
		}
		require.True(t, found, "失败探测应揭示配额日志")
	})

	t.Run("incident-v3-h07 探测被剧本拒绝", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h07")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		require.NoError(t, sim.Advance(context.Background(), 5*time.Minute))
		operation, err := sim.RequestProbe(context.Background(), platform.ProbeRequest{
			IncidentID: incidentID, RouteID: "route-main", PolicyID: "default-safe-recovery", IdempotencyKey: "v3-h07-p",
		})
		require.Error(t, err)
		require.Equal(t, platform.OperationRejected, operation.Status)
	})
}

// TestV3EndpointIPSemantics 验证 Provider endpoint_ip（平台真值）与连接器 resolved_ip
// （连接器视角）分域维护：事件改写真值、修复改写连接器视角，Trace 对端与真值对齐。
func TestV3EndpointIPSemantics(t *testing.T) {
	t.Run("incident-v3-d02 真值与连接器视角分离且最终一致", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-validation", "incident-v3-d02")
		incidentID := item.Scenario.Metadata.ID
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		ctx := context.Background()
		require.NoError(t, sim.Advance(ctx, 5*time.Minute))
		snapshot := sim.Snapshot()
		require.Equal(t, "198.51.100.77", snapshot.Providers["provider-tr-a"].EndpointIP)
		require.Equal(t, "198.51.100.10", snapshot.Connections["provider-tr-a"].ResolvedIP,
			"事件只改 Provider 真值，连接器解析仍为旧值")

		refresh := v3Remediation(t, sim, incidentID, "v3-ip-d02", "refresh_provider_connection",
			map[string]any{"provider_id": "provider-tr-a", "expected_pool_generation": 8})
		require.Equal(t, platform.OperationPending, refresh.Status)
		require.NoError(t, sim.Advance(ctx, time.Minute))
		snapshot = sim.Snapshot()
		require.Equal(t, "198.51.100.77", snapshot.Connections["provider-tr-a"].ResolvedIP,
			"refresh 修复改写连接器视角至当前真值")

		require.NoError(t, sim.Advance(ctx, 11*time.Minute)) // 跨过 12m 二次 rehome
		snapshot = sim.Snapshot()
		require.Equal(t, "198.51.100.99", snapshot.Providers["provider-tr-a"].EndpointIP)
		require.Equal(t, "198.51.100.77", snapshot.Connections["provider-tr-a"].ResolvedIP)

		// recreate 前置条件要求先有一次 hard_stop 探测（世界仍故障，如实失败）。
		probe := v3Probe(t, sim, incidentID, "route-a", "default-safe-recovery", "v3-ip-d02-p")
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "hard_stop", v3ProbeOutcome(t, sim, incidentID, probe.ID))

		recreate := v3Remediation(t, sim, incidentID, "v3-ip-d02-2", "recreate_provider_connection_pool",
			map[string]any{"provider_id": "provider-tr-a", "expected_pool_generation": 9})
		require.Equal(t, platform.OperationPending, recreate.Status)
		require.NoError(t, sim.Advance(ctx, 2*time.Minute))
		require.Equal(t, "198.51.100.99", sim.Snapshot().Connections["provider-tr-a"].ResolvedIP)
	})

	t.Run("incident-v3-h08 地址无罪：peer==endpoint_ip，无过时派生", func(t *testing.T) {
		item := loadV3DatasetCase(t, "toolops-v3-test", "incident-v3-h08")
		sim, err := New(item.Scenario)
		require.NoError(t, err)
		require.NoError(t, sim.Advance(context.Background(), 5*time.Minute))
		snapshot := sim.Snapshot()
		provider := snapshot.Providers["provider-plag-a"]
		require.Equal(t, "192.0.2.60", provider.EndpointIP)
		require.Equal(t, provider.EndpointIP, snapshot.Connections["provider-plag-a"].ResolvedIP)
		for _, trace := range snapshot.Traces {
			if peer, _ := trace.Attributes["peer_address"].(string); peer != "" {
				require.Equal(t, provider.EndpointIP, peer, "Trace 对端必须与 Provider 真值一致")
			}
		}
	})
}

// TestV3TitlesDoNotLeakRootCause test 场景标题不得包含根因/修复词（防泄漏静态检查）。
func TestV3TitlesDoNotLeakRootCause(t *testing.T) {
	leakWords := []string{"stale", "rollback", "rebuild", "recreate", "rotate", "cache", "quota", "credential",
		"mirror", "encoder", "failover", "corrupt", "refresh", "purge", "reload", "冲突", "回滚", "重建", "轮换", "缓存", "配额", "凭据"}
	for _, version := range []string{"toolops-v3-validation", "toolops-v3-test"} {
		for _, item := range loadV3Dataset(t, version).Cases {
			title := strings.ToLower(item.Scenario.Metadata.Title)
			for _, word := range leakWords {
				require.NotContains(t, title, word, "%s 标题泄漏根因/修复词 %q", item.Scenario.Metadata.ID, word)
			}
		}
	}
}
