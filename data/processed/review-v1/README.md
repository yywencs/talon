# Review 数据集 v1

该目录保存 OpenTalon 代码审查链路的首批端到端评测候选。它们来自 Go Vulnerability Database 的已审核记录，并由 GitHub Fix Commit 元数据进一步筛选。

## 当前文件

- `shortlist-30.jsonl`：30 条分层候选，用于人工复核和替补。
- `pilot-15.jsonl`：首批 15 条评测候选，一条记录对应一个 Advisory 和一个仓库。

每条记录都保留 Advisory、Fix Commit、文件级 Patch，并在 `selection` 字段中记录分类、评分、选择顺序及证据。

## 选择规则

首批候选同时满足：

1. Fix Commit 可物化且只有一个父提交；
2. 总改动 5–200 行、最多 4 个文件、最多 3 个可审查 Go 源文件；
3. Go Vulnerability Database 标注的受影响符号出现在 Patch 中；
4. Commit 同时修改 `_test.go` 文件；
5. 最终集合不重复仓库和 Advisory，并限制单一年份的占比；
6. 按 injection、auth、network、filesystem、crypto、dos_memory 和 other 分层抽样。

这些条件只用于判断样本是否适合稳定重放，不用于重新判断漏洞是否成立。

## 重新生成

在 OpenTalon 仓库根目录运行：

```bash
go run ./tools/dataset/cmd/dataset govulndb-select \
  --size 30 \
  --seed opentalon-pilot-v1 \
  --output ./data/processed/review-v1/shortlist-30.jsonl

go run ./tools/dataset/cmd/dataset govulndb-select \
  --size 15 \
  --seed opentalon-pilot-v1 \
  --output ./data/processed/review-v1/pilot-15.jsonl
```

相同 enriched 输入、数量和 Seed 会生成字节级一致的 JSONL。

## 下载首批仓库

```bash
./tools/dataset/scripts/fetch-review-repos.sh
```

仓库默认保存到 `data/repos/review-v1/`，按 `01-owner__repo` 编号。脚本只获取 Fix Commit 及其单一父提交，默认检出 Fix Commit，并补齐生成二者 Diff 所需的 Blob；它不会初始化 submodule，也不会运行第三方仓库中的代码。命令可以安全续跑，已完成的目录会校验远端地址、HEAD 和父提交，并补齐离线 Diff 后跳过。
