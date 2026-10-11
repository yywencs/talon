# toolops-v3-pilot 数据集(临时试点)

`toolops-v3-pilot` 是 2026-09-04 扩展场景(h11/h12/h13)的**冻结前试点集**:
内容为 `data/toolops-v3-test` 中三个场景修复后版本的副本,用于在正式批次之前
用真实 LLM Agent 验证新机制(单 Intent 打包 / 误导日志代数 / 诱饵工具+升级前探测)
不存在 Harness 级阻塞,并观察 `remediation_intents` 等新断言的真实通过情况。

## 与冻结集的关系

- 试点发现问题 → 修系统时按 test README 冻结规则 2 处置(场景挪 Regression、
  补充、重新冻结),此时尚未消耗正式批次的"一次测量"价值;
- 试点通过 → 记录批次号,执行 `make eval-baseline EVAL_DATASET=toolops-v3-test
  EVAL_REPEAT=3 EVAL_JUDGE=1` 正式批次,本目录即可归档或删除。

## 运行方式

```bash
make eval-baseline EVAL_DATASET=toolops-v3-pilot EVAL_REPEAT=1 EVAL_PARALLEL=3
```
