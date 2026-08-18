#!/bin/sh

set -eu
set -f

agent_binary=${EVAL_AGENT_BINARY:-bin/talon-eval}
export_binary=${EVAL_EXPORT_BINARY:-bin/talon-export-eval}
data_root=${EVAL_DATA_ROOT:-data}
dataset_version=${EVAL_DATASET:-toolops-v1}
eval_version=${EVAL_VERSION:-}
repeat=${EVAL_REPEAT:-3}
timeout=${EVAL_TIMEOUT:-5m}
max_steps=${EVAL_MAX_STEPS:-24}
env_file=${EVAL_ENV_FILE:-.env}
output_dir=${EVAL_OUTPUT_DIR:-}
run_judge=${EVAL_JUDGE:-0}
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

dataset_path="$data_root/$dataset_version"
code_version="talon-toolops-agent/$eval_version"
if [ -z "$output_dir" ]; then
    output_dir="evaluation-data/$eval_version-$dataset_version-r$repeat"
fi
deterministic_report="$output_dir-deterministic-result.json"
full_report="$output_dir-full-result.json"

for path in "$output_dir" "$deterministic_report"; do
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

scenario_count=0
command_failures=0
for scenario_id in $scenario_ids; do
    scenario_count=$((scenario_count + 1))
    current_repeat=1
    while [ "$current_repeat" -le "$repeat" ]; do
        echo "[eval] scenario=$scenario_id repeat=$current_repeat/$repeat"
        if ! COZELOOP_ENABLED=${COZELOOP_ENABLED:-false} \
                "$agent_binary" \
                --dataset "$dataset_path" \
                --scenario "$scenario_id" \
                --env-file "$env_file" \
                --timeout "$timeout" \
                --max-agent-steps "$max_steps" \
                --auto-approve=true; then
            command_failures=$((command_failures + 1))
            echo "[eval] Agent command failed; continuing so the batch report can retain the failed Artifact" >&2
        fi
        current_repeat=$((current_repeat + 1))
    done
done

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
        "$output_dir" --output "$full_report" --pretty --judge --env-file "$env_file"
    PYTHONPATH="$evaluator_source" python3 -m talon_evaluator.verify report \
        "$full_report" --expected-runs "$expected_runs" --require-complete
fi

echo "[eval] completed $expected_runs runs"
echo "[eval] Agent command failures=$command_failures"
echo "[eval] export=$output_dir"
echo "[eval] deterministic_report=$deterministic_report"
if [ "$run_judge" = "1" ]; then
    echo "[eval] full_report=$full_report"
fi
