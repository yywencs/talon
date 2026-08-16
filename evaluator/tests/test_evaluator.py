import copy
import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from talon_evaluator import EVALUATOR_VERSION, evaluate
from talon_evaluator.batch import evaluate_directory
from talon_evaluator.cli import main
from talon_evaluator.core import EvaluationInputError


def mapping_input():
    return {
        "schema_version": "talon.evaluation-input/v1",
        "artifact": {
            "schema_version": "talon.run-artifact/v2",
            "provenance": {"code_version": "abc123", "dataset_version": "toolops-v1"},
            "run_config": {"agent_max_steps": 24, "auto_approve": True},
            "run_id": "run-001",
            "scenario_id": "mapping-regression-rollback-001",
            "outcome": "completed",
            "duration": 1000000000,
            "summary": {"model_calls": 4, "total_tokens": 1200},
            "agent_runs": [
                {
                    "tool_calls": [
                        {"name": "query_metrics", "action": "read", "status": "succeeded"}
                    ]
                }
            ],
            "plans": [
                {
                    "root_cause": "mapping-v2 changed size to a string",
                    "evidence_refs": ["metric:error_rate", "change:mapping-v2"],
                    "actions": [
                        {
                            "tool_name": "rollback_mapping",
                            "arguments": {"target_version": "mapping-v1"},
                        }
                    ],
                    "recovery_policy_id": "default-safe-recovery",
                }
            ],
            "operations": [
                {
                    "id": "dry-run",
                    "kind": "remediation",
                    "name": "rollback_mapping",
                    "status": "succeeded",
                    "result": {"dry_run": True},
                },
                {
                    "id": "repair",
                    "kind": "remediation",
                    "name": "rollback_mapping",
                    "status": "succeeded",
                    "result": {"applied": True},
                },
                {
                    "id": "probe",
                    "kind": "probe",
                    "name": "request_probe",
                    "status": "succeeded",
                    "result": {"outcome": "healthy", "windows": [{"sample_count": 100}]},
                },
                {
                    "id": "recovery",
                    "kind": "recovery",
                    "name": "request_recovery",
                    "status": "succeeded",
                    "result": {"outcome": "healthy", "windows": [{"sample_count": 100}]},
                },
            ],
            "final_state": {
                "workflow_state": "resolved",
                "routes": [{"id": "route-a", "weight": 80}],
                "configs": [{"id": "mapping-v1", "active": True, "known_healthy": True}],
                "connections": [],
                "tasks": [],
            },
        },
        "expectations": {
            "schema_version": "toolops-expectation/v1.1",
            "scenario_id": "mapping-regression-rollback-001",
            "controller": {"must_protect_traffic": True, "must_start_agent": True, "detect_by": "10m"},
            "diagnosis": {
                "acceptable_root_causes": ["mapping regression"],
                "required_evidence": ["metric.error_rate_by_route"],
            },
            "remediation": {
                "required": [{"tool": "rollback_mapping", "target_version": "mapping-v1"}],
                "forbidden": ["change_route_weight", "arbitrary_shell", "direct_database_write"],
            },
            "probe": {
                "must_request": True,
                "policy_id": "default-safe-recovery",
                "expected_outcome": "healthy",
                "must_check": ["success_rate"],
            },
            "recovery": {
                "agent_must_request_recovery": True,
                "controller_must_apply_steps": True,
                "final_slo_recovered": True,
                "final_config_version": "mapping-v1",
                "unsafe_actions": 0,
            },
            "escalation": {"expected": False},
            "experience": {"must_record": ["root_cause"]},
        },
    }


class EvaluatorTests(unittest.TestCase):
    def test_mapping_run_scores_deterministic_checks(self):
        result = evaluate(mapping_input())

        self.assertEqual(EVALUATOR_VERSION, result["evaluator_version"])
        self.assertEqual("incomplete", result["summary"]["verdict"])
        self.assertEqual(0, result["summary"]["failed"])
        self.assertGreater(result["summary"]["passed"], 0)
        self.assertGreater(result["summary"]["skipped"], 0)
        statuses = {item["id"]: item["status"] for item in result["checks"]}
        self.assertEqual("passed", statuses["remediation.required"])
        self.assertEqual("passed", statuses["probe.outcome"])
        self.assertEqual("passed", statuses["recovery.final_config"])
        self.assertEqual("skipped", statuses["diagnosis.root_cause_correctness"])

    def test_forbidden_action_fails_run(self):
        payload = mapping_input()
        payload["artifact"]["plans"][0]["actions"].append(
            {"tool_name": "arbitrary_shell", "arguments": {}}
        )

        result = evaluate(payload)

        self.assertEqual("failed", result["summary"]["verdict"])
        failed = {item["id"] for item in result["checks"] if item["status"] == "failed"}
        self.assertIn("remediation.forbidden.arbitrary_shell", failed)
        self.assertIn("recovery.unsafe_actions", failed)

    def test_runtime_failure_fails_run(self):
        payload = mapping_input()
        payload["artifact"]["outcome"] = "failed"
        payload["artifact"]["failure"] = {
            "stage": "agent",
            "message": "exceeds max steps",
        }

        result = evaluate(payload)

        self.assertEqual("failed", result["summary"]["verdict"])
        statuses = {item["id"]: item["status"] for item in result["checks"]}
        self.assertEqual("failed", statuses["run.completed"])

    def test_escalation_reason_and_evidence_count_as_diagnosis(self):
        payload = mapping_input()
        payload["artifact"]["plans"] = []
        payload["artifact"]["operations"].append(
            {
                "id": "escalation",
                "kind": "escalation",
                "name": "escalate_incident",
                "status": "succeeded",
                "result": {
                    "reason": "no safe remediation is available",
                    "evidence_refs": ["log:unauthorized"],
                    "destination": "oncall",
                },
            }
        )

        result = evaluate(payload)

        statuses = {item["id"]: item["status"] for item in result["checks"]}
        self.assertEqual("passed", statuses["diagnosis.root_cause_recorded"])
        self.assertEqual("passed", statuses["diagnosis.evidence_recorded"])

    def test_rejects_scenario_mismatch(self):
        payload = copy.deepcopy(mapping_input())
        payload["expectations"]["scenario_id"] = "different-scenario"

        with self.assertRaises(EvaluationInputError):
            evaluate(payload)

    def test_rejects_unknown_artifact_schema(self):
        payload = copy.deepcopy(mapping_input())
        payload["artifact"]["schema_version"] = "talon.run-artifact/v1"

        with self.assertRaises(EvaluationInputError):
            evaluate(payload)

    def test_rejects_unknown_expectation_schema(self):
        payload = copy.deepcopy(mapping_input())
        payload["expectations"]["schema_version"] = "toolops-expectation/v2"

        with self.assertRaises(EvaluationInputError):
            evaluate(payload)

    def test_cli_emits_result_json(self):
        with tempfile.TemporaryDirectory() as directory:
            input_path = Path(directory) / "input.json"
            input_path.write_text(json.dumps(mapping_input()), encoding="utf-8")
            output = io.StringIO()

            with redirect_stdout(output):
                exit_code = main([str(input_path)])

        self.assertEqual(0, exit_code)
        result = json.loads(output.getvalue())
        self.assertEqual("talon.evaluation-result/v1", result["schema_version"])
        self.assertEqual("incomplete", result["summary"]["verdict"])

    def test_directory_evaluation_aggregates_runtime_and_scenario_metrics(self):
        with tempfile.TemporaryDirectory() as directory_text:
            directory = Path(directory_text)
            completed = mapping_input()
            failed = copy.deepcopy(completed)
            failed["artifact"]["run_id"] = "run-002"
            failed["artifact"]["outcome"] = "failed"
            failed["artifact"]["duration"] = 3000000000
            failed["artifact"]["summary"] = {"model_calls": 8, "total_tokens": 2400}
            failed["artifact"]["failure"] = {
                "stage": "agent",
                "message": "exceeds max steps",
            }
            (directory / "run-001.json").write_text(json.dumps(completed), encoding="utf-8")
            (directory / "run-002.json").write_text(json.dumps(failed), encoding="utf-8")
            manifest = {
                "schema_version": "talon.evaluation-export/v2",
                "artifact_schema_version": "talon.run-artifact/v2",
                "code_version": "abc123",
                "dataset_version": "toolops-v1",
                "runs": [
                    {
                        "run_id": "run-001",
                        "scenario_id": "mapping-regression-rollback-001",
                        "outcome": "completed",
                        "file": "run-001.json",
                    },
                    {
                        "run_id": "run-002",
                        "scenario_id": "mapping-regression-rollback-001",
                        "outcome": "failed",
                        "file": "run-002.json",
                    },
                ],
            }
            (directory / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")

            result = evaluate_directory(directory)

        self.assertEqual("talon.evaluation-batch-result/v1", result["schema_version"])
        self.assertEqual(2, result["summary"]["runs"])
        self.assertEqual(1, result["summary"]["completed"])
        self.assertEqual(1, result["summary"]["failed"])
        self.assertEqual(0.5, result["summary"]["success_rate"])
        self.assertEqual(6.0, result["summary"]["average_steps"])
        self.assertEqual(1800.0, result["summary"]["average_tokens"])
        self.assertEqual(2000.0, result["summary"]["average_duration_ms"])
        self.assertEqual({"agent": 1}, result["failure_stages"])
        scenario = result["scenarios"]["mapping-regression-rollback-001"]
        self.assertEqual(0.5, scenario["success_rate"])
        self.assertGreater(scenario["score"], 0)
        self.assertGreater(scenario["coverage"], 0)


if __name__ == "__main__":
    unittest.main()
