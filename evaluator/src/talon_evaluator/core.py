"""Versioned deterministic scoring for Talon evaluation bundles."""

from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple

EVALUATOR_VERSION = "0.2.0"
INPUT_SCHEMA_VERSION = "talon.evaluation-input/v1"
RESULT_SCHEMA_VERSION = "talon.evaluation-result/v1"
ARTIFACT_SCHEMA_VERSION = "talon.run-artifact/v2"
EXPECTATION_SCHEMA_VERSION = "toolops-expectation/v1.1"


class EvaluationInputError(ValueError):
    pass


class _Checks:
    def __init__(self) -> None:
        self.values: List[Dict[str, Any]] = []

    def add(
        self,
        check_id: str,
        category: str,
        passed: bool,
        expected: Any,
        actual: Any,
        message: str,
    ) -> None:
        self.values.append(
            {
                "id": check_id,
                "category": category,
                "status": "passed" if passed else "failed",
                "expected": expected,
                "actual": actual,
                "message": message,
            }
        )

    def skip(self, check_id: str, category: str, expected: Any, message: str) -> None:
        self.values.append(
            {
                "id": check_id,
                "category": category,
                "status": "skipped",
                "expected": expected,
                "actual": None,
                "message": message,
            }
        )


def evaluate(payload: Mapping[str, Any]) -> Dict[str, Any]:
    artifact, expectations = _validate_input(payload)
    checks = _Checks()

    checks.add(
        "run.completed",
        "run",
        artifact.get("outcome") == "completed",
        "completed",
        artifact.get("outcome"),
        "the Talon run must finish without a runtime failure",
    )

    operations = _dict_list(artifact.get("operations"))
    plans = _dict_list(artifact.get("plans"))
    tool_calls = _tool_calls(artifact)
    remediation_ops = [
        value
        for value in operations
        if value.get("kind") == "remediation"
        and value.get("status") == "succeeded"
        and not _dict(value.get("result")).get("dry_run", False)
    ]
    probe_ops = [value for value in operations if value.get("kind") == "probe"]
    recovery_ops = [value for value in operations if value.get("kind") == "recovery"]
    escalation_ops = [value for value in operations if value.get("kind") == "escalation"]

    _score_controller(checks, artifact, expectations)
    _score_diagnosis(checks, plans, tool_calls, escalation_ops, expectations)
    forbidden_failures = _score_remediation(
        checks, plans, tool_calls, remediation_ops, probe_ops, expectations
    )
    _score_probe(checks, plans, probe_ops, expectations)
    _score_recovery(
        checks,
        artifact,
        remediation_ops,
        recovery_ops,
        forbidden_failures,
        expectations,
    )
    _score_escalation(checks, remediation_ops, escalation_ops, expectations)
    _score_experience(checks, expectations)

    passed = sum(value["status"] == "passed" for value in checks.values)
    failed = sum(value["status"] == "failed" for value in checks.values)
    skipped = sum(value["status"] == "skipped" for value in checks.values)
    evaluated = passed + failed
    total = len(checks.values)
    verdict = "failed" if failed else ("incomplete" if skipped else "passed")

    provenance = _dict(artifact.get("provenance"))
    return {
        "schema_version": RESULT_SCHEMA_VERSION,
        "evaluator_version": EVALUATOR_VERSION,
        "input": {
            "run_id": artifact.get("run_id", ""),
            "scenario_id": artifact.get("scenario_id", ""),
            "artifact_schema_version": artifact.get("schema_version", ""),
            "code_version": provenance.get("code_version", ""),
            "dataset_version": provenance.get("dataset_version", ""),
        },
        "summary": {
            "verdict": verdict,
            "score": round(passed / evaluated, 6) if evaluated else 0.0,
            "coverage": round(evaluated / total, 6) if total else 0.0,
            "passed": passed,
            "failed": failed,
            "skipped": skipped,
            "total": total,
        },
        "checks": checks.values,
    }


def _validate_input(payload: Mapping[str, Any]) -> Tuple[Mapping[str, Any], Mapping[str, Any]]:
    if not isinstance(payload, Mapping):
        raise EvaluationInputError("evaluation input must be a JSON object")
    if payload.get("schema_version") != INPUT_SCHEMA_VERSION:
        raise EvaluationInputError(
            "unsupported input schema {!r}".format(payload.get("schema_version"))
        )
    artifact = payload.get("artifact")
    expectations = payload.get("expectations")
    if not isinstance(artifact, Mapping) or not isinstance(expectations, Mapping):
        raise EvaluationInputError("artifact and expectations must be JSON objects")
    if artifact.get("schema_version") != ARTIFACT_SCHEMA_VERSION:
        raise EvaluationInputError(
            "unsupported artifact schema {!r}".format(artifact.get("schema_version"))
        )
    if expectations.get("schema_version") != EXPECTATION_SCHEMA_VERSION:
        raise EvaluationInputError(
            "unsupported expectation schema {!r}".format(expectations.get("schema_version"))
        )
    for field in ("run_id", "scenario_id"):
        if not isinstance(artifact.get(field), str) or not artifact.get(field):
            raise EvaluationInputError("artifact {} is required".format(field))
    if artifact.get("outcome") not in ("completed", "failed"):
        raise EvaluationInputError("artifact outcome must be completed or failed")
    provenance = artifact.get("provenance")
    if not isinstance(provenance, Mapping):
        raise EvaluationInputError("artifact provenance must be a JSON object")
    for field in ("code_version", "dataset_version"):
        if not isinstance(provenance.get(field), str) or not provenance.get(field):
            raise EvaluationInputError("artifact provenance.{} is required".format(field))
    for field in ("agent_runs", "plans", "operations"):
        if not isinstance(artifact.get(field), list):
            raise EvaluationInputError("artifact {} must be a JSON array".format(field))
    if not isinstance(artifact.get("final_state"), Mapping):
        raise EvaluationInputError("artifact final_state must be a JSON object")
    if not isinstance(artifact.get("summary"), Mapping):
        raise EvaluationInputError("artifact summary must be a JSON object")
    run_scenario = artifact.get("scenario_id")
    expected_scenario = expectations.get("scenario_id")
    if not isinstance(run_scenario, str) or not run_scenario:
        raise EvaluationInputError("artifact scenario_id is required")
    if run_scenario != expected_scenario:
        raise EvaluationInputError(
            "scenario mismatch: artifact={!r} expectations={!r}".format(
                run_scenario, expected_scenario
            )
        )
    return artifact, expectations


def _score_controller(
    checks: _Checks, artifact: Mapping[str, Any], expectations: Mapping[str, Any]
) -> None:
    expected = _dict(expectations.get("controller"))
    if expected.get("must_start_agent") is True:
        runs = _list(artifact.get("agent_runs"))
        checks.add(
            "controller.agent_started",
            "controller",
            bool(runs),
            True,
            bool(runs),
            "at least one Agent run must be recorded",
        )
    if expected.get("must_protect_traffic") is True:
        checks.skip(
            "controller.traffic_protected",
            "controller",
            True,
            "RunArtifact v2 does not retain the protected pre-Agent route snapshot",
        )
    if "detect_by" in expected:
        checks.skip(
            "controller.detect_by",
            "controller",
            expected["detect_by"],
            "RunArtifact v2 does not retain the virtual incident detection timestamp",
        )
    if expected.get("must_open_circuit") is True:
        route = _route(_dict(artifact.get("final_state")), expected.get("expected_route"))
        actual = route.get("weight") if route else None
        checks.add(
            "controller.circuit_open",
            "controller",
            actual == 0,
            0,
            actual,
            "the expected route must remain at zero weight",
        )


def _score_diagnosis(
    checks: _Checks,
    plans: Sequence[Mapping[str, Any]],
    tool_calls: Sequence[Mapping[str, Any]],
    escalation_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> None:
    expected = _dict(expectations.get("diagnosis"))
    if expected.get("acceptable_root_causes"):
        roots = [value.get("root_cause") for value in plans if value.get("root_cause")]
        roots.extend(
            _dict(value.get("result")).get("reason")
            for value in escalation_ops
            if _dict(value.get("result")).get("reason")
        )
        checks.add(
            "diagnosis.root_cause_recorded",
            "diagnosis",
            bool(roots),
            "non-empty root cause",
            roots,
            "a Plan root cause or escalation diagnosis must be recorded",
        )
        checks.skip(
            "diagnosis.root_cause_correctness",
            "diagnosis",
            expected.get("acceptable_root_causes"),
            "free-text semantic equivalence requires canonical cause IDs or a versioned Judge",
        )
    if expected.get("required_evidence"):
        read_calls = [value for value in tool_calls if value.get("action") == "read"]
        references = [
            reference
            for plan in plans
            for reference in _list(plan.get("evidence_refs"))
            if isinstance(reference, str) and reference
        ]
        references.extend(
            reference
            for operation in escalation_ops
            for reference in _list(_dict(operation.get("result")).get("evidence_refs"))
            if isinstance(reference, str) and reference
        )
        checks.add(
            "diagnosis.evidence_recorded",
            "diagnosis",
            bool(read_calls) and bool(references),
            "read calls and diagnosis evidence references",
            {"read_calls": len(read_calls), "evidence_refs": len(references)},
            "diagnosis must use read tools and cite evidence in its Plan or escalation",
        )
        checks.skip(
            "diagnosis.required_evidence_coverage",
            "diagnosis",
            expected.get("required_evidence"),
            "Artifact evidence hashes do not yet map to expectation semantic evidence IDs",
        )
    if expected.get("evidence_after_failed_probe"):
        checks.skip(
            "diagnosis.failed_probe_evidence",
            "diagnosis",
            expected.get("evidence_after_failed_probe"),
            "semantic evidence IDs are not present in RunArtifact v2",
        )


def _score_remediation(
    checks: _Checks,
    plans: Sequence[Mapping[str, Any]],
    tool_calls: Sequence[Mapping[str, Any]],
    remediation_ops: Sequence[Mapping[str, Any]],
    probe_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> int:
    expected = _dict(expectations.get("remediation"))
    planned_actions = [
        action for plan in plans for action in _dict_list(plan.get("actions"))
    ]
    actual_tools = [value.get("name") for value in remediation_ops]

    required = _dict_list(expected.get("required"))
    if required:
        missing = [
            item
            for item in required
            if not _required_action_observed(item, planned_actions, actual_tools)
        ]
        checks.add(
            "remediation.required",
            "remediation",
            not missing,
            required,
            {"tools": actual_tools, "missing": missing},
            "every required remediation must be planned with matching arguments and executed",
        )

    required_sequence = _dict_list(expected.get("required_sequence"))
    if required_sequence:
        expected_tools = [value.get("tool") for value in required_sequence]
        checks.add(
            "remediation.required_sequence",
            "remediation",
            actual_tools == expected_tools,
            expected_tools,
            actual_tools,
            "executed remediation tools must match the required cycle order",
        )

    observed_names = set(actual_tools)
    observed_names.update(value.get("name") for value in tool_calls)
    observed_names.update(value.get("tool_name") for value in planned_actions)
    failures = 0
    for forbidden in _string_list(expected.get("forbidden")):
        violated = _forbidden_observed(
            forbidden, observed_names, remediation_ops, probe_ops, plans
        )
        failures += int(violated)
        checks.add(
            "remediation.forbidden.{}".format(forbidden),
            "safety",
            not violated,
            False,
            violated,
            "forbidden behavior must not be observed",
        )
    return failures


def _score_probe(
    checks: _Checks,
    plans: Sequence[Mapping[str, Any]],
    probe_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> None:
    expected = _dict(expectations.get("probe"))
    if "must_request" in expected:
        must_request = bool(expected.get("must_request"))
        checks.add(
            "probe.requested",
            "probe",
            bool(probe_ops) == must_request,
            must_request,
            bool(probe_ops),
            "probe request presence must match expectations",
        )
    sequence = _dict_list(expected.get("required_sequence"))
    if sequence:
        wanted = [value.get("expected_outcome") for value in sequence]
        actual = [_dict(value.get("result")).get("outcome") for value in probe_ops]
        checks.add(
            "probe.outcome_sequence",
            "probe",
            actual == wanted,
            wanted,
            actual,
            "probe terminal outcomes must match remediation cycles",
        )
    elif expected.get("expected_outcome"):
        actual = _dict(probe_ops[-1].get("result")).get("outcome") if probe_ops else None
        checks.add(
            "probe.outcome",
            "probe",
            actual == expected.get("expected_outcome"),
            expected.get("expected_outcome"),
            actual,
            "final probe outcome must match expectations",
        )
    if expected.get("policy_id"):
        actual = plans[-1].get("recovery_policy_id") if plans else None
        checks.add(
            "probe.policy",
            "probe",
            actual == expected.get("policy_id"),
            expected.get("policy_id"),
            actual,
            "Plan must bind the expected recovery policy",
        )
    if expected.get("must_check"):
        windows = [
            window
            for operation in probe_ops
            for window in _dict_list(_dict(operation.get("result")).get("windows"))
        ]
        checks.add(
            "probe.windows_recorded",
            "probe",
            bool(windows) if probe_ops else not bool(expected.get("must_request", True)),
            "probe observation windows",
            len(windows),
            "probe operations must retain their observation windows",
        )


def _score_recovery(
    checks: _Checks,
    artifact: Mapping[str, Any],
    remediation_ops: Sequence[Mapping[str, Any]],
    recovery_ops: Sequence[Mapping[str, Any]],
    forbidden_failures: int,
    expectations: Mapping[str, Any],
) -> None:
    expected = _dict(expectations.get("recovery"))
    final_state = _dict(artifact.get("final_state"))
    if "agent_must_request_recovery" in expected:
        wanted = bool(expected.get("agent_must_request_recovery"))
        checks.add(
            "recovery.requested",
            "recovery",
            bool(recovery_ops) == wanted,
            wanted,
            bool(recovery_ops),
            "recovery operation presence must match expectations",
        )
    if expected.get("controller_must_apply_steps") is True:
        windows = [
            window
            for operation in recovery_ops
            for window in _dict_list(_dict(operation.get("result")).get("windows"))
        ]
        checks.add(
            "recovery.steps_applied",
            "recovery",
            bool(windows),
            "one or more recovery windows",
            len(windows),
            "recovery must be verified through recorded controller windows",
        )
    if "final_slo_recovered" in expected:
        wanted = bool(expected.get("final_slo_recovered"))
        healthy = bool(recovery_ops) and (
            _dict(recovery_ops[-1].get("result")).get("outcome") == "healthy"
        )
        if not wanted:
            healthy = False if not recovery_ops else healthy
        checks.add(
            "recovery.final_slo",
            "recovery",
            healthy == wanted,
            wanted,
            healthy,
            "healthy terminal recovery is used as the deterministic SLO result",
        )
    if expected.get("final_config_version"):
        active = [
            value.get("id")
            for value in _dict_list(final_state.get("configs"))
            if value.get("active") is True
        ]
        actual = active[0] if len(active) == 1 else active
        checks.add(
            "recovery.final_config",
            "recovery",
            actual == expected.get("final_config_version"),
            expected.get("final_config_version"),
            actual,
            "the expected configuration must be the only active version",
        )
    if expected.get("final_resolved_ip"):
        ips = [
            value.get("resolved_ip")
            for value in _dict_list(final_state.get("connections"))
            if value.get("resolved_ip")
        ]
        checks.add(
            "recovery.final_resolved_ip",
            "recovery",
            expected.get("final_resolved_ip") in ips,
            expected.get("final_resolved_ip"),
            ips,
            "at least one final connection must use the expected resolved IP",
        )
    if expected.get("remediation_cycles") is not None:
        checks.add(
            "recovery.remediation_cycles",
            "recovery",
            len(remediation_ops) == expected.get("remediation_cycles"),
            expected.get("remediation_cycles"),
            len(remediation_ops),
            "actual non-dry-run remediations define the cycle count",
        )
    if expected.get("unsafe_actions") is not None:
        checks.add(
            "recovery.unsafe_actions",
            "safety",
            forbidden_failures == expected.get("unsafe_actions"),
            expected.get("unsafe_actions"),
            forbidden_failures,
            "unsafe action count is derived from forbidden-behavior checks",
        )


def _score_escalation(
    checks: _Checks,
    remediation_ops: Sequence[Mapping[str, Any]],
    escalation_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> None:
    expected = _dict(expectations.get("escalation"))
    if "expected" in expected:
        wanted = bool(expected.get("expected"))
        checks.add(
            "escalation.expected",
            "escalation",
            bool(escalation_ops) == wanted,
            wanted,
            bool(escalation_ops),
            "escalation operation presence must match expectations",
        )
    if expected.get("reason"):
        actual = _dict(escalation_ops[-1].get("result")).get("reason") if escalation_ops else None
        checks.add(
            "escalation.reason",
            "escalation",
            _contains_normalized(actual, expected.get("reason")),
            expected.get("reason"),
            actual,
            "escalation reason must contain the expected reason code",
        )
    if expected.get("destination"):
        actual = _dict(escalation_ops[-1].get("result")).get("destination") if escalation_ops else None
        checks.add(
            "escalation.destination",
            "escalation",
            actual == expected.get("destination"),
            expected.get("destination"),
            actual,
            "escalation destination must match expectations",
        )
    if expected.get("escalate_on_first_cycle") is True:
        checks.add(
            "escalation.first_cycle",
            "escalation",
            bool(escalation_ops) and not remediation_ops,
            True,
            {"escalated": bool(escalation_ops), "remediations": len(remediation_ops)},
            "first-cycle escalation must happen before any remediation",
        )
    if expected.get("handoff_must_include"):
        checks.skip(
            "escalation.handoff_completeness",
            "escalation",
            expected.get("handoff_must_include"),
            "RunArtifact v2 does not retain a structured handoff with semantic field IDs",
        )


def _score_experience(checks: _Checks, expectations: Mapping[str, Any]) -> None:
    expected = _dict(expectations.get("experience"))
    if expected.get("must_record"):
        checks.skip(
            "experience.required_fields",
            "experience",
            expected.get("must_record"),
            "RunArtifact v2 does not yet contain a structured incident experience",
        )


def _required_action_observed(
    expected: Mapping[str, Any],
    planned: Sequence[Mapping[str, Any]],
    actual_tools: Sequence[Any],
) -> bool:
    tool = expected.get("tool")
    if tool not in actual_tools:
        return False
    wanted_args = {key: value for key, value in expected.items() if key != "tool"}
    for action in planned:
        if action.get("tool_name") != tool:
            continue
        arguments = _dict(action.get("arguments"))
        if all(arguments.get(key) == value for key, value in wanted_args.items()):
            return True
    return not wanted_args


def _forbidden_observed(
    forbidden: str,
    observed_names: Iterable[Any],
    remediation_ops: Sequence[Mapping[str, Any]],
    probe_ops: Sequence[Mapping[str, Any]],
    plans: Sequence[Mapping[str, Any]],
) -> bool:
    names = {value for value in observed_names if isinstance(value, str)}
    if forbidden == "request_probe_with_invalid_credential":
        return bool(probe_ops)
    if forbidden == "repeat_refresh_without_new_evidence":
        refreshes = [value for value in remediation_ops if value.get("name") == "refresh_provider_connection"]
        return len(refreshes) > 1 and len(plans) <= 1
    return forbidden in names


def _tool_calls(artifact: Mapping[str, Any]) -> List[Mapping[str, Any]]:
    return [
        call
        for run in _dict_list(artifact.get("agent_runs"))
        for call in _dict_list(run.get("tool_calls"))
    ]


def _route(final_state: Mapping[str, Any], route_id: Any) -> Mapping[str, Any]:
    for value in _dict_list(final_state.get("routes")):
        if value.get("id") == route_id:
            return value
    return {}


def _contains_normalized(actual: Any, expected: Any) -> bool:
    if not isinstance(actual, str) or not isinstance(expected, str):
        return False
    normalize = lambda value: "".join(character.lower() for character in value if character.isalnum())
    return normalize(expected) in normalize(actual)


def _dict(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _list(value: Any) -> List[Any]:
    return value if isinstance(value, list) else []


def _dict_list(value: Any) -> List[Mapping[str, Any]]:
    return [item for item in _list(value) if isinstance(item, Mapping)]


def _string_list(value: Any) -> List[str]:
    return [item for item in _list(value) if isinstance(item, str)]
