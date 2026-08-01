#!/usr/bin/env bash

# 下载 Review 数据集所需的最小 Git 历史：Fix Commit 及其父提交。
# 仓库代码是不可信输入；本脚本只执行 Git fetch/checkout，不运行其中的 Hook、脚本或测试。
set -euo pipefail

input_path="${1:-./data/processed/review-v1/pilot-15.jsonl}"
output_root="${2:-./data/repos/review-v1}"

for dependency in git jq; do
	if ! command -v "${dependency}" >/dev/null 2>&1; then
		echo "missing required command: ${dependency}" >&2
		exit 1
	fi
done

if [[ ! -f "${input_path}" ]]; then
	echo "dataset does not exist: ${input_path}" >&2
	exit 1
fi
mkdir -p "${output_root}"

while IFS=$'\t' read -r rank repository fix_commit parent_commit; do
	if [[ ! "${rank}" =~ ^[0-9]+$ ]] ||
		[[ ! "${repository}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
		[[ ! "${fix_commit}" =~ ^[0-9a-f]{40}$ ]] ||
		[[ ! "${parent_commit}" =~ ^[0-9a-f]{40}$ ]]; then
		echo "invalid dataset row: rank=${rank} repository=${repository}" >&2
		exit 1
	fi

	printf -v rank_prefix '%02d' "${rank}"
	target="${output_root}/${rank_prefix}-${repository//\//__}"
	remote_url="https://github.com/${repository}.git"

	# 已完成的目录只做一致性校验，允许网络中断后安全重跑。
	if [[ -d "${target}/.git" ]]; then
		actual_remote="$(git -C "${target}" remote get-url origin)"
		actual_head="$(git -C "${target}" rev-parse HEAD)"
		if [[ "${actual_remote}" != "${remote_url}" ]] || [[ "${actual_head}" != "${fix_commit}" ]]; then
			echo "existing repository does not match dataset: ${target}" >&2
			exit 1
		fi
		git -C "${target}" cat-file -e "${parent_commit}^{commit}"
		# 部分克隆可能尚未拥有父提交中被修改文件的旧 Blob；预生成 Diff 将其按需补齐。
		git -C "${target}" diff --no-ext-diff "${parent_commit}" "${fix_commit}" >/dev/null
		echo "[${rank_prefix}] verified ${repository}"
		continue
	fi
	if [[ -e "${target}" ]]; then
		echo "refusing to overwrite existing path: ${target}" >&2
		exit 1
	fi

	temporary="$(mktemp -d "${output_root}/.fetch-${rank_prefix}-XXXXXX")"
	git init --quiet "${temporary}"
	git -C "${temporary}" remote add origin "${remote_url}"
	# depth=2 恰好覆盖 Fix Commit 和单一父提交；blob:none 减少 fetch 阶段传输量。
	git -C "${temporary}" fetch --quiet --depth=2 --filter=blob:none --no-tags origin "${fix_commit}"
	git -C "${temporary}" -c advice.detachedHead=false checkout --quiet --detach "${fix_commit}"
	git -C "${temporary}" cat-file -e "${parent_commit}^{commit}"
	git -C "${temporary}" diff --no-ext-diff "${parent_commit}" "${fix_commit}" >/dev/null
	mv "${temporary}" "${target}"
	echo "[${rank_prefix}] downloaded ${repository}"
done < <(jq -r '[.selection.rank, .repository, .fix_commit, .commit.parents[0].sha] | @tsv' "${input_path}")
