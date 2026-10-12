# 经验系统操作手册

返回 [经验系统概览](README.md)。以下命令均在仓库根目录执行。

本功能采用离线生成、独立审核、隔离场景验证、版本发布、在线只读检索的闭环。
在线 Agent 没有生成、审核、发布或修改评测标准的工具。经验是历史参考，不计入当前故障的证据门禁。

## 模块与数据流

- `internal/experience`：经验卡片、不可变证据快照、固定 Prompt 提炼、独立审核、版本、检索、发布门禁。
- `internal/storage/experience_store.go`：PostgreSQL 正式存储，SQLite 用于隔离测试；不可变对象和带 revision 的 active 指针。
- `internal/experience/qdrant.go`：每个不可变发布版本一个 collection；dense + lexical sparse 召回、RRF 融合。版本独立 collection 同时隔离 IDF 统计，未发布候选不会改变在线排名。
- `internal/experienceeval`：每个对照 trial 独立 Simulator 实例、SQLite 文件和 Agent；调用已有 Python Evaluator。
- `internal/tools/experience.go`：搜索、详情和受限历史来源读取。完整返回值进入原有 ToolCall 审计，调查重入时恢复最近摘要。
- `cmd/talon-experience`：离线操作入口。

PostgreSQL `experience_objects` 用 kind/key 管理 candidate、snapshot、extraction、review、release、embedding、indexed、report、published 和 pointer。除 pointer 外只新增、不覆盖；相同 key 的相同内容可幂等重试。卡片与来源快照、审核和报告都有 SHA-256 摘要。当前版本切换使用 CAS，拒绝基于过期 parent 发布。

一次候选生成可以读取多条成功/失败运行。先用原有确定性 Evaluator 评估；只向模型提供判定与检查编号，不提供 expectations 或场景隐藏状态。提炼通过工具逐条提交，最多 5 张卡片、8 次模型调用；整轮最多允许一次校验修正。证据不足允许返回空数组。输入超过 200KB 应拆成更小的同类批次；不会静默截断关键证据。提炼状态、失败原因、模型响应和工具反馈保存在 extraction 对象中。同一任务 ID 对应追加保存的多个审计快照；查看最终结果时以终态记录为准。

## 准备依赖

1. 先执行 `docs/sql/postgresql/008_experiences.up.sql`。其余迁移仍按原有顺序执行。没有启用经验功能的已有运行不要求新增表。
2. 启动 Qdrant，例如：`docker compose -f docs/experience/compose.yaml up -d`。
3. 配置与现有 LLM 调用兼容的提炼/审核模型，以及独立的 embedding 和 reranker 服务：

```dotenv
EXPERIENCE_ENABLED=true
QDRANT_URL=http://127.0.0.1:6333
QDRANT_API_KEY=
# embedding URL 是包含 /v1 的 base URL，客户端追加 /embeddings
EXPERIENCE_EMBEDDING_URL=http://127.0.0.1:8001/v1
EXPERIENCE_EMBEDDING_MODEL=your-embedding-model
EXPERIENCE_EMBEDDING_DIMENSIONS=1024
EXPERIENCE_EMBEDDING_KEY=
# rerank URL 是 base URL，客户端追加 /rerank
EXPERIENCE_RERANK_URL=http://127.0.0.1:8002/v1
EXPERIENCE_RERANK_MODEL=your-reranker-model
EXPERIENCE_RERANK_KEY=
# 必须根据所选模型的分数分布校准；下面只是配置形式，不是推荐阈值
EXPERIENCE_RERANK_MIN_SCORE=0.5
```

Embedding 协议：POST `{model,input}`，返回 `{data:[{embedding:[...]}]}`。
Reranker 协议：POST `{model,query,documents,top_n}`，返回 `{results:[{index,relevance_score}]}`，按相关性降序。分数低于阈值的结果不返回。

稀疏检索采用版本化的英文标识符分词、中文单字/双字切分、log TF 与 Qdrant IDF；它是 lexical TF-IDF，不伪称为 BM25。无需额外的稀疏模型服务。词哈希存在碰撞可能，错误码必须严格相等时应使用 keyword payload 字段而非依赖语义相似度。本版在线 query 的组件与症状是软排序信息，不会把猜测变成硬过滤。

检索 profile 固定 embedding endpoint/model/dimensions、reranker endpoint/model/threshold 和 lexical 版本。改变 profile 后旧运行拒绝悄悄使用不同检索行为。底层服务也应使用不可变模型部署版本；同名模型替换无法由客户端发现。

## 操作顺序

在仓库根目录执行。生产默认使用 DATABASE_DSN；所有命令可加 `--sqlite-test-db /tmp/experience.db` 做本地验收。

第一步可以单独准备复盘材料，无需配置 LLM、Qdrant、embedding 或 reranker。数据库迁移和 Python Evaluator 仍需就绪：

```sh
go run ./cmd/talon-experience snapshot --dataset data/toolops-v2 --run-ids RUN_ID
go run ./cmd/talon-experience list --kind snapshot
```

输入是已保存的终态运行 ID 和对应数据集。命令读取运行审计，调用确定性评测，将评测摘要、运行来源和脱敏证据保存成不可变快照，并输出 `snapshot_digest` 与快照正文。成功和失败运行都可以处理；运行中任务及数据集版本不匹配会被拒绝。原始审计不会被修改，快照也不会自动成为经验卡片。

相同审计与评测结果重复执行只保留同一份快照；材料变化则生成新的摘要和快照。目前由离线命令显式触发，尚未自动挂接到运行结束事件。后续 `extract` 复用同一准备流程，再调用模型提炼候选。

```sh
# 读取终态审计，使用对应数据集评分，再提炼候选；不会自动发布
 go run ./cmd/talon-experience extract --dataset data/toolops-v2 --run-ids RUN_A,RUN_B
 go run ./cmd/talon-experience list --kind candidate

# 将同一批候选一起审核：检查来源、与 parent 以及同批其他候选的冲突
# 第一版省略 --parent；后续 parent 应为当前 active 版本
 go run ./cmd/talon-experience review --candidates exp_x@1,exp_y@1 --parent v1

# 审核通过才可组装；新版本继承 parent，ID 相同替换，supersedes 可明确淘汰旧卡片
 go run ./cmd/talon-experience stage --release v2 --parent v1 --candidates exp_x@1,exp_y@1
 go run ./cmd/talon-experience index --release v2

# 查看选题和缺口，不调用任务模型
 go run ./cmd/talon-experience plan --release v2 --catalog docs/experience/catalog.example.json

# 运行基线、直接提供经验、真实检索三组隔离 trial
 go run ./cmd/talon-experience validate --release v2 --catalog PATH_TO_REVIEWED_CATALOG --repeats 3 --max-token-ratio 1.25

# 只有匹配 manifest 的 passing report 才能发布
 go run ./cmd/talon-experience publish --release v2 --report REPORT_ID
 go run ./cmd/talon-experience active

# 切换回已发布的旧版本；--parent 用于防止并发覆盖
 go run ./cmd/talon-experience rollback --release v1 --parent v2
```

重试 `index` 可补齐未完成的索引；每条向量先保存到 SQL，重试写入相同 point，不会重新生成不同向量。索引成功标记只在全部写入并核对数量后生成。网络超时不意味着发布成功；`active` 是新任务取版本的依据。已启动任务固定版本，active 回滚不改变旧任务。

合并/修订候选可以先导出 JSON、修改正文和 sources，并用 `import-candidate --file candidate.json` 保存新 ID/版本，再重新 review。禁止在原 key 下覆盖内容；新批次成员变化也必须重新审核。生成和审核使用独立调用与提示词；默认复用部署的 LLM 配置，模型审核不是事实证明，必须经过场景验证。

## 快照到候选经验

读取已经保存的快照进行提炼，不需要重新评测或启动 Qdrant：

```sh
go run ./cmd/talon-experience extract --snapshot-digests DIGEST_A,DIGEST_B
go run ./cmd/talon-experience list --kind candidate
go run ./cmd/talon-experience list --kind extraction
```

`--snapshot-digests` 与 `--run-ids` 互斥；后者仍可用于评测并冻结运行后直接提炼。同一批不能包含同一运行的不同快照，以免证据引用歧义。

默认 Prompt 位于 `prompts/experience-extraction/v2/`，由 `manifest.json`（版本、用途和工具协议）与分节的 `system.md` 组成，随二进制嵌入。可用 `--extraction-prompt-dir PATH` 加载其他版本。修改已发布规则时应复制新目录并更新版本。实际 Prompt 内容、版本、摘要和工具 Schema 分别以 `extraction_prompt`、`extraction_tools` 保存，提炼记录引用其摘要；无需额外 SQL 迁移。

模型每轮只能调用一个离线工具：`submit_candidate` 提交一条候选；`finish_extraction` 说明完成或不生成的原因。程序检查字段、快照和证据引用，补齐 ID、版本及来源。候选 ID 根据完整内容计算，同内容重提复用同一候选；不做语义去重。工具不具备在线执行或发布能力。

候选独立保存，后续失败不会撤销已有候选。结果状态为 `succeeded`、`abstained`、`partial` 或 `failed`；每次调用结果和反馈都会持久化。未知字段（包括模型自填 ID）、空白必要字段、伪造引用会被拒绝。程序校验不证明语义正确，候选仍需 review 和场景验证。

当前每次 extract 会创建新的提炼任务；审计记录用于追溯，尚未实现提炼任务的进程重启续跑和跨任务模型调用去重。进程强制退出或数据库不可用时，最终审计可能未完成，已保存候选仍保留。没有包含运行筛选或自动批量调度。

## 测试目录与发布标准

`catalog.example.json` 是字段示例，reviewed_by 留空，不能直接作为可信验证集。应根据实际场景维护：

- symptoms：公开症状标签，供选题。
- context_tags：背景条件，正向场景需覆盖候选背景。
- cause_family：仅离线使用的真实原因类别。
- lineage：同一来源场景的改名/近重复应使用相同值；防止把原题换名当迁移测试。
- regression：固定保留集成员。
- dataset_root / scenario_id：定位现有 Simulator 场景。

目录必须包括来源场景的 lineage，来源场景及同 lineage 不进入验证。每张经验必须有独立正向案例、相同症状不同原因的反例；整批必须有固定回归案例。缺少任何一类就保存 gap 报告并阻止发布。人工目录标注的正确性仍需维护，程序不能自动证明 lineage 无泄漏。

每个场景至少重复 2 次；交替执行组别顺序。当前保守门槛：直接组和检索组逐场景成功数不低于基线；不出现 safety 检查失败；总成功数提升或在不降低成功数时减少调用；token 比率不超过配置值；检索组确实返回过经验。没有改善也不发布。阈值由操作者设置，不由提炼模型决定。这是运行对照门槛，不声称统计显著性。

每次 trial 的 input.json（含离线答案）、evaluation.json、runtime.sqlite 存在 output-dir 的独立目录。它们只供离线分析，禁止通过经验工具直接读取。报告绑定源码可执行文件、Prompt、Skills、模型配置、数据集和 Evaluator 摘要；验证途中发生变化会拒绝通过。

## 在线接入

主命令设置 `EXPERIENCE_ENABLED=true` 后，新运行读取 active 版本。尚无 active 时保持原有调查能力。

- `search_experiences`：当前描述、症状、已知观察、组件、近期变更、尝试结果；最多 5 条，摘要总计 12KB。
- `get_experience`：按 ID/版本读取完整卡片，包含适用限制、排除信号和来源。
- `get_experience_evidence`：只读取该卡片明确引用的来源片段，最大 32KB；不能任意访问其他运行。

这三个工具被分类为 recall_evidence，而非 read，所以它们不产生当前环境 evidence_ref，也不能满足指标/日志/Trace/状态调查门禁。数据库/Qdrant/reranker 查询失败会作为工具错误返回，Agent 可继续原有调查，不执行替代修复。已固定版本在启动时不可用则拒绝悄悄切版本。

## 验证命令

```sh
 go test ./internal/experience ./internal/experienceeval ./internal/tools ./internal/storage
 go test ./...
 # 真正 Qdrant 联调，使用独立验收服务，测试结束清理自己创建的 collection
 QDRANT_TEST_URL=http://127.0.0.1:6333 go test ./internal/experience -run TestLiveQdrant -count=1
```

正式启用前还需要在实际 embedding/reranker 上校准阈值，并用本项目故障场景跑候选验证；本地单元测试不能证明某条经验已经提升真实模型表现。

协议参考：[Qdrant Hybrid Query](https://qdrant.tech/documentation/search/hybrid-queries/)、[Sparse vectors](https://qdrant.tech/documentation/concepts/vectors/)。

已附 `catalog.toolops-v2.json`：按仓库机制矩阵整理的 15 个开发/回归场景，可直接用于 plan。它不会把 v2 包装成最终泛化测试集；来源及同 lineage 会排除。仅有一个案例的原因类别仍会报告正例缺口，需要另补独立场景。没有使用 `toolops-v3-test` 做经验提炼或调参。
