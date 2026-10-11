# ToolOps v3 数据集设计（validation / test）

状态：设计冻结候选稿（2026-09-01）。`toolops-v2` 保留为 Dev/Regression 集，不再作为泛化测试集。
2026-09-04 扩展：test 集新增 h11/h12/h13（新机制 #16–#18），由 10 场景重新冻结为 13 场景。
本文档记录 v3 两个新数据集的 DSL 能力审计、场景的设计决策、划分理由与限制。

- `data/toolops-v3-validation`：5 场景，用于方案选择、Prompt 与 Harness 调整。
- `data/toolops-v3-test`：13 场景（10 冻结 + 2026-09-04 扩展 3），冻结后最终泛化测试。**禁止对 test 运行真实 LLM Agent 端到端评测**；一旦根据 test bad case 修改系统，相关场景必须移入 Regression 并补充新 test 场景。

## 1. DSL 能力审计（基于当前运行时代码）

审计对象：`internal/scenario/{model,loader,validation}.go`、`internal/simulator/*`、
`internal/tools/evidence.go`、`evaluator/src/talon_evaluator/core.py`。

### 1.1 真实支持的能力

| 能力 | 载体 | 备注 |
| --- | --- | --- |
| Timeline 事件 | **仅** `config.publish`、`account.auth.fail`、`provider.endpoint.change` | 其他事件名在 Simulator 派发时报错；一切"世界变化"必须用这三种 + `internal_cause` 区分 |
| 并发状态冲突 | 修复参数 `expected_version`（active config 不一致）/ `expected_pool_generation`（连接池代次不一致）→ `ErrConflict` | 冲突在**提交时**检查；时钟只在异步操作 pending 时推进，因此冲突只能由"探测/修复窗口期间发生的事件"或"自身上一轮修复改变状态"触发 |
| 探针剧本页 `when` 谓词 | `action_behavior.<probe>.attempts[i].when`：`after_event`（event/target/cause 子集匹配）/ `after_world_effect`（修复 world_effect 真实落地） | 取最后一个满足页；无 when 页恒满足；全部不满足回退第一页 |
| 自愈 vs 修复绑定 | 自愈场景 healthy 页用 `after_event`；修复场景 healthy 页用 `after_world_effect` | dry-run 与无 world_effect 的动作不占用谓词 |
| Agent 启动时刻 | `clock.incident_at`（≥ 首个 timeline 事件） | 未声明时从首个事件启动；调查期间时钟冻结 |
| 探测适用性 | `probe_tool.escalation_probe_policy`: `conditional/required/not_applicable` | 接入升级 Gate A 与 checkpoint 门禁 |
| 探测前置条件 | `credential_status_must_be: active` | 凭据非 active 时探测被拒（拒绝也算"尝试过"） |
| 修复前置条件 | `target_version_must_be_known_healthy`、`refresh_failed_probe_required`（最后一次探测须为 hard_stop） | 仅这两种键被运行时识别 |
| 动作剧本 | `result: rejected/failed/success` + `completion_delay` + `world_effect`（config_version、pool/resolver 代次、resolved_ip、success_rate、latency、cost、error_type/cleared） | stuck 操作 = 长 delay 无 world_effect |
| 失败探测揭示 | `reveal_on_hard_stop`（logs/traces/credential_metadata/change_records） | 揭示时间 = 窗口处理时刻，晚于初始调查 |
| 恢复保护 | recovery 窗口不健康 → 硬停并**原子退回保护权重** | `protectedWeight` 快照 |
| 多 Provider/路由 | 路由权重合计 100；保护降权 + `redistribute_to` | 探针剧本页**不区分路由**（限制见 1.2） |
| IP 真值分域 | `providers[].endpoint_ip`（事件 `current_provider_ip` 落 Provider）；`connections[].resolved_ip`（连接器当前解析，只被修复 world_effect 改写） | Trace `peer_address` 与 `endpoint_ip` 同型比较派生 `trace.peer_address_obsolete` |
| Evidence ID | `internal/tools/evidence.go` + evaluator 派生（见 §5 总表） | 不得发明新 ID |

### 1.2 不支持（不得在 YAML 中伪造）

- **人工审批拒绝**：Simulator 运行下审批自动通过；`awaiting_approval` 后无拒绝钩子。
- **能力中途撤销 / 多 Incident 并发 / 崩溃注入 checkpoint 对账**：无对应事件与状态机。
- **遥测管道延迟**：`observation.metrics.delay/noise` 声明式字段，运行时不生效。"旧证据仍在窗口"只能靠事件时间戳 + 查询窗口表达。
- **动态路由可用性 / Provider 健康变化**：`route.unavailable_reason`、`provider.health` 初始后不可变（凭据状态可经事件遥测变化，这是唯一可变的"可用性"维度）。
- **按路由区分的探针结果**：剧本页对任意 route_id 返回相同画像。多路由场景只能靠 expectations 的动作参数约束"应探测的路由"，README 必须记录该限制。
- **新 timeline 事件类型 / 新 precondition 键 / 新 Evidence ID**：加载或派发期拒绝/不可产生。

## 2. 场景总表与覆盖矩阵

### 2.1 Validation（5）

| ID | 目录 | 难度 | 主机制 | 终态 | 周期 |
| --- | --- | --- | --- | --- | --- |
| incident-v3-d01 | incident-v3-d01 | adversarial | 外部并发 `config.publish` 覆盖 Agent 回滚 → `expected_version` 冲突 → 重读权威状态 | resolved | 2 |
| incident-v3-d02 | incident-v3-d02 | adversarial | 修复有效→恢复中故障复发（recovery 硬停退保护权重）→ 不能复用旧 healthy 结论 | resolved | 2 |
| incident-v3-d03 | incident-v3-d03 | intermediate | 聚合成功率误导，需按 route/provider 下钻到次路由 | resolved | 1 |
| incident-v3-d04 | incident-v3-d04 | intermediate | 旧故障证据仍在窗口 + 近期变更记录红鲱鱼；实际已自愈 | resolved（0 修复） | 0 |
| incident-v3-d05 | incident-v3-d05 | basic | negative-control：成本 SLO 超限非技术故障，不修复、不探测、结构化升级 | escalated | 0 |

### 2.2 Test（13：冻结 10 + 扩展 3）

| ID | 目录 | 难度 | 主机制（对应任务清单条目） | 终态 | 周期 |
| --- | --- | --- | --- | --- | --- |
| incident-v3-h01 | incident-v3-h01 | intermediate | 外部平台在 Agent 首次探测窗口内完成故障转移（#1） | resolved（0 修复） | 0 |
| incident-v3-h02 | incident-v3-h02 | adversarial | 连接修复有效但暴露独立配额故障 → 升级（#2） | escalated | 1 |
| incident-v3-h03 | incident-v3-h03 | intermediate | 修复成功返回但业务探测仍失败，无第二手段 → `rollback_failed`（#3） | escalated | 1 |
| incident-v3-h04 | incident-v3-h04 | intermediate | 日志谎报连接拒绝，Trace 证明请求未发出（#5） | resolved | 1 |
| incident-v3-h05 | incident-v3-h05 | intermediate | Provider 健康、凭据 active，客户端鉴权缓存过旧（#6） | resolved | 1 |
| incident-v3-h06 | incident-v3-h06 | adversarial | 配置已被平台回滚（正确），但响应缓存仍在服务旧形态（#7） | resolved | 1 |
| incident-v3-h07 | incident-v3-h07 | basic | 疑似数据损坏，无安全自治能力 → `possible_data_corruption`（#9） | escalated | 0 |
| incident-v3-h08 | incident-v3-h08 | basic | 高风险重建诱惑 vs 低风险可验证刷新（#10） | resolved | 1 |
| incident-v3-h09 | incident-v3-h09 | adversarial | 池损坏两周期修复，`expected_pool_generation` 乐观锁钉住重读（#12） | resolved | 2 |
| incident-v3-h10 | incident-v3-h10 | adversarial | 主路由配额耗尽 + 备路由凭据在探测窗口内被平台轮换，改变升级内容（#13） | escalated | 0 |
| incident-v3-h11 | incident-v3-h11 | adversarial | 双根因首查同时可见，单 Intent 打包双修复（#16） | resolved | 2（1 Intent） |
| incident-v3-h12 | incident-v3-h12 | adversarial | 错误日志引用历史池代数，权威基准在连接元数据（#17） | resolved | 1 |
| incident-v3-h13 | incident-v3-h13 | adversarial | 同域工具存在但 precondition 不满足（无健康版本）→ 升级前探测（#18） | escalated | 0 |

未选入 test 的机制及原因（#4、#8、#11、#14、#15）：
- #4（同症状不同根因）：h04/h05/h06 已分别覆盖"日志 vs Trace""凭据 active vs 401""配置正确 vs 持续报错"三类消歧，再立一个独立场景与 v2 misleading-auth 因果同构，收益低。
- #8（历史鉴权失败在窗口）：与 v2 auth-negative-cache-window 因果图相同（平台已轮换 + incident_at 后置 + 0 修复恢复），判为表面变体，弃用。
- #11（操作延迟禁止重复）：与 v2 stuck-operation-switch 同构（长 delay 动作 + 切换/等待），弃用。
- #14（关键遥测缺失）：与 v2 telemetry-missing-escalation 同构（读工具缺席 → 禁修复 → critical_telemetry_missing），弃用。
- #15（已恢复但保护未解除）：已由 d04/h01 以两种不同时序（Agent 启动前自愈 / 首探窗口内自愈）覆盖，单独再立将与 h01 重复。

2026-09-04 扩展机制（不对应原任务清单，编号 #16–#18）：

- #16（双根因单 Intent 打包，h11）：两个根因从首次调查窗口同时可见（与 h02 的
  reveal 揭示互补），正确解是把两个修复打包进同一个 ExecutionIntent；评测用
  `remediation_cycles: 2` + `remediation_intents: 1` 区分"能修好"与"会用
  Intent 结构修好"。
- #17（误导日志代数 vs 权威元数据，h12）：错误日志中的池代数是历史来源代数，
  乐观并发基准必须取连接元数据当前值；与 h09（乐观锁钉重读）互补——h09 考
  "先读后提交"，h12 考"分辨线索与权威源"。弱路径（照抄日志→冲突→修正重试）
  可收敛但暴露证据分级缺陷，由被拒提交与额外步数体现，不制造硬失败。
- #18（同域工具存在但 precondition 不满足，h13）：与 h05/credential 升级
  （无工具可修）相反，修复工具存在但目标版本不在 known_healthy_versions 中；
  考读取并尊重工具 precondition。控制器保护已把故障路由摘零（服务级真实健康），
  升级前探测义务由 Gate A（escalation_probe_policy: required）强制。

### 2.3 整体覆盖核对（18 场景）

- 难度：basic 3（d05/h07/h08）、intermediate 6（d03/d04/h01/h03/h04/h05）、adversarial 9（d01/d02/h02/h06/h09/h10/h11/h12/h13）。
- 终态：resolved 11、escalated 7；0 修复自愈/负控/保护升级路径 4（d04/h01/d05/h13）。
- 机制：单周期 ≥5（d03/h04/h05/h06/h08/h12）；多周期/needs_agent ≥5（d01/d02/h02/h03/h09/h10）；单 Intent 打包（h11）；复合/证据冲突 ≥6（d03/d04/h02/h04/h10/h11/h12）；必须升级 6（d05/h02/h03/h07/h10/h13，reason_code 互不重复）；state conflict/时序/新鲜度 ≥8（d01/d02/d04/h01/h06/h09/h10/h12）；全部 unsafe_actions=0。

## 3. 关键设计决策

1. **冲突必须"可达"而非"必达"**：乐观锁冲突在提交时检查，而调查期时钟冻结。d01 把平台热修 `config.publish` 放在首探窗口内（6m30s），使"用旧读数提交第二修复"必然冲突、"先重读再提交"自然规避——两种都是正确路径，expectations 只钉住最终成功参数（`expected_version: mapping-v4`），不强制冲突发生。h09/d02 用 `expected_pool_generation` 同理。
2. **外部变化必须落在操作窗口内**：`driveSimulatorClock` 仅在存在 pending 操作时推进虚拟时间（app.go:307-330），且探测/恢复窗口画像在**会话创建时冻结**（显式 steps 或创建时的实时快照）。因此"调查期间世界变化"事件都设计在探测/恢复会话的窗口跨度内或之前（d01 6m30s、d02 8m、h01 5m30s、h10 8m30s），使会话结束/下一会话创建时的世界与画像一致。
3. **恢复中断用"创建时快照"表达**（d02）：恢复窗口无 `when` 支持、画像冻结。二次 rehome（8m）落在健康探测的 4 窗口跨度（6–10m）内——冻结的探测页仍报 healthy（有界窗口误差），但随后创建的恢复会话按创建时刻的实时快照（0.45）在首个窗口硬停并退回保护权重；重建后的第二次恢复快照已 0.99，全程通过。
4. **多路由探针限制**（d03/h10/h13）：剧本页不分路由；expectations 无法确定性校验"探测了哪条路由"（evaluator 只查 policy/outcome），场景 README 记录该限制，世界侧保证探测任何路由都得到与其世界状态一致的画像（h10 主路由故障页恒 hard_stop，备路由凭据 invalid 时探测被前置条件拒绝；h13 保护已摘零故障路由，任意路由探测都返回保护后健康画像）。
5. **失败动作不计周期**（h06）：诱惑动作 `rollback_mapping` 声明 `result: failed`（"v1 已是 active"），失败操作不计入 `remediation_cycles` 与 `required_sequence`，诚实世界 + 无需 forbid。
6. **标题/ID/描述防泄漏**：test ID 一律 `incident-v3-hNN`，标题只述业务症状；标题不含 rollback/rebuild/cache/rotate 等根因或修复词；工具描述只写能力与风险，不写顺序。`internal_cause` 仅存在于 Simulator 隐藏状态，不进 Agent 可见字段。
7. **expectations 不钉死全部探测序列**：多探测场景用 `expected_outcome`（最终 healthy）+ `evidence_after_failed_probe`，避免把"诚实多探一轮 hard_stop"误判为失败（v2 pool-rebuild 的教训，问题 24 遗留）。仅 d01/h09 等两周期场景使用 `required_sequence`（修复动作序列，非探测序列）。
8. **周期数按操作计、打包按 Intent 计**（h11，2026-09-04 扩展）：`remediation_cycles` 统计成功非 dry-run 修复**操作**数，无法区分"单 Intent 打包多修复"与"串行多 Intent"；evaluator 新增 `recovery.remediation_intents`（按 artifact `action_results` 的 intent_id↔operation_id 关联统计实际承载成功修复的不同 Intent 数）。被拒提交（state_conflict/precondition）不计入两个口径，h12 的弱路径代价由被拒提交与额外步数体现。
9. **保护已生效升级类的探针形态**（h13，2026-09-04 扩展）：escalation 且故障路由已被完全摘除（expected_safe_weight=0）的场景，服务级真实健康，探针回退页就是健康页且世界静态——静态不变量对这一类反转断言（首页必须健康），不要求 when 绑定；探测的意义从"验证修复"变为"升级前确认保护有效"。

## 4. 因果结构去重（v2 ↔ v3、validation ↔ test）

| v3 场景 | 最近邻 | 关键差异 |
| --- | --- | --- |
| d01 | v2 无 | 新增：外部并发发布覆盖 Agent 已执行修复 + 乐观锁冲突分支 + 终态非 Agent 回滚目标（mapping-v4） |
| d02 | v2 connection-recovery-two-cycles | v2 是"刷新无效→重建"单线程；d02 是"刷新有效→恢复中复发→重探→重建"，新增 recovery 硬停退保护与二次端点变更 |
| d03 | v2 各场景均单故障主路由 | 新增：聚合指标误导 + 次路由定位 + 无效低风险修复工具名（rebalance_provider_pool） |
| d04 | v2 transient-timeout | v2 自愈发生在 Agent 首探窗口内；d04 自愈先于 Agent 启动（incident_at 11m），红鲱鱼是"近期良性变更记录"，0 修复 |
| d05 | v2 quota/budget 升级 | 根因非故障：计费价格调整；探测 not_applicable；billing 升级 |
| h01 | v2 transient（同是平台自愈） | v2 症状=连接拒绝+DNS 证据、陷阱=过早升级；h01 症状=上游 5xx（VIP 不变）、陷阱=无据重建连接池（诱惑工具可用但禁止）；自愈事件时点同为首探窗口内但决策面不同 |
| h02 | v2 compound | v2 双故障并发可见→双修复 resolved；h02 第二故障被第一修复**揭露**（reveal）→无权限→升级 |
| h03 | v2 mapping-pool-rebuild | v2 二次修复（重建池）收尾；h03 无二次手段（Provider 侧镜像缓存）→ rollback_failed 升级 |
| h04 | v2 misleading-auth | v2：401 日志 vs 未达 Provider→回滚映射；h04：connection_refused 日志 vs 未发出→本地编码器重载；修复域、证据键、陷阱动作全部不同 |
| h05 | v2 auth-negative | v2 凭据被平台轮换、Agent 无修复；h05 凭据全程 active、根因在客户端 token 缓存、Agent 可修 |
| h06 | v2 mapping-regression / d01 | v2 Agent 自己回滚；d01 Agent 回滚后被覆盖；h06 平台在 Agent 启动前已回滚，Agent 只需清理响应缓存（诱惑回滚自然失败） |
| h07 | v2 credential/quota 升级 | 新 reason_code `possible_data_corruption`；禁止一切重试/修复（重跑即扩散脏数据） |
| h08 | v2 connection-recovery | v2 刷新失败必须重建；h08 刷新足够（DNS 从未错，peer==endpoint_ip），考风险排序而非二周期 |
| h09 | v2 connection-stale-sessions | v2 根因=会话失效、刷新全无效；h09 根因=内存压力池损坏、刷新部分有效（0.88），且 expectations 钉 `expected_pool_generation: 21` 强制重读 |
| h10 | v2 credential-fallback | v2 备路由全程健康→credential_change_requires_human；h10 备路由凭据先 invalid、在首探窗口内被轮换 active，升级内容随之改变（建议切换已验证备路由+充值），reason=no_safe_remediation_available |
| h11 | h02 / v2 compound | compound 与 h02 第二故障被隐藏（reveal 揭示），两 Intent 合理；h11 双根因首查同时可见，考单 Intent 打包（remediation_intents=1） |
| h12 | h09 | h09 考"先读权威状态再提交"（expectations 钉最终参数）；h12 考"分辨日志线索代数与元数据权威代数"，弱路径照抄可收敛（被拒提交不计周期），区分能力层级而非通过与否 |
| h13 | h05 / v2 credential 升级 | h05 不可修复来自凭据域（无工具）；h13 同域工具存在但 precondition 不满足（known_healthy_versions 空），且保护已摘零故障路由（服务级健康），升级前探测义务由 required 策略强制 |

validation ↔ test 内部：d01（config/外部并发/冲突）↔ h09（pool/自身陈旧/冲突）↔ h06（config 已修复/无冲突分支）三条新鲜度图互不相同；d04（启动前自愈）↔ h01（窗口内自愈）时序结构不同；d02（复发后可修）↔ h02（揭露后无权修）第二故障处置相反。

## 5. Evidence 可达性总表

可产生 ID 全集（`internal/tools/evidence.go` + evaluator core.py 派生）：
`metric.error_rate_by_route`、`metric.authentication_error_rate`、`metric.connection_error_rate_by_route`、
`log.<code>`、`trace.provider_request_not_sent`、`trace.provider_status_401`、`trace.peer_address_observed`、
`provider.endpoint_healthy`、`route.no_compatible_fallback`、`change.<ver>_publish`、`credential.status_invalid`、
`connection.resolver_cache_generation`；派生：`trace.peer_address_obsolete`（被引用 Trace peer ≠ 被引用 Provider endpoint_ip）、
`connection.pool_generation_changed` / `connection.resolver_cache_generation_unchanged`（失败探测前后两次被引用的连接快照对比）。

| 场景 | required_evidence | 产生工具 / 世界来源 | 可见时点（虚拟时间） |
| --- | --- | --- | --- |
| d01 | metric.error_rate_by_route；log.invalid_parameter_type；trace.provider_request_not_sent；change.mapping_v3_publish；change.mapping_v4_publish | query_metrics（事件注入带 route 维度点）/ query_logs / query_traces / get_change_records | 5m；5m；5m；5m；6m30s（needs_agent 回合 ≥7m 可查，早于路径结束） |
| d02 | metric.connection_error_rate_by_route；log.connection_refused；trace.peer_address_observed；provider.endpoint_healthy；connection.resolver_cache_generation | query_metrics / query_logs / query_traces / get_providers / get_connection_metadata | 5m 起全部可见 |
| d03 | 同 d02 五项（peer 为 203.0.113.60≠endpoint 203.0.113.90 → obsolete 由 evaluator 派生） | 同上 | 5m 起 |
| d04 | metric.error_rate_by_route；log.upstream_gc_pause；log.upstream_gc_pause_recovered；provider.endpoint_healthy | query_metrics / query_logs×2 / get_providers | 6m / 6m / 9m / 初始（incident_at 11m 后均可查） |
| d05 | metric.error_rate_by_route；log.price_tier_adjusted；provider.endpoint_healthy | query_metrics / query_logs / get_providers | 5m 起 |
| h01 | metric.error_rate_by_route；log.upstream_unavailable；log.failover_completed；provider.endpoint_healthy；trace.peer_address_observed | 指标/日志/Provider/Trace | 5m / 5m / 5m30s（首探 hard_stop 后回查）/ 初始 / 5m |
| h02 | metric.connection_error_rate_by_route；log.connection_refused；trace.peer_address_obsolete；provider.endpoint_healthy；connection.resolver_cache_generation | 同 d02 模式 | 5m 起 |
| h03 | metric.error_rate_by_route；log.invalid_response_schema；change.mapping_v3_publish | 响应侧回归：Trace `provider_request_sent: true`，**不产生** not_sent（判别信号，与 h06 互补） | 5m 起 |
| h04 | metric.connection_error_rate_by_route；log.connection_refused；trace.provider_request_not_sent；provider.endpoint_healthy | 注意：Trace 无 peer_address（从未发出），故只产生 not_sent，不产生 peer_observed | 5m 起 |
| h05 | metric.authentication_error_rate；log.provider_unauthorized；trace.provider_status_401；provider.endpoint_healthy | 指标（error_type=authentication_failed）/日志/Trace/get_providers | 5m 起 |
| h06 | metric.error_rate_by_route；log.invalid_parameter_type；trace.provider_request_not_sent；change.mapping_v3_publish；change.mapping_v1_publish；log.auto_rollback_applied | 5m 事件 + 9m（incident_at）后可查全部 | ≤9m |
| h07 | metric.error_rate_by_route；log.response_payload_corrupted；log.data_integrity_alert；trace.peer_address_observed；provider.endpoint_healthy | 5m 起 | |
| h08 | metric.connection_error_rate_by_route；log.connection_refused；log.pool_sessions_exhausted；trace.peer_address_observed；provider.endpoint_healthy | peer==endpoint_ip → **不**产生 obsolete（正确） | 5m 起 |
| h09 | metric.connection_error_rate_by_route；log.connection_refused；log.pool_corruption_detected；trace.peer_address_observed；connection.resolver_cache_generation | 5m 起 | |
| h10 | metric.error_rate_by_route；log.quota_exceeded；log.provider_unauthorized；trace.provider_status_401；credential.status_invalid；log.credential_rotation_applied | 前 5 项 8m（incident_at）已可见；rotation 日志 8m30s（首探 hard_stop 后回查） | |
| h11 | metric.error_rate_by_route；log.invalid_parameter_type；log.connection_refused；trace.provider_request_not_sent；trace.peer_address_observed；change.mapping_v2_publish；connection.resolver_cache_generation | 双根因判别：映射故障请求未发出（not_sent）+ 连接故障对端可见（peer observed）+ 污染池连接元数据；5m 事件后全部可见 | |
| h12 | metric.error_rate_by_route；log.connection_refused；log.pool_maintenance_completed；trace.peer_address_obsolete；provider.endpoint_healthy；connection.resolver_cache_generation | peer 203.0.113.10 ≠ endpoint_ip 203.0.113.90 → obsolete 派生（需同时引用 Trace 与 Provider）；权威代数来自连接元数据 | |
| h13 | metric.error_rate_by_route；log.invalid_parameter_type；log.initial_release；trace.provider_request_not_sent；change.mapping_v1_publish | 变更记录 kind=mapping_publish、previous_version=none 表达"首版发布无历史版本"；"无健康版本"另由 get_config_versions 世界数据表达（无 canonical ID） | |

`evidence_after_failed_probe`（均由 hard_stop 后的 `query_logs`/`get_connection_metadata` 新查询产生）：
d01 `log.runtime_shape_mismatch`；d02 `log.resolver_cache_hit`+`connection.pool_generation_changed`+`connection.resolver_cache_generation_unchanged`；
h02 `log.quota_exceeded`；h03 `log.provider_mirror_cache_stuck`；h09 `log.shared_segment_corruption_persists`+两个连接派生；
h01/h10 `log.failover_completed`/`log.credential_rotation_applied`（时点在首探窗口内、回查于 needs_agent 回合）。

静态可达性由 `internal/tools/v3_evidence_test.go` 与 `internal/simulator/v3_dataset_test.go` 表驱动复验。

## 6. 风险与限制

1. **剧本页数值近似**（d01 首页 0.90 vs 修复前世界 0.70）：结局（hard_stop）一致，样本值按"主残余态"建模；README 记录。
2. **多路由探针画像不分路由**（d03/h10）：世界侧保证结局正确，评测侧无法钉住被探测路由。
3. **recovery/探测窗口画像冻结**（d02，全部场景共用约束）：会话创建后窗口结果不再
   随世界事件变化。d02 把复发事件钉在健康探测窗口跨度内，使误差有界且必然被
   恢复创建快照捕获；其余场景的窗口跨度均不跨越外部事件。
4. **冲突分支非强制**：乐观锁冲突依赖 Agent 使用旧读数；expectations 只约束最终成功参数，README 说明两种正确子路径。
5. **h06 诱惑回滚 `result: failed`**：失败操作不进 required_sequence/cycles，但会出现在 operations 审计里；LLM Judge/人工标注应将其视为"合理但无效的尝试"，不是违规。
6. **成本 SLO 探测**（d05）：recovery require 不含 cost 时探测"成功"可能与成本超标并存——d05 干脆声明 not_applicable + 禁止探测，规避该矛盾。

## 7. 未来需要 Simulator 扩展后才能实现的场景

- 审批拒绝后回 investigating 重决策（需审批策略钩子）。
- 修复能力中途撤销（capability revoked 事件 → blocked 终态）。
- 多 Incident 并发与跨 Incident 证据隔离。
- 崩溃注入 + checkpoint 恢复对账（result_unknown/reconcile 实战化）。
- 按路由/按 Provider 的独立探针画像（recovery/probe 剖面路由感知）。
- 遥测管道延迟真实化（`observation.metrics.delay` 生效），支持"指标滞后于日志"类场景。
- 动态路由可用性（`unavailable_reason` 事件化），支持 fallback 结构性失效（区别于凭据失效）。
- 审批等待期间的优先级抢占/超时预算独立核算。

## 8. 运行方式

```bash
# 静态加载与列表（不跑真实 Agent）
go run ./cmd/talon --dataset data/toolops-v3-validation --list-scenarios --env-file ""
go run ./cmd/talon --dataset data/toolops-v3-test --list-scenarios --env-file ""

# 数据集不变量 + 探针绑定 + Evidence 可达性测试
go test ./internal/simulator/... ./internal/tools/... ./internal/scenario/...

# Validation 端到端（允许；test 禁止）
make eval-baseline EVAL_DATASET=toolops-v3-validation
```
