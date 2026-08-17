import argparse
import json
import sys
from pathlib import Path
from typing import Optional, Sequence

from .core import EvaluationInputError, evaluate
from .batch import evaluate_directory


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Evaluate a Talon input JSON or export directory")
    parser.add_argument("input", type=Path, help="evaluation input JSON or export directory")
    parser.add_argument("--output", "-o", type=Path, help="write result JSON instead of stdout")
    parser.add_argument("--pretty", action="store_true", help="indent result JSON")
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.input.is_dir():
            result = evaluate_directory(args.input)
            failed = result["summary"]["successful_runs"] != result["summary"]["runs"]
        else:
            with args.input.open("r", encoding="utf-8") as source:
                payload = json.load(source)
            result = evaluate(payload)
            failed = result["summary"]["verdict"] == "failed"
    except (OSError, json.JSONDecodeError, EvaluationInputError) as exc:
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
