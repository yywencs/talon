# toolops-v3-validation 数据集

`toolops-v3-validation` 是 ToolOps v3 的**方案选择 / Prompt / Harness 调整集**：允许对其
运行真实 LLM Agent 端到端评测、分析 bad case 并修改系统。`toolops-v2` 降级为
Dev/Regression 集；本数据集冻结后，最终泛化测量只在 `toolops-v3-test` 上进行。

- 版本：`toolops-v3-validation`（dataset.yaml）
- Schema：`toolops-scenario/v1.1` / `toolops-expectation/v1.1`（未新增字段）
- 场景数：5；每场景建议重复 3 次
- 设计文档：`docs/evaluation/toolops-v3-dataset-design.md`

## 场景覆盖矩阵

| # | ID | 难度 | 考察机制 | 终态 | 修复周期 | 探测序列 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | incident-v3-d01 | adversarial | 外部并发 config.publish 覆盖回滚 → expected_version 冲突 → 重读权威状态 | resolved | 2 | hard_stop→healthy |
| 2 | incident-v3-d02 | adversarial | 修复有效→恢复期故障复发（recovery 硬停退保护权重）→ 新证据→ 二次修复 | resolved | 2 | healthy→hard_stop→healthy |
| 3 | incident-v3-d03 | intermediate | 聚合成功率误导 → 按 route/provider 下钻低权重次路由 | resolved | 1 | healthy |
| 4 | incident-v3-d04 | intermediate | 旧错误仍在窗口 + 良性变更记录红鲱鱼 → 判定已自愈，0 修复 | resolved | 0 | healthy |
| 5 | incident-v3-d05 | basic | negative control：成本 SLO 超限（非技术故障）→ 不修复不探测、结构化升级 | escalated | 0 | 无 |

## 与 toolops-v2 的差异

- v2 的世界里 Agent 是唯一写者（除既有故障事件外无运行中外部变更）；d01/d02 引入
  **运行中的第二个写者**（platform-ops 并发发布、端点二次 rehome），考察状态新鲜度。
- v2 探针剧本修复后仅使用 `after_world_effect` 门禁；v3 按"自愈用 after_event、
  修复用 after_world_effect"补齐（d04 自愈页 `after_event`）。
- v2 无聚合指标误导场景（所有故障都落在主路由且指标直接可见）；d03 把故障藏进
  80/20 权重混合的聚合数字后面。
- v2 升级场景全部为技术根因；d05 引入零技术故障的负控（成本/商务根因）。
- expectations 收紧了"不得钉死全部探测序列"的纪律（d02/d03 用最终 outcome，
  避免把诚实多探判负，见问题 24 遗留）。

## 难度分布

basic 1（d05）、intermediate 2（d03/d04）、adversarial 2（d01/d02）。

## 终态分布

resolved 4（d01–d04，其中 d04 为 0 修复自愈路径）、escalated 1（d05）。

## 机制覆盖

- 单周期：d03；多周期/needs_agent：d01/d02；复合/证据冲突：d03/d04；
  必须升级：d05；state conflict/时序/新鲜度：d01/d02/d04；unsafe actions 全 0。

## 数据冻结规则

1. 本数据集允许反复运行与调参；每次系统性修改（Prompt/Harness/门禁）后建议全量
   45-run 复验并记录批次号。
2. 场景 YAML 一旦用于生成对比结论后不得原地修改语义；确需修改时，在场景 README
   追加修订记录并新开评测批次。
3. 若某场景因系统修改而永久失真（例如门禁使正确路径不可达），应移入 Regression
   并从 test 候补新场景。

## test 防泄漏规则（对 validation 的约束）

- validation 场景 ID/标题同样保持中性（incident-v3-dNN + 症状描述），避免养成
  "从标题猜根因"的习惯后被 test 放大。
- expectations 与场景 README 均不进入 Agent 上下文。

## 运行方式

```bash
# 列出场景（静态加载，不初始化模型）
go run ./cmd/talon --dataset data/toolops-v3-validation --list-scenarios --env-file ""

# 数据集不变量与探针绑定测试
go test ./internal/simulator/... ./internal/tools/...

# 端到端基线（允许）
make eval-baseline EVAL_DATASET=toolops-v3-validation
```

## 已知限制

见设计文档 §6 与各场景 README §12。要点：探针剧本页不分路由；恢复窗口无 when
（d02 用实时值弥补）；冲突分支非强制可达；`observation.metrics.delay` 不生效。
