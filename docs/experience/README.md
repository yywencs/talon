# 诊断经验系统

将历史运行中的诊断方法整理成可复用经验，帮助 Agent 减少重复试错。经验必须经过审核和场景验证才能发布，Agent 使用时仍需核对当前环境的证据。

## 工作流程

```text
运行结束 → 复盘快照 → 候选经验 → 审核与场景验证 → 版本发布
                                                     ↓
当前故障 → 检索相关经验 → 读取摘要或详情 → 核实后制定方案
```

PostgreSQL 保存经验正文、来源和版本，Qdrant 负责混合检索。在线 Agent 可以查询经验，不能直接修改或发布经验库；已发布版本支持回滚。

## 从复盘材料开始

在仓库根目录执行，使用已结束的运行 ID 和对应数据集：

```bash
go run ./cmd/talon-experience snapshot --dataset data/toolops-v2 --run-ids RUN_ID
go run ./cmd/talon-experience list --kind snapshot
```

这一步读取运行记录、进行确定性评测并保存脱敏快照，需要数据库迁移和 Python 评测器，无需 LLM 或 Qdrant。快照只是后续提炼的材料，不会自动发布成经验。

## 继续使用

完整流程需要配置提炼模型、Qdrant、embedding 和 reranker。在线检索通过 `EXPERIENCE_ENABLED=true` 启用。

- [依赖与配置](operations.md#准备依赖)
- [提炼、审核、验证、发布与回滚](operations.md#操作顺序)
- [测试场景与发布标准](operations.md#测试目录与发布标准)
- [Agent 检索接入](operations.md#在线接入)
- [验证命令](operations.md#验证命令)

返回 [项目首页](../../README.md)。
