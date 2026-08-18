"""一键 Baseline 严格门禁的测试，重点防止版本混批和旧导出器降级。"""

import json
import tempfile
import unittest
from pathlib import Path

from talon_evaluator.verify import VerificationError, verify_export, verify_report


class VerifyExportTests(unittest.TestCase):
    """验证完整矩阵可通过，而缺少新 capabilities 的 Artifact 会被拒绝。"""

    def test_accepts_exact_versioned_matrix(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self._write_export(directory, ["scenario-a", "scenario-b"], repeat=2)

            count = verify_export(
                directory,
                "talon-toolops-agent/eval-test",
                "toolops-v1",
                ["scenario-a", "scenario-b"],
                2,
            )

            self.assertEqual(4, count)

    def test_rejects_artifact_from_stale_exporter(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self._write_export(directory, ["scenario-a"], repeat=1, capabilities=[])

            with self.assertRaisesRegex(VerificationError, "missing capabilities"):
                verify_export(
                    directory,
                    "talon-toolops-agent/eval-test",
                    "toolops-v1",
                    ["scenario-a"],
                    1,
                )

    def test_rejects_inconsistent_stage_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self._write_export(directory, ["scenario-a"], repeat=1)
            path = directory / "run-1.json"
            payload = json.loads(path.read_text(encoding="utf-8"))
            payload["artifact"]["stage_failures"] = [
                {
                    "stage": "probe",
                    "category": "platform_unavailable",
                    "code": "probe_query_failed",
                    "safe_summary": "暂时无法查询探测 Operation",
                    "next_action": "retry",
                    "retryable": False,
                }
            ]
            self._write_json(path, payload)

            with self.assertRaisesRegex(VerificationError, "retry semantics"):
                verify_export(
                    directory,
                    "talon-toolops-agent/eval-test",
                    "toolops-v1",
                    ["scenario-a"],
                    1,
                )

    def _write_export(self, directory, scenarios, repeat, capabilities=None):
        """生成只包含门禁所需字段的临时版本化导出目录。"""

        if capabilities is None:
            capabilities = [
                "canonical_evidence_ids",
                "evidence_lookup",
                "incident_context_snapshot",
                "per_model_context_snapshot",
                "structured_escalation_handoff",
                "structured_experience",
                "structured_stage_failures",
            ]
        runs = []
        index = 0
        for scenario_id in scenarios:
            for _ in range(repeat):
                index += 1
                run_id = "run-{}".format(index)
                file_name = run_id + ".json"
                runs.append(
                    {
                        "run_id": run_id,
                        "scenario_id": scenario_id,
                        "outcome": "completed",
                        "file": file_name,
                    }
                )
                self._write_json(
                    directory / file_name,
                    {
                        "schema_version": "talon.evaluation-input/v1",
                        "artifact": {
                            "schema_version": "talon.run-artifact/v2",
                            "capabilities": capabilities,
                            "run_id": run_id,
                            "scenario_id": scenario_id,
                            "outcome": "completed",
                            "provenance": {
                                "code_version": "talon-toolops-agent/eval-test",
                                "dataset_version": "toolops-v1",
                                "prompt_version": "toolops-agent/v3",
                                "prompt_digest": "abc123",
                            },
                            "run_config": {"context_version": "talon.incident-context/v1"},
                            "stage_failures": [],
                            "agent_runs": [
                                {
                                    "context_snapshot": {
                                        "schema_version": "talon.incident-context/v1",
                                        "digest": "sha256:" + "a" * 64,
                                        "incident_id": scenario_id,
                                        "objective": "investigate incident",
                                    },
                                    "model_calls": [
                                        {
                                            "context_snapshot": {
                                                "schema_version": "talon.incident-context/v1",
                                                "digest": "sha256:" + "b" * 64,
                                                "incident_id": scenario_id,
                                                "objective": "investigate incident",
                                            }
                                        }
                                    ],
                                }
                            ],
                        },
                        "expectations": {},
                    },
                )
        self._write_json(
            directory / "manifest.json",
            {
                "schema_version": "talon.evaluation-export/v2",
                "artifact_schema_version": "talon.run-artifact/v2",
                "code_version": "talon-toolops-agent/eval-test",
                "dataset_version": "toolops-v1",
                "runs": runs,
            },
        )

    @staticmethod
    def _write_json(path, value):
        path.write_text(json.dumps(value), encoding="utf-8")


class VerifyReportTests(unittest.TestCase):
    """区分允许语义 skipped 的本地报告和必须完整的 Judge 报告。"""

    def test_complete_report_requires_no_skips(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "report.json"
            path.write_text(
                json.dumps(
                    {
                        "schema_version": "talon.evaluation-batch-result/v1",
                        "summary": {
                            "runs": 3,
                            "completed": 3,
                            "failed": 0,
                            "successful_runs": 3,
                            "failed_checks": 0,
                            "skipped_checks": 1,
                            "coverage": 0.95,
                        },
                    }
                ),
                encoding="utf-8",
            )

            verify_report(path, 3, require_complete=False)
            with self.assertRaisesRegex(VerificationError, "skipped checks"):
                verify_report(path, 3, require_complete=True)


if __name__ == "__main__":
    unittest.main()
