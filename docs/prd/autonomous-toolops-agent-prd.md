# Talon Autonomous ToolOps Agent 需求文档

> 版本：v1.2（职责边界修订）
> 状态：Draft
> 日期：2026-08-10

## 1. 我们要做什么

Talon 是一个长期运行的 ToolOps Agent。它面向 MCP 工具平台和 Agent 服务平台，在确定性控制器完成异常检测和流量保护后接管事件，负责收集证据、定位根因、选择受控修复动作、发起小流量探测，并在修复有效后申请恢复流量。Talon 不直接计算或修改路由权重，权重的降低和逐步恢复始终由控制器执行。

它的完整工作闭环是：

```text
控制器检测异常并降权 → 启动 Agent → Agent 收集只读证据
    → 判断根因 → 选择受控修复函数 → 发起小流量探测
    → 恢复策略校验 → 控制器逐级回权
    → 恢复成功 / 停止探测并回退 / 人工接管
```

AgentEarth 是第一个计划接入的平台，但 Talon 不能成为 AgentEarth 的附属模块。Talon 核心必须保持平台无关，通过标准 Adapter 接入外部平台，并在没有 AgentEarth 源码、数据库和运行实例时独立完成所有核心测试。

## 2. 为什么值得做

MCP 工具平台依赖大量外部 Provider。Provider 会超时、限流、修改接口、耗尽额度或出现区域性故障；同一能力的多个 Provider 在成功率、延迟和价格上也会持续变化。任务链配置、参数 Mapping 和异步任务驱动同样可能发生回归。

现有监控可以告诉运维人员“某个指标出问题了”，但从告警到恢复仍需要人工关联日志、判断根因、寻找替代路由、修改权重、观察效果并决定是否回滚。Talon 的业务价值是缩短这个闭环，直接改善工具调用成功率、MTTD、MTTR 和单次成功调用成本。

这个方向成立有一个必要前提：目标平台必须存在真实调用量、真实故障和可替代路由。如果没有这些条件，Talon 就没有需要负责的业务结果。项目第一阶段必须先验证这些事实，不能用合成 Demo 代替业务价值。

## 3. 产品边界

Talon 负责“故障根因诊断、受控修复和恢复验证”，不负责异常检测后的即时降权，也不负责成为通用聊天助手或自动修改平台源代码。生产环境中不得向模型提供任意 Shell、SQL 或文件编辑工具，Talon 也不得绕过控制面直接写目标平台数据库。

Talon 的输出不是一份文档，而是经过审计的系统状态变化。每次写动作都必须具备明确权限、影响范围、幂等键、验证条件和回滚方式。故障报告只是整个闭环留下的审计结果。

首版重点解决四类根因：

| 场景 | Talon 的目标动作 |
|---|---|
| Provider 成功率或延迟退化 | 判断是 Provider 自身、网络、账号还是请求分布导致，执行允许的修复后发起探测 |
| Provider、账号或连接不可用 | 修复连接、额度或凭证配置；无法安全处理时升级人工 |
| Mapping / Task Chain 配置回归 | 关联变更时间，调用配置回滚函数并验证业务指标 |
| 异步任务卡住 | 判断驱动是否丢失，在满足幂等条件时补发或重试 |

控制器和 Agent 之间通过事件接口协作。控制器在错误率或延迟连续违反规则、路由被降至干预阈值、熔断器打开或同一路由反复抖动时创建或更新 Incident，并调用 `start_agent(incident_id)`。触发规则、窗口、最小样本量、降权幅度和事件去重均由确定性代码负责。

## 4. 一个完整事件如何运行

以 Provider 错误率退化为例。控制器发现某个工具连续多个窗口违反 SLO，立即按规则降权或熔断，并创建 Incident 启动 Talon。Talon 随后通过只读工具按需查询同时间段的请求量、成功率、错误分布、Trace、连接状态、账号额度、实际路由以及近期发布和配置变更，形成多个候选根因，而不是根据一条日志直接下结论。日志、Trace、指标、配置和变更记录对 Agent 均为只读，并在进入模型前完成脱敏和权限过滤。

如果证据表明近期 Mapping 发布造成参数类型错误，Talon 从预先注册的强类型函数中选择 `rollback_mapping(version)`。修复函数必须声明权限、前置条件、影响范围、dry-run、幂等键、执行结果和补偿方式；Agent 只能选择和组合这些函数，不能生成任意写操作。低风险动作可以在授权范围内自动执行，高风险动作等待人工批准。

修复函数成功不代表事件结束。Talon 调用 `request_probe(incident_id, route_id, policy_id)` 申请小流量探测，由控制器分配受限流量。Agent 根据探测证据判断根因是否消除并申请进入恢复阶段；Recovery Policy 再确定性检查最小样本量、连续健康窗口、成功率、延迟、成本、结果质量、遥测完整性和硬停止条件。通过后，控制器按照服务策略逐级回权；任一阶段恶化都立即停止并回到安全权重。

每次探测失败都必须立即停止该轮探测并回退，不等待累计三次。失败后 Agent 可以根据新证据更换根因假设和修复方案。低风险事件默认最多允许三轮“修复—探测”循环或一个事件时间预算，任一预算耗尽即升级人工；高风险、不可逆、缺少关键遥测、无安全修复函数、疑似安全或数据问题、影响范围持续扩大时则第一次就升级，不能等待三次。

整个过程中，证据、假设、计划、审批、动作前后快照、验证窗口和回滚结果都进入同一条事件时间线。Talon 重启后必须从 checkpoint 恢复，继续承担尚未完成的验证或回滚责任。

## 5. 核心产品能力

| 能力 | 要求 |
|---|---|
| 触发 | 接收控制器创建的 Incident；错误阈值、降权、熔断和去重由控制器负责 |
| 观察 | 通过只读接口查询服务、工具、Provider、路由、日志、Trace、连接、配置、发布和任务状态 |
| 调查 | 按需获取指标切片、错误样本、健康状态、配置差异和替代路由，维护多个带证据的假设 |
| 计划 | 从已注册修复函数中选择动作，生成包含根因、证据、范围、顺序、验证和失败处理的冻结计划 |
| 策略 | 由确定性 Policy Engine 检查权限、自治等级、风险、影响范围、数据完整度和回滚能力 |
| 修复 | 调用强类型函数执行配置回滚、连接修复和幂等任务补偿；不直接操作路由权重 |
| 探测 | 申请控制器分配受限流量，比较修复前基线和探测窗口 |
| 恢复 | Agent 申请恢复，Recovery Policy 校验，控制器执行逐级回权和硬停止 |
| 记忆 | 生成结构化事件经验，经审核后用于检索相似事件；经验不能改变权限和恢复门槛 |
| 人工控制 | 支持审批、拒绝、缩小范围、暂停、接管和全局/环境/服务级 Kill Switch |

事件状态统一为：

```text
protected → investigating → planned
    → awaiting_approval / remediating → probing
    → recovering → resolved / reinvestigating / escalated
```

同一故障域只能存在一个会修改流量的 Active Plan。人工修改与 Talon 动作冲突时，默认人工优先，Talon 停止动作并重新读取状态。

## 6. 自治和安全边界

Talon 的完整产品目标至少是 L2：控制器完成发现和流量保护后，Agent 自己完成诊断、计划、受控修复、探测和恢复申请，人工只负责批准高风险动作。只完成观察和建议不算完整 Agent。

| 等级 | 行为 |
|---|---|
| L0 Observe | 只记录，不生成恢复计划 |
| L1 Advise | 自动调查和计划，动作由人工在外部执行 |
| L2 Approve | 人工批准计划，Talon 负责执行、验证和回滚 |
| L3 Bounded Auto | 经过验证的低风险动作自动执行，其他动作仍需审批 |

首版不支持完全自治。动作按照风险划分：查询和通知自动允许；低频探测和幂等 poll 补发可以逐步开放 L3；配置回滚等高影响修复默认需要审批；路由权重只允许控制器根据策略修改；删除资源、读取或修改密钥、任意 SQL/Shell 永久禁止。

任何写计划如果不能灰度、不能验证或不能回滚，就不得自动执行。Policy Engine、审计或关键遥测不可用时，系统必须停止新的写动作。模型只能提出计划，不能绕过策略直接调用底层控制接口。

## 7. 平台无关架构

Talon 使用自己定义的 `ToolOpsPlatform` 契约与外部系统交互：

```text
Talon Core
  ├── Incident State Machine
  ├── Eino Investigation & Planning Graph
  ├── Policy Engine
  ├── Remediation Tool Registry
  ├── Probe / Recovery Coordinator
  ├── Incident Memory
  └── ToolOpsPlatform Interface
          ├── Simulator Reference Adapter
          └── AgentEarth HTTP Adapter

External Runtime Controller
  ├── Anomaly Detector / Circuit Breaker
  ├── Weight Controller
  └── Recovery Policy Executor
```

核心领域只认识统一模型，例如 Service、Tool、Route、Provider、MetricSeries、ConfigVersion、ManagedTask 和 Action。AgentEarth 的表名、内部结构和特有枚举只能出现在 Adapter 内部。

Eino 用于证据收集、假设生成、调查路径选择、修复函数选择和探测结果解释。异常阈值、降权、权限、状态机、幂等、探测上限、恢复门槛、灰度推进和硬停止条件必须由确定性代码实现。Skill 和 Prompt 可以承载调查方法、领域知识和经验总结格式，但不能承载唯一的生产安全约束。

每次事件关闭后，Talon 生成结构化经验：症状、证据、根因、有效和无效动作、探测结果及适用边界。经验经过脱敏和人工审核后进入案例库，为后续相似事件提供调查排序；未经审核的总结不能自动执行动作，也不能改变 Policy。

生产 ToolOps Agent 只注册受控的只读与操作工具，不注册通用 Coding Agent 的 Terminal 和文件编辑工具。所有平台返回、请求日志和外部 Provider 内容都视为不可信输入，在进入模型前进行脱敏和指令隔离。

## 8. 独立测试与 Simulator

Talon 的单元测试、集成测试、Benchmark 和完整闭环测试不能依赖 AgentEarth 代码，也不能要求启动 AgentEarth。仓库内必须提供一个确定性的 ToolOps Simulator，它实现与真实平台相同的 `ToolOpsPlatform` 契约。

Simulator 不是简单 Mock。它需要同时模拟外部控制器和受管平台，包括异常检测、自动降权、Incident 触发、服务、Provider、路由权重、真实流量、指标窗口、日志、Trace、配置和发布记录、修复函数、探测流量、逐级恢复、动作延迟、幂等、并发冲突、异步任务和遥测故障，并使用虚拟时钟快速推进分钟级验证窗口。

测试场景使用版本化 YAML/JSON 描述：

```yaml
name: provider_latency_degradation
initial_state: two-provider-service
timeline:
  - at: 5m
    event: provider.degrade
    target: provider-a
    latency_p95_ms: 12000
expect:
  controller_action: route.downweight
  root_cause: mapping_schema_regression
  allowed_action: mapping.rollback
  required_probe: true
  slo_recovered: true
  unsafe_actions: 0
```

测试断言的是最终状态和动作轨迹，而不是模型输出的某段文字。核心场景至少覆盖 Provider 5xx、延迟退化、鉴权失败、额度耗尽、配置回归、异步任务卡住、遥测缺失、人工并发修改和 Prompt Injection。

Talon 同时提供 Adapter conformance suite，验证 ID、分页、时间单位、dry-run、幂等、乐观锁、动作状态、配置回滚和错误分类。Reference Adapter 必须通过全部测试。AgentEarth Adapter 可以对已部署的测试环境运行同一套黑盒测试，但 Talon 主仓 CI 不构建、不启动也不读取 AgentEarth。

## 9. AgentEarth 接入要求

AgentEarth 是首个生产 Adapter，需要通过稳定的 Read API 暴露服务、工具、路由、健康、时序指标、配置版本和异步任务，通过 Control API 暴露灰度切流、隔离/恢复、连接池探测、配置回滚和任务 redrive。

所有写接口都必须支持 RBAC、幂等键、expected version、dry-run、动作状态查询和前后快照。Mapping、Task Chain、连接及路由配置需要不可变版本号和原子回滚能力。Talon 禁止通过直连数据库补齐这些能力。

当前累计调用量、累计成功量和单值响应时间不足以支撑诊断，AgentEarth Adapter 还需要获得按时间窗口聚合的调用量、成功率、延迟分位数、错误类型、成本及实际路由分布。

异步任务补偿必须先明确 submit 和 poll 的幂等语义。如果重试可能重复扣费、重复生成内容或产生不可逆副作用，Talon 只能调查和请求人工处理。

## 10. 交付路径

阶段只是风险控制方式，最终目标仍然是完整闭环。

### M0：验证业务和搭建独立测试底座

确认真实调用量、故障类型、人工处理成本以及具有替代 Provider 的服务。同时定义统一领域模型和 `ToolOpsPlatform` 契约，完成 Simulator、虚拟时钟、Reference Adapter 和首批故障场景。

退出条件：完全不依赖 AgentEarth 时，Talon 可以在 Simulator 中读取证据、执行灰度、验证效果并完成回滚。

### M1：事件接入与调查

实现控制器事件接入、事件合并、持久化状态机和 Eino 调查流程，在独立 Scenario 中评测根因 Top-3 命中率和无效工具调用率。该阶段只提供 L0/L1，不作为最终产品发布。

### M2：审批式完整闭环

实现 Plan、Policy、Approval、Remediation、Probe、Recovery 和 checkpoint 恢复，首先支持 Mapping 回滚后的健康探测。接入一个真实平台 Adapter，以 L2 方式运行。

退出条件：Talon 能完成“控制器保护—Agent 诊断—计划—审批—修复—探测—逐步恢复/升级人工”，而不是在修复函数返回成功后结束。

### M3：有界自治

扩展隔离恢复、配置回滚和异步任务补偿。经过离线回放、影子运行、故障注入和生产审批积累后，逐项将成熟低风险动作提升到 L3。

## 11. v1 验收标准

v1 发布时必须同时满足以下条件：

1. 所有核心测试在没有 AgentEarth 源码、数据库和运行实例的环境通过；
2. Simulator 能完成 Provider 5xx、延迟退化、配置回归和异步任务卡住四类闭环；
3. Reference Adapter 和至少一个真实平台 Adapter 通过 conformance suite；
4. 接入至少一个具有两个可替代 Provider 的真实服务；
5. 至少一种低风险修复和探测流程达到 L3，其他生产写动作至少达到 L2；
6. 每个写动作都有 dry-run、幂等、前态快照、验证和回滚；
7. Agent 重启后可以继续未完成的执行、验证或回滚；
8. 未授权动作、敏感信息泄漏和严重扩大故障均为零；
9. 事件具有完整审计时间线，人工可以暂停、接管和触发 Kill Switch；
10. Talon 不位于受管平台在线请求关键路径，并能证明 MTTD、MTTR 得到改善。

建议目标为：控制器事件触发 Precision 不低于 90%、Recall 不低于 85%，Agent 根因 Top-3 命中率不低于 85%，经审批动作执行成功率和可回滚动作回滚成功率不低于 99%。这些数值需要在 M0 获取真实基线后校准。

## 12. 当前必须确认的问题

进入技术设计前，需要先回答四个业务问题：AgentEarth 是否有足够真实调用量和可量化故障；哪些工具存在真正可替代的 Provider；当前人工处理一次故障需要多久；首个接入服务能否提供稳定 SLO、配置版本和安全故障注入环境。

还需要确认四个接口问题：时序指标能否按 service/tool/route/provider/config version 关联；路由权重如何原子发布和回滚；异步任务哪些动作具备业务幂等性；生产审批与通知采用现有管理后台还是外部协作工具。

如果真实业务条件不成立，应停止该方向，而不是继续扩充 Simulator 来制造产品价值。如果条件成立，第一项工程工作应是 `ToolOpsPlatform` 契约和 Simulator，而不是直接修改 AgentEarth 或继续扩展现有 PR Review 流程。
