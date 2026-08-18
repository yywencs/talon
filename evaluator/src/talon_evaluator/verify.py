"""一键 Baseline 的严格门禁：阻止版本混批、旧 Exporter 丢字段和不完整报告。"""

import argparse
import json
import sys
from collections import Counter
from pathlib import Path
from typing import Any, Mapping, Optional, Sequence

from .batch import BATCH_RESULT_SCHEMA_VERSION, EXPORT_SCHEMA_VERSION
from .core import ARTIFACT_SCHEMA_VERSION, INPUT_SCHEMA_VERSION


REQUIRED_CAPABILITIES = {
    "canonical_evidence_ids",
    "incident_context_snapshot",
    "structured_escalation_handoff",
    "structured_experience",
}


class VerificationError(ValueError):
    """评测产物不满足流水线要求，调用方应让 CI 直接失败。"""

    pass


def verify_export(
    directory: Path,
    code_version: str,
    dataset_version: str,
    scenario_ids: Sequence[str],
    repeat: int,
) -> int:
    """验证导出批次的版本、矩阵数量、文件对应关系和 Artifact 能力声明。"""

    if repeat <= 0:
        raise VerificationError("repeat must be positive")
    # dict 保序去重，既保留命令行场景顺序，也能显式发现重复参数。
    expected_scenarios = list(dict.fromkeys(scenario_ids))
    if not expected_scenarios:
        raise VerificationError("at least one scenario is required")
    if len(expected_scenarios) != len(scenario_ids):
        raise VerificationError("scenario IDs must be unique")

    directory = directory.resolve()
    manifest = _mapping(_read_json(directory / "manifest.json"), "export manifest")
    _expect(manifest.get("schema_version") == EXPORT_SCHEMA_VERSION, "unexpected export schema")
    _expect(
        manifest.get("artifact_schema_version") == ARTIFACT_SCHEMA_VERSION,
        "unexpected Artifact schema",
    )
    _expect(manifest.get("code_version") == code_version, "manifest code version mismatch")
    _expect(
        manifest.get("dataset_version") == dataset_version,
        "manifest dataset version mismatch",
    )

    entries = manifest.get("runs")
    if not isinstance(entries, list):
        raise VerificationError("manifest runs must be an array")
    expected_runs = len(expected_scenarios) * repeat
    _expect(
        len(entries) == expected_runs,
        "expected {} exported runs, found {}".format(expected_runs, len(entries)),
    )

    scenario_counts: Counter[str] = Counter()
    run_ids: set[str] = set()
    # manifest 和每个输入文件都要交叉校验，不能只相信其中一边的版本字段。
    for entry in entries:
        entry = _mapping(entry, "manifest run")
        run_id = _string(entry.get("run_id"), "manifest run_id")
        scenario_id = _string(entry.get("scenario_id"), "manifest scenario_id")
        file_name = _string(entry.get("file"), "manifest file")
        _expect(Path(file_name).name == file_name, "manifest contains an unsafe run file")
        _expect(run_id not in run_ids, "manifest contains duplicate run_id {}".format(run_id))
        _expect(
            entry.get("outcome") in ("completed", "failed"),
            "run {} has a non-terminal outcome".format(run_id),
        )
        run_ids.add(run_id)
        scenario_counts[scenario_id] += 1

        payload = _mapping(_read_json(directory / file_name), "evaluation input")
        _expect(payload.get("schema_version") == INPUT_SCHEMA_VERSION, "input schema mismatch")
        artifact = _mapping(payload.get("artifact"), "RunArtifact")
        _expect(artifact.get("schema_version") == ARTIFACT_SCHEMA_VERSION, "Artifact schema mismatch")
        _expect(artifact.get("run_id") == run_id, "Artifact run_id mismatch")
        _expect(artifact.get("scenario_id") == scenario_id, "Artifact scenario_id mismatch")
        _expect(
            artifact.get("outcome") == entry.get("outcome"),
            "Artifact outcome mismatch",
        )

        provenance = _mapping(artifact.get("provenance"), "Artifact provenance")
        _expect(provenance.get("code_version") == code_version, "Artifact code version mismatch")
        _expect(
            provenance.get("dataset_version") == dataset_version,
            "Artifact dataset version mismatch",
        )
        _string(provenance.get("prompt_version"), "Artifact prompt_version")
        _string(provenance.get("prompt_digest"), "Artifact prompt_digest")

        capabilities = artifact.get("capabilities")
        if not isinstance(capabilities, list) or not all(
            isinstance(value, str) for value in capabilities
        ):
            raise VerificationError("Artifact capabilities must be a string array")
        missing = sorted(REQUIRED_CAPABILITIES - set(capabilities))
        _expect(not missing, "Artifact is missing capabilities: {}".format(", ".join(missing)))

        run_config = _mapping(artifact.get("run_config"), "Artifact run_config")
        _expect(
            run_config.get("context_version") == "talon.incident-context/v1",
            "Artifact context version mismatch",
        )
        agent_runs = artifact.get("agent_runs")
        if not isinstance(agent_runs, list) or not agent_runs:
            raise VerificationError("Artifact agent_runs must be a non-empty array")
        for agent_run in agent_runs:
            agent_run = _mapping(agent_run, "Artifact AgentRun")
            context = _mapping(agent_run.get("context_snapshot"), "AgentRun context_snapshot")
            _expect(
                context.get("schema_version") == "talon.incident-context/v1",
                "AgentRun context schema mismatch",
            )
            _expect(context.get("incident_id") == scenario_id, "AgentRun context Incident mismatch")
            _string(context.get("objective"), "AgentRun context objective")
            digest = _string(context.get("digest"), "AgentRun context digest")
            _expect(
                digest.startswith("sha256:") and len(digest) == 71,
                "AgentRun context digest is invalid",
            )

    expected_counts = Counter({scenario_id: repeat for scenario_id in expected_scenarios})
    _expect(
        scenario_counts == expected_counts,
        "scenario run counts mismatch: expected {}, found {}".format(
            dict(sorted(expected_counts.items())), dict(sorted(scenario_counts.items()))
        ),
    )
    return expected_runs


def verify_report(path: Path, expected_runs: int, require_complete: bool) -> None:
    """验证批次无运行/规则失败；Judge 模式还要求零 skipped 和 100% coverage。"""

    if expected_runs <= 0:
        raise VerificationError("expected-runs must be positive")
    report = _mapping(_read_json(path.resolve()), "evaluation report")
    _expect(
        report.get("schema_version") == BATCH_RESULT_SCHEMA_VERSION,
        "unexpected batch report schema",
    )
    summary = _mapping(report.get("summary"), "evaluation summary")
    _expect(summary.get("runs") == expected_runs, "evaluation run count mismatch")
    _expect(summary.get("completed") == expected_runs, "not every run completed")
    _expect(summary.get("failed") == 0, "one or more runs failed at runtime")
    _expect(summary.get("successful_runs") == expected_runs, "one or more runs failed evaluation")
    _expect(summary.get("failed_checks") == 0, "one or more evaluation checks failed")
    if require_complete:
        _expect(summary.get("skipped_checks") == 0, "complete report contains skipped checks")
        _expect(summary.get("coverage") == 1.0, "complete report coverage is not 100%")


def build_parser() -> argparse.ArgumentParser:
    """为导出批次和最终报告提供两个独立的严格校验子命令。"""

    parser = argparse.ArgumentParser(description="Verify a Talon evaluation batch")
    subparsers = parser.add_subparsers(dest="command", required=True)

    export = subparsers.add_parser("export", help="verify an exported Artifact batch")
    export.add_argument("directory", type=Path)
    export.add_argument("--code-version", required=True)
    export.add_argument("--dataset-version", required=True)
    export.add_argument("--repeat", type=int, required=True)
    export.add_argument("--scenario", action="append", required=True, dest="scenarios")

    report = subparsers.add_parser("report", help="verify an evaluation report")
    report.add_argument("path", type=Path)
    report.add_argument("--expected-runs", type=int, required=True)
    report.add_argument("--require-complete", action="store_true")
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    """执行门禁并以非零退出码阻断不合格的 Baseline。"""

    args = build_parser().parse_args(argv)
    try:
        if args.command == "export":
            count = verify_export(
                args.directory,
                args.code_version,
                args.dataset_version,
                args.scenarios,
                args.repeat,
            )
            print("verified {} exported runs".format(count))
        else:
            verify_report(args.path, args.expected_runs, args.require_complete)
            print("verified evaluation report {}".format(args.path))
    except VerificationError as exc:
        print("talon-evaluation-verify: {}".format(exc), file=sys.stderr)
        return 1
    return 0


def _read_json(path: Path) -> Any:
    try:
        with path.open("r", encoding="utf-8") as source:
            return json.load(source)
    except (OSError, json.JSONDecodeError) as exc:
        raise VerificationError("read {}: {}".format(path, exc)) from exc


def _mapping(value: Any, label: str) -> Mapping[str, Any]:
    if not isinstance(value, Mapping):
        raise VerificationError("{} must be an object".format(label))
    return value


def _string(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise VerificationError("{} must be a non-empty string".format(label))
    return value.strip()


def _expect(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


if __name__ == "__main__":
    raise SystemExit(main())
