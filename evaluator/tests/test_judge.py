import json
import tempfile
import unittest
import urllib.request
from pathlib import Path

from talon_evaluator.batch import evaluate_directory
from talon_evaluator.judge import (
    JUDGE_VERSION,
    JudgeConfig,
    JudgeError,
    JudgeOutcome,
    OpenAICompatibleJudge,
    _url_opener,
    evaluate_with_judge,
)
from test_evaluator import mapping_input


class FakeJudge:
    def __init__(self, outcome):
        self.config = JudgeConfig(
            provider="openai-compatible",
            model="independent-judge",
            endpoint="http://judge.example/v1/chat/completions",
        )
        self.outcome = outcome
        self.case = None

    def judge_root_cause(self, case):
        self.case = case
        return self.outcome


class FakeHTTPResponse:
    def __init__(self, payload):
        self.payload = json.dumps(payload).encode("utf-8")

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        return False

    def read(self):
        return self.payload


class JudgeTests(unittest.TestCase):
    def test_loopback_judge_bypasses_environment_proxy(self):
        opener = _url_opener("http://127.0.0.1:11434/v1/chat/completions")

        self.assertNotEqual(urllib.request.urlopen, opener)

    def test_judge_replaces_semantic_skip_and_records_provenance(self):
        judge = FakeJudge(
            JudgeOutcome(
                passed=True,
                score=0.95,
                reason="The same mapping type regression is identified.",
                prompt_tokens=100,
                completion_tokens=20,
                total_tokens=120,
                duration_ms=42.5,
            )
        )

        result = evaluate_with_judge(mapping_input(), judge)

        checks = {item["id"]: item for item in result["checks"]}
        self.assertEqual("passed", checks["diagnosis.root_cause_correctness"]["status"])
        self.assertEqual("passed", result["summary"]["verdict"])
        self.assertEqual(1.0, result["summary"]["coverage"])
        self.assertEqual(JUDGE_VERSION, result["judge"]["judge_version"])
        self.assertEqual("root-cause/v1", result["judge"]["prompt_version"])
        self.assertEqual("independent-judge", result["judge"]["model"])
        self.assertFalse(result["judge"]["same_as_agent_model"])
        self.assertEqual(120, result["judge"]["usage"]["total_tokens"])
        self.assertEqual(
            ["mapping-v2 changed size to a string"], judge.case["agent_root_causes"]
        )

    def test_threshold_can_fail_an_otherwise_positive_judgment(self):
        judge = FakeJudge(
            JudgeOutcome(
                passed=True,
                score=0.7,
                reason="Related but missing a material mechanism.",
                prompt_tokens=0,
                completion_tokens=0,
                total_tokens=0,
                duration_ms=1.0,
            )
        )

        result = evaluate_with_judge(mapping_input(), judge)

        checks = {item["id"]: item for item in result["checks"]}
        self.assertEqual("failed", checks["diagnosis.root_cause_correctness"]["status"])
        self.assertEqual("failed", result["summary"]["verdict"])

    def test_openai_compatible_client_parses_strict_result_and_usage(self):
        captured = {}

        def opener(request, timeout):
            captured["authorization"] = request.get_header("Authorization")
            captured["body"] = json.loads(request.data.decode("utf-8"))
            captured["timeout"] = timeout
            return FakeHTTPResponse(
                {
                    "choices": [
                        {
                            "message": {
                                "content": '```json\n{"passed": true, "score": 0.9, "reason": "supported"}\n```'
                            }
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 10,
                        "completion_tokens": 5,
                        "total_tokens": 15,
                    },
                }
            )

        config = JudgeConfig(
            provider="openai",
            model="judge-model",
            endpoint="https://api.example/v1/chat/completions",
            api_key="secret-token",
            timeout_seconds=9,
        )
        outcome = OpenAICompatibleJudge(config, opener=opener).judge_root_cause(
            {"agent_root_causes": ["cause"]}
        )

        self.assertTrue(outcome.passed)
        self.assertEqual(0.9, outcome.score)
        self.assertEqual(15, outcome.total_tokens)
        self.assertEqual("Bearer secret-token", captured["authorization"])
        self.assertEqual(0, captured["body"]["temperature"])
        self.assertEqual(9, captured["timeout"])

    def test_config_requires_separate_explicit_model(self):
        with self.assertRaisesRegex(JudgeError, "JUDGE_MODEL"):
            JudgeConfig.load(Path("/path/that/does/not/exist"), {})

    def test_invalid_model_output_is_infrastructure_error(self):
        def opener(request, timeout):
            return FakeHTTPResponse(
                {"choices": [{"message": {"content": "not json"}}]}
            )

        config = JudgeConfig(
            provider="openai-compatible",
            model="judge-model",
            endpoint="http://judge.example/v1/chat/completions",
        )
        with self.assertRaises(JudgeError):
            OpenAICompatibleJudge(config, opener=opener).judge_root_cause({})

    def test_batch_retains_judge_check_reason(self):
        payload = mapping_input()
        judge = FakeJudge(
            JudgeOutcome(
                passed=True,
                score=0.9,
                reason="auditable root cause verdict",
                prompt_tokens=1,
                completion_tokens=1,
                total_tokens=2,
                duration_ms=3.0,
            )
        )
        with tempfile.TemporaryDirectory() as directory_text:
            directory = Path(directory_text)
            (directory / "run-001.json").write_text(json.dumps(payload), encoding="utf-8")
            (directory / "manifest.json").write_text(
                json.dumps(
                    {
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
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )

            result = evaluate_directory(
                directory, lambda item: evaluate_with_judge(item, judge)
            )

        self.assertEqual(
            "auditable root cause verdict",
            result["runs"][0]["judge_checks"][0]["actual"]["reason"],
        )


if __name__ == "__main__":
    unittest.main()
