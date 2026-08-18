"""Talon 版本化确定性评测核心：只依据结构化事实评分，不猜测自由文本语义。"""

from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple

EVALUATOR_VERSION = "0.4.1"
INPUT_SCHEMA_VERSION = "talon.evaluation-input/v1"
RESULT_SCHEMA_VERSION = "talon.evaluation-result/v1"
ARTIFACT_SCHEMA_VERSION = "talon.run-artifact/v2"
EXPECTATION_SCHEMA_VERSION = "toolops-expectation/v1.1"


class EvaluationInputError(ValueError):
    """输入 JSON、Schema 或版本契约不满足评测要求。"""

    pass


class _Checks:
    """按统一结构收集 passed、failed 和因信息不足而 skipped 的检查项。"""

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
    """校验输入后，按诊断、修复、探针、恢复、升级和经验六个阶段评分。"""

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
    # dry-run 只验证计划可执行性，不能算作真正发生的修复动作。
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

    _score_diagnosis(checks, artifact, plans, tool_calls, escalation_ops, expectations)
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
    _score_escalation(checks, artifact, remediation_ops, escalation_ops, expectations)
    _score_experience(checks, artifact, expectations)

    passed = sum(value["status"] == "passed" for value in checks.values)
    failed = sum(value["status"] == "failed" for value in checks.values)
    skipped = sum(value["status"] == "skipped" for value in checks.values)
    evaluated = passed + failed
    total = len(checks.values)
    # skipped 不等于失败，但意味着证据覆盖不完整，最终 verdict 必须是 incomplete。
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
    """在评分前锁定输入、Artifact 与 expectations 的 Schema 和场景对应关系。"""

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


def _score_diagnosis(
    checks: _Checks,
    artifact: Mapping[str, Any],
    plans: Sequence[Mapping[str, Any]],
    tool_calls: Sequence[Mapping[str, Any]],
    escalation_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> None:
    """检查根因是否被记录，以及 Plan/升级引用的证据是否覆盖场景要求。"""

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
        # 自由文本根因不能用字符串相似度可靠判断，明确留给版本化 LLM Judge。
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
        required = set(_string_list(expected.get("required_evidence")))
        actual = _cited_evidence_ids(tool_calls, references)
        if _supports(artifact, "canonical_evidence_ids"):
            missing = sorted(required - actual)
            checks.add(
                "diagnosis.required_evidence_coverage",
                "diagnosis",
                not missing,
                sorted(required),
                {"evidence_ids": sorted(actual), "missing": missing},
                "cited read calls must cover every required canonical evidence ID",
            )
        else:
            checks.skip(
                "diagnosis.required_evidence_coverage",
                "diagnosis",
                sorted(required),
                "the Artifact predates canonical evidence IDs",
            )
    if expected.get("evidence_after_failed_probe"):
        required = set(_string_list(expected.get("evidence_after_failed_probe")))
        actual = _failed_probe_evidence_ids(artifact)
        if _supports(artifact, "canonical_evidence_ids"):
            missing = sorted(required - actual)
            checks.add(
                "diagnosis.failed_probe_evidence",
                "diagnosis",
                not missing,
                sorted(required),
                {"evidence_ids": sorted(actual), "missing": missing},
                "reinvestigation must add the canonical evidence required after a failed probe",
            )
        else:
            checks.skip(
                "diagnosis.failed_probe_evidence",
                "diagnosis",
                sorted(required),
                "the Artifact predates canonical evidence IDs",
            )


def _score_remediation(
    checks: _Checks,
    plans: Sequence[Mapping[str, Any]],
    tool_calls: Sequence[Mapping[str, Any]],
    remediation_ops: Sequence[Mapping[str, Any]],
    probe_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> int:
    """检查计划动作、dry-run、禁止行为和实际修复是否符合 expectations。"""

    expected = _dict(expectations.get("remediation"))
    planned_actions = _plan_actions(plans)
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
    """确认修复后确实执行了指定路由和策略的小流量探针。"""

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
        policies = _probe_policy_ids(plans)
        actual = policies[0] if len(policies) == 1 else (policies or None)
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
    """验证 Controller 恢复步骤、最终状态和多轮修复次数。"""

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
    artifact: Mapping[str, Any],
    remediation_ops: Sequence[Mapping[str, Any]],
    escalation_ops: Sequence[Mapping[str, Any]],
    expectations: Mapping[str, Any],
) -> None:
    """验证升级时机、稳定 reason_code、目标团队和结构化 handoff。"""

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
    expected_reason_code = expected.get("reason_code") or expected.get("reason")
    if expected_reason_code:
        actual = _dict(escalation_ops[-1].get("result")).get("reason_code") if escalation_ops else None
        checks.add(
            "escalation.reason_code",
            "escalation",
            actual == expected_reason_code,
            expected_reason_code,
            actual,
            "escalation reason_code must exactly match expectations",
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
        required = set(_string_list(expected.get("handoff_must_include")))
        operation_result = _dict(escalation_ops[-1].get("result")) if escalation_ops else {}
        if not _supports(artifact, "structured_escalation_handoff"):
            checks.skip(
                "escalation.handoff_completeness",
                "escalation",
                sorted(required),
                "the Artifact predates structured escalation handoff",
            )
            return
        handoff = _dict(operation_result.get("handoff"))
        present = {field for field, value in handoff.items() if _present(value)}
        missing = sorted(required - present)
        checks.add(
            "escalation.handoff_completeness",
            "escalation",
            not missing,
            sorted(required),
            {"present": sorted(present), "missing": missing},
            "the structured escalation handoff must contain every required non-empty field",
        )


def _score_experience(
    checks: _Checks, artifact: Mapping[str, Any], expectations: Mapping[str, Any]
) -> None:
    """检查 RunArtifact 是否生成可供后续检索和复盘的结构化经验索引。"""

    expected = _dict(expectations.get("experience"))
    if expected.get("must_record"):
        required = set(_string_list(expected.get("must_record")))
        if not _supports(artifact, "structured_experience"):
            checks.skip(
                "experience.required_fields",
                "experience",
                sorted(required),
                "the Artifact predates the deterministic incident experience index",
            )
            return
        actual = set(_string_list(_dict(artifact.get("experience")).get("fields")))
        missing = sorted(required - actual)
        checks.add(
            "experience.required_fields",
            "experience",
            not missing,
            sorted(required),
            {"fields": sorted(actual), "missing": missing},
            "the deterministic incident experience index must include every required field",
        )


def _supports(artifact: Mapping[str, Any], capability: str) -> bool:
    return capability in _string_list(artifact.get("capabilities"))


def _cited_evidence_ids(
    tool_calls: Sequence[Mapping[str, Any]], references: Sequence[Any]
) -> set[str]:
    """只汇总 Plan/升级实际引用的只读调用，未引用的查询不能冒充有效证据。"""

    cited = {value for value in references if isinstance(value, str) and value}
    result: set[str] = set()
    peer_addresses: set[str] = set()
    provider_endpoints: set[str] = set()
    for call in tool_calls:
        if call.get("status") != "succeeded" or call.get("action") != "read":
            continue
        if call.get("call_id") not in cited and call.get("evidence_ref") not in cited:
            continue
        result.update(_string_list(call.get("evidence_ids")))
        data = _list(_dict(call.get("output")).get("data"))
        if call.get("name") == "query_traces":
            for trace in _dict_list(data):
                peer = _dict(trace.get("attributes")).get("peer_address")
                if isinstance(peer, str) and peer.strip():
                    peer_addresses.add(peer.strip())
        elif call.get("name") == "get_providers":
            for provider in _dict_list(data):
                endpoint = provider.get("endpoint")
                if isinstance(endpoint, str) and endpoint.strip():
                    provider_endpoints.add(endpoint.strip())
    # Trace 对端与 Provider 声明端点不一致时，派生“旧地址”这一跨工具证据。
    if provider_endpoints and any(peer not in provider_endpoints for peer in peer_addresses):
        result.add("trace.peer_address_obsolete")
    return result


def _failed_probe_evidence_ids(artifact: Mapping[str, Any]) -> set[str]:
    """提取失败探针之后的新查询证据，并从前后连接快照派生状态变化。"""

    has_failed_probe = any(
        operation.get("kind") == "probe"
        and operation.get("status") == "succeeded"
        and _dict(operation.get("result")).get("outcome") == "hard_stop"
        for operation in _dict_list(artifact.get("operations"))
    )
    if not has_failed_probe:
        return set()

    runs = _dict_list(artifact.get("agent_runs"))
    result: set[str] = set()
    for run in runs[1:]:
        for call in _dict_list(run.get("tool_calls")):
            if call.get("status") == "succeeded" and call.get("action") == "read":
                result.update(_string_list(call.get("evidence_ids")))

    snapshots: List[Mapping[str, Mapping[str, Any]]] = []
    for run in runs:
        for call in _dict_list(run.get("tool_calls")):
            if call.get("name") != "get_connection_metadata" or call.get("status") != "succeeded":
                continue
            data = _list(_dict(call.get("output")).get("data"))
            snapshot = {
                value.get("provider_id"): value
                for value in _dict_list(data)
                if isinstance(value.get("provider_id"), str)
            }
            if snapshot:
                snapshots.append(snapshot)
    for before, after in zip(snapshots, snapshots[1:]):
        for provider_id in set(before) & set(after):
            previous, current = before[provider_id], after[provider_id]
            if previous.get("pool_generation") != current.get("pool_generation"):
                result.add("connection.pool_generation_changed")
            if previous.get("resolver_cache_generation") == current.get("resolver_cache_generation"):
                result.add("connection.resolver_cache_generation_unchanged")
    return result


def _present(value: Any) -> bool:
    if value is None or value is False:
        return False
    if isinstance(value, str):
        return bool(value.strip())
    if isinstance(value, (list, dict)):
        return bool(value)
    return True


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


def _plan_actions(plans: Sequence[Mapping[str, Any]]) -> List[Mapping[str, Any]]:
    """Flatten legacy Plan.actions and dynamic Plan.stages[].actions."""

    result: List[Mapping[str, Any]] = []
    for plan in plans:
        result.extend(_dict_list(plan.get("actions")))
        for stage in _dict_list(plan.get("stages")):
            result.extend(_dict_list(stage.get("actions")))
    return result


def _probe_policy_ids(plans: Sequence[Mapping[str, Any]]) -> List[str]:
    """Return distinct probe policy IDs from either Plan schema generation."""

    result: List[str] = []
    for plan in plans:
        legacy = plan.get("recovery_policy_id")
        if isinstance(legacy, str) and legacy and legacy not in result:
            result.append(legacy)
        for action in _plan_actions([plan]):
            if action.get("tool_name") != "request_probe":
                continue
            policy_id = _dict(action.get("arguments")).get("policy_id")
            if isinstance(policy_id, str) and policy_id and policy_id not in result:
                result.append(policy_id)
    return result


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
