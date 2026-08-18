"""按 manifest 批量评测一个版本化 Talon 导出目录，并聚合运行指标。"""

import json
from collections import Counter
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping

from .core import EVALUATOR_VERSION, EvaluationInputError, evaluate

EXPORT_SCHEMA_VERSION = "talon.evaluation-export/v2"
BATCH_RESULT_SCHEMA_VERSION = "talon.evaluation-batch-result/v1"


def evaluate_directory(
    directory: Path,
    evaluate_one: Callable[[Mapping[str, Any]], Dict[str, Any]] = evaluate,
) -> Dict[str, Any]:
    """校验 manifest 与每个 Artifact 的对应关系，再逐 Run 执行指定评测函数。"""

    directory = directory.resolve()
    manifest = _read_json(directory / "manifest.json")
    if not isinstance(manifest, Mapping):
        raise EvaluationInputError("export manifest must be a JSON object")
    if manifest.get("schema_version") != EXPORT_SCHEMA_VERSION:
        raise EvaluationInputError(
            "unsupported export schema {!r}".format(manifest.get("schema_version"))
        )
    entries = manifest.get("runs")
    if not isinstance(entries, list) or not entries:
        raise EvaluationInputError("export manifest runs must be a non-empty array")

    evaluated: List[Dict[str, Any]] = []
    for entry in entries:
        if not isinstance(entry, Mapping):
            raise EvaluationInputError("export manifest run must be a JSON object")
        file_name = entry.get("file")
        if not isinstance(file_name, str) or not file_name or Path(file_name).name != file_name:
            raise EvaluationInputError("export manifest contains an unsafe run file")
        payload = _read_json(directory / file_name)
        if not isinstance(payload, Mapping):
            raise EvaluationInputError("evaluation input {} must be a JSON object".format(file_name))
        artifact = payload.get("artifact")
        if not isinstance(artifact, Mapping):
            raise EvaluationInputError("evaluation input {} has no artifact".format(file_name))
        # manifest 是批次索引，必须与文件内 Artifact 一致，防止错配或替换文件。
        for field in ("run_id", "scenario_id", "outcome"):
            if artifact.get(field) != entry.get(field):
                raise EvaluationInputError(
                    "manifest {} mismatch for {}".format(field, file_name)
                )
        result = evaluate_one(payload)
        evaluated.append(_run_result(artifact, result))

    scenario_groups: Dict[str, List[Dict[str, Any]]] = {}
    for run in evaluated:
        scenario_groups.setdefault(run["scenario_id"], []).append(run)

    failure_stages: Counter[str] = Counter()
    failure_reasons: Counter[str] = Counter()
    for run in evaluated:
        if run["runtime_outcome"] == "failed":
            failure_stages[run["failure_stage"] or "unknown"] += 1
            failure_reasons[run["failure_reason"] or "unknown"] += 1

    return {
        "schema_version": BATCH_RESULT_SCHEMA_VERSION,
        "evaluator_version": EVALUATOR_VERSION,
        "source": {
            "export_schema_version": manifest.get("schema_version", ""),
            "artifact_schema_version": manifest.get("artifact_schema_version", ""),
            "code_version": manifest.get("code_version", ""),
            "dataset_version": manifest.get("dataset_version", ""),
        },
        "summary": _aggregate(evaluated),
        "failure_stages": dict(sorted(failure_stages.items())),
        "failure_reasons": dict(sorted(failure_reasons.items())),
        "scenarios": {
            scenario_id: _aggregate(runs)
            for scenario_id, runs in sorted(scenario_groups.items())
        },
        "runs": evaluated,
    }


def _run_result(artifact: Mapping[str, Any], result: Mapping[str, Any]) -> Dict[str, Any]:
    """把单 Run 的 Artifact 指标和规则结果压缩成批次报告所需的稳定摘要。"""

    summary = _mapping(artifact.get("summary"))
    evaluation_summary = _mapping(result.get("summary"))
    failure = _mapping(artifact.get("failure"))
    duration = artifact.get("duration", 0)
    duration_ns = float(duration) if _number(duration) else 0.0
    steps = summary.get("model_calls", 0)
    tokens = summary.get("total_tokens", 0)
    run = {
        "run_id": artifact.get("run_id", ""),
        "scenario_id": artifact.get("scenario_id", ""),
        "runtime_outcome": artifact.get("outcome", ""),
        "verdict": evaluation_summary.get("verdict", "failed"),
        "score": evaluation_summary.get("score", 0.0),
        "coverage": evaluation_summary.get("coverage", 0.0),
        "passed": evaluation_summary.get("passed", 0),
        "failed": evaluation_summary.get("failed", 0),
        "skipped": evaluation_summary.get("skipped", 0),
        "total": evaluation_summary.get("total", 0),
        "steps": int(steps) if _number(steps) else 0,
        "tokens": int(tokens) if _number(tokens) else 0,
        "duration_ms": round(duration_ns / 1_000_000, 3),
        "failure_stage": failure.get("stage", ""),
        "failure_reason": failure.get("message", ""),
    }
    if isinstance(result.get("judge"), Mapping):
        run["judge"] = dict(result["judge"])
        run["judge_checks"] = [
            dict(item)
            for item in result.get("checks", [])
            if isinstance(item, Mapping)
            and item.get("id") == "diagnosis.root_cause_correctness"
        ]
    return run


def _aggregate(runs: List[Dict[str, Any]]) -> Dict[str, Any]:
    """聚合一组 Run；score 只统计已评项，coverage 额外反映 skipped 比例。"""

    count = len(runs)
    completed = sum(run["runtime_outcome"] == "completed" for run in runs)
    failed_runtime = sum(run["runtime_outcome"] == "failed" for run in runs)
    # incomplete 不是失败：本地确定性模式允许把语义题留给后续 LLM Judge。
    successful = sum(
        run["runtime_outcome"] == "completed" and run["verdict"] != "failed"
        for run in runs
    )
    passed_checks = sum(int(run["passed"]) for run in runs)
    failed_checks = sum(int(run["failed"]) for run in runs)
    skipped_checks = sum(int(run["skipped"]) for run in runs)
    evaluated_checks = passed_checks + failed_checks
    total_checks = evaluated_checks + skipped_checks
    judges = [_mapping(run.get("judge")) for run in runs if run.get("judge")]
    return {
        "runs": count,
        "successful_runs": successful,
        "success_rate": _ratio(successful, count),
        "completed": completed,
        "failed": failed_runtime,
        "average_steps": _average(runs, "steps"),
        "average_tokens": _average(runs, "tokens"),
        "average_duration_ms": _average(runs, "duration_ms"),
        "score": _ratio(passed_checks, evaluated_checks),
        "coverage": _ratio(evaluated_checks, total_checks),
        "passed_checks": passed_checks,
        "failed_checks": failed_checks,
        "skipped_checks": skipped_checks,
        "total_checks": total_checks,
        "judge_calls": sum(int(value.get("calls", 0)) for value in judges),
        "judge_total_tokens": sum(
            int(_mapping(value.get("usage")).get("total_tokens", 0)) for value in judges
        ),
        "judge_duration_ms": round(
            sum(float(value.get("duration_ms", 0.0)) for value in judges), 3
        ),
    }


def _average(runs: List[Dict[str, Any]], field: str) -> float:
    if not runs:
        return 0.0
    return round(sum(float(run[field]) for run in runs) / len(runs), 6)


def _ratio(numerator: int, denominator: int) -> float:
    return round(numerator / denominator, 6) if denominator else 0.0


def _mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _read_json(path: Path) -> Any:
    try:
        with path.open("r", encoding="utf-8") as source:
            return json.load(source)
    except (OSError, json.JSONDecodeError) as exc:
        raise EvaluationInputError("read {}: {}".format(path, exc)) from exc
