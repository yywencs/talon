#!/bin/sh

set -eu
set -f

agent_binary=${EVAL_AGENT_BINARY:-bin/talon-eval}
export_binary=${EVAL_EXPORT_BINARY:-bin/talon-export-eval}
data_root=${EVAL_DATA_ROOT:-data}
dataset_version=${EVAL_DATASET:-toolops-v1}
eval_version=${EVAL_VERSION:-}
repeat=${EVAL_REPEAT:-3}
timeout=${EVAL_TIMEOUT:-15m}
max_steps=${EVAL_MAX_STEPS:-24}
env_file=${EVAL_ENV_FILE:-.env}
output_dir=${EVAL_OUTPUT_DIR:-}
run_judge=${EVAL_JUDGE:-0}
parallel=${EVAL_PARALLEL:-1}
judge_concurrency=${EVAL_JUDGE_CONCURRENCY:-1}
evaluator_source=${EVALUATOR_SOURCE:-evaluator/src}

if [ -z "$eval_version" ]; then
    echo "EVAL_VERSION is required" >&2
    exit 2
fi
case "$eval_version" in
    *[!A-Za-z0-9._-]*)
        echo "EVAL_VERSION may contain only letters, digits, dot, underscore and hyphen" >&2
        exit 2
        ;;
esac
case "$repeat" in
    ''|*[!0-9]*|0)
        echo "EVAL_REPEAT must be a positive integer" >&2
        exit 2
        ;;
esac
case "$run_judge" in
    0|1) ;;
    *)
        echo "EVAL_JUDGE must be 0 or 1" >&2
        exit 2
        ;;
esac
case "$parallel" in
    ''|*[!0-9]*|0)
        echo "EVAL_PARALLEL must be a positive integer" >&2
        exit 2
        ;;
esac
case "$judge_concurrency" in
    ''|*[!0-9]*|0)
        echo "EVAL_JUDGE_CONCURRENCY must be a positive integer" >&2
        exit 2
        ;;
esac

dataset_path="$data_root/$dataset_version"
code_version="talon-toolops-agent/$eval_version"
if [ -z "$output_dir" ]; then
    output_dir="evaluation-data/$eval_version-$dataset_version-r$repeat"
fi
deterministic_report="$output_dir-deterministic-result.json"
full_report="$output_dir-full-result.json"
run_log_dir="$output_dir-run-logs"

for path in "$output_dir" "$deterministic_report" "$run_log_dir"; do
    if [ -e "$path" ]; then
        echo "evaluation output already exists: $path" >&2
        exit 2
    fi
done
if [ "$run_judge" = "1" ] && [ -e "$full_report" ]; then
    echo "evaluation output already exists: $full_report" >&2
    exit 2
fi

actual_version=$("$agent_binary" --version | awk 'NR == 1 {print $1}')
if [ "$actual_version" != "$code_version" ]; then
    echo "Agent version mismatch: expected $code_version, found $actual_version" >&2
    exit 2
fi

scenario_ids=$("$agent_binary" --dataset "$dataset_path" --list-scenarios --env-file "")
if [ -z "$scenario_ids" ]; then
    echo "dataset contains no scenarios: $dataset_path" >&2
    exit 2
fi

"$export_binary" \
    --data-root "$data_root" \
    --dataset-version "$dataset_version" \
    --code-version "$code_version" \
    --env-file "$env_file" \
    --preflight

# 运行矩阵：EVAL_PARALLEL 控制 Agent 并发数（默认 1 等价旧行为）。
# 并发时每个 Run 的输出写入 run_log_dir 独立日志，失败用标记文件计数；
# 输出目录本身保留给 Exporter 原子写入，日志放兄弟目录。
mkdir -p "$run_log_dir/failed"
worker_script="$run_log_dir/run-one.sh"
cat > "$worker_script" <<'WORKER'
#!/bin/sh
scenario_id=$1
repeat_index=$2
log_file="$run_log_dir/$scenario_id-r$repeat_index.log"
echo "[eval] scenario=$scenario_id repeat=$repeat_index/$repeat parallel=$parallel"
if COZELOOP_ENABLED=${COZELOOP_ENABLED:-false} \
        "$agent_binary" \
        --dataset "$dataset_path" \
        --scenario "$scenario_id" \
        --env-file "$env_file" \
        --timeout "$timeout" \
        --max-agent-steps "$max_steps" \
        --auto-approve=true >"$log_file" 2>&1; then
    exit 0
fi
echo "[eval] Agent command failed; continuing so the batch report can retain the failed Artifact" >&2
echo "scenario=$scenario_id repeat=$repeat_index" > "$run_log_dir/failed/$scenario_id-r$repeat_index"
exit 1
WORKER
chmod +x "$worker_script"

job_list="$run_log_dir/jobs.txt"
scenario_count=0
: > "$job_list"
for scenario_id in $scenario_ids; do
    scenario_count=$((scenario_count + 1))
    current_repeat=1
    while [ "$current_repeat" -le "$repeat" ]; do
        printf '%s %s\n' "$scenario_id" "$current_repeat" >> "$job_list"
        current_repeat=$((current_repeat + 1))
    done
done

export agent_binary dataset_path env_file timeout max_steps repeat parallel run_log_dir
set +e
xargs -P "$parallel" -n 2 "$worker_script" < "$job_list"
set -e
command_failures=0
if [ -d "$run_log_dir/failed" ]; then
    command_failures=$(find "$run_log_dir/failed" -type f | wc -l | tr -d ' ')
fi

"$export_binary" \
    --data-root "$data_root" \
    --dataset-version "$dataset_version" \
    --code-version "$code_version" \
    --env-file "$env_file" \
    --output "$output_dir"

set --
for scenario_id in $scenario_ids; do
    set -- "$@" --scenario "$scenario_id"
done
PYTHONPATH="$evaluator_source" python3 -m talon_evaluator.verify export \
    "$output_dir" \
    --code-version "$code_version" \
    --dataset-version "$dataset_version" \
    --repeat "$repeat" \
    "$@"

expected_runs=$((scenario_count * repeat))
deterministic_status=0
PYTHONPATH="$evaluator_source" python3 -m talon_evaluator \
    "$output_dir" --output "$deterministic_report" --pretty || deterministic_status=$?
if [ "$deterministic_status" -eq 2 ]; then
    echo "deterministic evaluator failed to produce a valid report" >&2
    exit 2
fi
PYTHONPATH="$evaluator_source" python3 -m talon_evaluator.verify report \
    "$deterministic_report" --expected-runs "$expected_runs"

if [ "$run_judge" = "1" ]; then
    PYTHONPATH="$evaluator_source" python3 -m talon_evaluator \
        "$output_dir" --output "$full_report" --pretty --judge --env-file "$env_file" \
        --judge-concurrency "$judge_concurrency"
    PYTHONPATH="$evaluator_source" python3 -m talon_evaluator.verify report \
        "$full_report" --expected-runs "$expected_runs" --require-complete
fi

echo "[eval] completed $expected_runs runs"
echo "[eval] Agent command failures=$command_failures"
echo "[eval] run_logs=$run_log_dir"
echo "[eval] export=$output_dir"
echo "[eval] deterministic_report=$deterministic_report"
if [ "$run_judge" = "1" ]; then
    echo "[eval] full_report=$full_report"
fi
