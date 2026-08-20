#!/usr/bin/env python3
"""把 RunArtifact JSON 压缩成可读的执行剧本，用于人工复盘与学习。

用法:
    python3 scripts/inspect_artifact.py <artifact-or-export.json>     # 完整剧本
    python3 scripts/inspect_artifact.py <file.json> --model-call 3    # 只看第 3 次模型调用的上下文快照

输入既可以是导出的 evaluation input（{"artifact": ...}），也可以是裸 RunArtifact。
"""

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, List, Mapping


def mapping(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def short(value: Any, limit: int = 70) -> str:
    text = str(value).replace("\n", " ")
    return text if len(text) <= limit else text[: limit - 1] + "…"


def load(path: Path) -> Mapping[str, Any]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        sys.exit("read {}: {}".format(path, exc))
    artifact = payload.get("artifact") if isinstance(payload, Mapping) else None
    return mapping(artifact if artifact is not None else payload)


def header(art: Mapping[str, Any]) -> None:
    summary = mapping(art.get("summary"))
    provenance = mapping(art.get("provenance"))
    duration_ms = float(art.get("duration", 0)) / 1_000_000
    print("== 概要 ==")
    print("  run_id     :", art.get("run_id", ""))
    print("  scenario   :", art.get("scenario_id", ""))
    print("  outcome    : {}  stop_reason: {}".format(art.get("outcome", ""), art.get("stop_reason", "")))
    print("  code       :", provenance.get("code_version", ""))
    print("  prompt     : {} ({})".format(provenance.get("prompt_version", ""), provenance.get("prompt_digest", "")))
    print("  调用统计    : {} 次模型 / {} 次工具 / {} tokens / {:.0f}s".format(
        summary.get("model_calls", 0), summary.get("tool_calls", 0),
        summary.get("total_tokens", 0), duration_ms / 1000))


def transitions(art: Mapping[str, Any]) -> None:
    print("\n== 状态转移线 ==")
    for item in art.get("workflow_history", []):
        item = mapping(item)
        print("  v{:<2} {:>14} → {:<14} {} [{}] {}".format(
            item.get("version", ""), item.get("from", ""), item.get("to", ""),
            item.get("event", ""), item.get("actor", ""), short(item.get("reason", ""), 50)))


def intents(art: Mapping[str, Any]) -> None:
    items = art.get("execution_intents") or []
    if items:
        print("\n== Execution Intents ==")
    for item in items:
        item = mapping(item)
        print("  ● {} — {}".format(item.get("id", ""), short(item.get("summary", ""))))
        print("    root_cause: {}".format(short(item.get("root_cause", ""), 90)))
        print("    evidence  : {}".format(short(", ".join(item.get("evidence_refs", [])))))
        for stage in item.get("stages", []):
            stage = mapping(stage)
            actions = ", ".join(
                "{}({})".format(mapping(a).get("tool_name", ""), mapping(a).get("id", ""))
                for a in stage.get("actions", [])
            )
            policy = mapping(stage.get("checkpoint_policy"))
            print("    Stage {} [{}]: {}".format(stage.get("sequence", ""), stage.get("stage_id", ""), actions))
            print("      checkpoint default: {}".format(policy.get("default_decision", "")))
            for rule in policy.get("rules", []):
                rule = mapping(rule)
                print("      rule: {} == {} → {}{}".format(
                    rule.get("output_path", ""), rule.get("equals", ""), rule.get("decision", ""),
                    " (→{})".format(rule.get("next_stage_id")) if rule.get("next_stage_id") else ""))
    # legacy v2：扁平 actions + probe/recovery 参数，没有 Stage 与 checkpoint_policy。
    legacy = art.get("plans") or []
    if legacy:
        print("\n== Execution Plans（v2 legacy）==")
    for item in legacy:
        item = mapping(item)
        print("  ● {} — {}".format(item.get("id", ""), short(item.get("summary", ""))))
        print("    root_cause: {}".format(short(item.get("root_cause", ""), 90)))
        print("    evidence  : {}".format(short(", ".join(item.get("evidence_refs", [])))))
        actions = ", ".join(
            "{}({})".format(mapping(a).get("tool_name", ""), mapping(a).get("id", ""))
            for a in item.get("actions", [])
        )
        print("    actions(扁平): {}".format(actions))
        if item.get("probe_route_id"):
            print("    probe_route_id: {}  recovery_policy_id: {}".format(
                item.get("probe_route_id"), item.get("recovery_policy_id")))


def agent_runs(art: Mapping[str, Any]) -> None:
    print("\n== Agent 轮次与工具调用 ==")
    for run in art.get("agent_runs", []):
        run = mapping(run)
        print("  ▶ Agent Run #{}  {}→{}  ({} 次模型调用)".format(
            run.get("sequence", ""), mapping(run.get("initial_state")).get("state", ""),
            mapping(run.get("final_state")).get("state", ""), len(run.get("model_calls", []))))
        print("    指令: {}".format(short(run.get("instruction", ""), 80)))
        for call in run.get("tool_calls", []):
            call = mapping(call)
            marker = ""
            if call.get("status") == "succeeded" and call.get("is_new_evidence"):
                marker = "  🧾" + short(",".join(call.get("evidence_ids", [])), 40)
            elif call.get("status") != "succeeded":
                marker = "  ✗" + str(call.get("status", ""))
            print("    {:>3}. {:<32} [{}]{}".format(
                call.get("sequence", ""), call.get("name", ""), call.get("action", ""), marker))


def operations(art: Mapping[str, Any]) -> None:
    print("\n== 平台操作 ==")
    for item in art.get("operations", []):
        item = mapping(item)
        result = mapping(item.get("result"))
        extra = " outcome={}".format(result.get("outcome")) if result.get("outcome") else ""
        dry = " (dry_run)" if result.get("dry_run") else ""
        print("  {:<14} {:<36} → {}{}{}".format(
            item.get("kind", ""), item.get("name", ""), item.get("status", ""), dry, extra))


def checkpoints(art: Mapping[str, Any]) -> None:
    items = art.get("decision_checkpoints") or []
    if not items:
        return
    print("\n== 检查点判定 ==")
    for item in items:
        item = mapping(item)
        print("  Stage {} [{}] → {}  {}".format(
            item.get("stage_id", ""), item.get("trigger", ""), item.get("decision", ""),
            short(item.get("decision_reason", ""), 70)))


def failures(art: Mapping[str, Any]) -> None:
    items = art.get("stage_failures", [])
    if not items:
        return
    print("\n== 结构化失败 ==")
    for item in items:
        item = mapping(item)
        print("  {}/{} [{}] → {}  {}".format(
            item.get("stage", ""), item.get("category", ""), item.get("code", ""),
            item.get("next_action", ""), short(item.get("safe_summary", ""), 60)))


def final_state(art: Mapping[str, Any]) -> None:
    state = mapping(art.get("final_state"))
    print("\n== 终态 ==")
    for route in state.get("routes", []):
        route = mapping(route)
        print("  路由 {:<14} weight {}/{} enabled={}".format(
            route.get("id", ""), route.get("weight", 0), route.get("baseline_weight", 0), route.get("enabled")))
    for conn in state.get("connections", []):
        conn = mapping(conn)
        print("  连接 {:<22} pool={} resolver={} ip={}".format(
            conn.get("provider_id", ""), conn.get("pool_generation", ""), conn.get("resolver_cache_generation", ""), conn.get("resolved_ip", "")))
    for cfg in state.get("configs", []):
        cfg = mapping(cfg)
        print("  配置 version={} active={}".format(cfg.get("version", ""), cfg.get("active", "")))
    fields = mapping(art.get("experience")).get("fields", [])
    if fields:
        print("  经验字段: {}".format(", ".join(fields)))


def dump_model_call(art: Mapping[str, Any], index: int) -> None:
    calls: List[Mapping[str, Any]] = [
        mapping(call)
        for run in art.get("agent_runs", [])
        for call in mapping(run).get("model_calls", [])
    ]
    if index < 1 or index > len(calls):
        sys.exit("model call index out of range: 1..{}".format(len(calls)))
    call = calls[index - 1]
    print(json.dumps(call, ensure_ascii=False, indent=2))


def main() -> int:
    parser = argparse.ArgumentParser(description="Summarize a Talon RunArtifact as a readable storyline")
    parser.add_argument("path", type=Path, help="artifact JSON or exported evaluation input")
    parser.add_argument("--model-call", type=int, help="dump the Nth model call (context snapshot) instead")
    args = parser.parse_args()
    art = load(args.path)
    if args.model_call:
        dump_model_call(art, args.model_call)
        return 0
    header(art)
    transitions(art)
    intents(art)
    agent_runs(art)
    operations(art)
    checkpoints(art)
    failures(art)
    final_state(art)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
