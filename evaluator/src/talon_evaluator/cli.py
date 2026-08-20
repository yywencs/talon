"""Talon Evaluator 命令行入口，统一单文件、批次目录和可选 Judge 模式。"""

import argparse
import json
import sys
from pathlib import Path
from typing import Optional, Sequence

from .core import EvaluationInputError, evaluate
from .batch import evaluate_directory
from .judge import JudgeConfig, JudgeError, OpenAICompatibleJudge, evaluate_with_judge


def build_parser() -> argparse.ArgumentParser:
    """声明命令行参数；Judge 配置既可来自 env 文件，也可由参数覆盖。"""

    parser = argparse.ArgumentParser(description="Evaluate a Talon input JSON or export directory")
    parser.add_argument("input", type=Path, help="evaluation input JSON or export directory")
    parser.add_argument("--output", "-o", type=Path, help="write result JSON instead of stdout")
    parser.add_argument("--pretty", action="store_true", help="indent result JSON")
    parser.add_argument("--judge", action="store_true", help="run the versioned LLM Judge")
    parser.add_argument("--env-file", type=Path, default=Path(".env"), help="Judge env file")
    parser.add_argument("--judge-provider", help="override JUDGE_PROVIDER")
    parser.add_argument("--judge-model", help="override JUDGE_MODEL")
    parser.add_argument("--judge-endpoint", help="override JUDGE_ENDPOINT")
    parser.add_argument("--judge-api-key", help="override JUDGE_API_KEY")
    parser.add_argument("--judge-pass-threshold", type=float, help="override JUDGE_PASS_THRESHOLD")
    parser.add_argument("--judge-timeout-seconds", type=float, help="override JUDGE_TIMEOUT_SECONDS")
    parser.add_argument(
        "--judge-concurrency",
        type=int,
        default=1,
        help="parallel Judge evaluations for directory input (default: 1)",
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    """执行评测并返回稳定退出码：0 无失败、1 评测失败、2 基础设施或输入错误。"""

    args = build_parser().parse_args(argv)
    try:
        evaluate_one = evaluate
        if args.judge:
            config = JudgeConfig.load(
                args.env_file,
                {
                    "JUDGE_PROVIDER": args.judge_provider,
                    "JUDGE_MODEL": args.judge_model,
                    "JUDGE_ENDPOINT": args.judge_endpoint,
                    "JUDGE_API_KEY": args.judge_api_key,
                    "JUDGE_PASS_THRESHOLD": args.judge_pass_threshold,
                    "JUDGE_TIMEOUT_SECONDS": args.judge_timeout_seconds,
                },
            )
            judge = OpenAICompatibleJudge(config)
            evaluate_one = lambda payload: evaluate_with_judge(payload, judge)
        # 目录输入需要看所有 Run 是否成功；单文件则直接看该次 verdict。
        if args.input.is_dir():
            result = evaluate_directory(args.input, evaluate_one, concurrency=args.judge_concurrency)
            failed = result["summary"]["successful_runs"] != result["summary"]["runs"]
        else:
            with args.input.open("r", encoding="utf-8") as source:
                payload = json.load(source)
            result = evaluate_one(payload)
            failed = result["summary"]["verdict"] == "failed"
    except (OSError, json.JSONDecodeError, EvaluationInputError, JudgeError) as exc:
        print("talon-evaluator: {}".format(exc), file=sys.stderr)
        return 2

    encoded = json.dumps(
        result,
        ensure_ascii=False,
        indent=2 if args.pretty else None,
        sort_keys=True,
    ) + "\n"
    try:
        if args.output is None:
            sys.stdout.write(encoded)
        else:
            args.output.write_text(encoded, encoding="utf-8")
    except OSError as exc:
        print("talon-evaluator: {}".format(exc), file=sys.stderr)
        return 2
    return 1 if failed else 0
