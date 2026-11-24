#!/usr/bin/env python3
"""Run frozen synthetic benchmarks with explicit OpenAI reasoning_effort.

The wrapper records its injected setting separately; model_bench.py and
predicate_bench.py stay unchanged. All requests and benchmark cells are serial.
"""
import argparse
import hashlib
import json
import sys
from pathlib import Path

import model_bench
import predicate_bench


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default="qwen3.8-27b-splash")
    parser.add_argument("--task", choices=["state", "predicate"], required=True)
    parser.add_argument("--widths", nargs="+", type=int, default=[960, 1920])
    parser.add_argument("--samples", type=int, default=5)
    parser.add_argument("--reasoning-effort", default="none")
    parser.add_argument("--fixture-offset", type=int, default=0,
                        help="Use new fixture indices to avoid exact-request cache reuse")
    parser.add_argument("--condition", choices=[key for key, _, _ in predicate_bench.CONDITIONS])
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if args.samples < 1 or any(width <= 0 or width * 1080 // 1920 <= 0 for width in args.widths):
        parser.error("positive samples and image dimensions are required")
    if args.fixture_offset < 0:
        parser.error("--fixture-offset must be nonnegative")
    if args.condition and args.task != "predicate":
        parser.error("--condition applies only to predicate tasks")
    original_request = model_bench.request
    original_fixture = model_bench.fixture

    def offset_fixture(index, width, font_path=model_bench.DEFAULT_FONT):
        return original_fixture(index + args.fixture_offset, width, font_path)

    def configured_request(base, path, body=None, timeout=120):
        if path == "/v1/chat/completions" and body is not None:
            body = dict(body, reasoning_effort=args.reasoning_effort)
        return original_request(base, path, body, timeout)

    model_bench.request = configured_request
    predicate_bench.request = configured_request
    model_bench.fixture = offset_fixture
    predicate_bench.fixture = offset_fixture
    if args.condition:
        predicate_bench.CONDITIONS = [entry for entry in predicate_bench.CONDITIONS if entry[0] == args.condition]
    for width in args.widths:
        out = args.out / str(width)
        out.mkdir(parents=True, exist_ok=True)
        profile = {
            "wrapper": "splash_bench.py",
            "injected_request_fields": {"reasoning_effort": args.reasoning_effort},
            "fixture_index_offset": args.fixture_offset,
            "actual_fixture_indices": list(range(args.fixture_offset, args.fixture_offset + args.samples + 1)),
            "condition_filter": args.condition,
            "source_sha256": {name: hashlib.sha256(Path(__file__).with_name(name).read_bytes()).hexdigest()
                              for name in ["splash_bench.py", "model_bench.py", "predicate_bench.py"]},
            "notes": ["Only generated synthetic fixture images are read.",
                      "State also inherits frozen model_bench chat_template_kwargs.enable_thinking=false.",
                      "Reasoning-off compatibility must be checked in each model's returned content."],
        }
        (out / "request-profile.json").write_text(json.dumps(profile, indent=2) + "\n")
        if args.task == "state":
            sys.argv = ["model_bench.py", "--model", args.model, "--widths", str(width),
                        "--samples", str(args.samples), "--explicit-mode", "--out", str(out)]
            model_bench.main()
        else:
            sys.argv = ["predicate_bench.py", "--model", args.model, "--width", str(width),
                        "--samples", str(args.samples), "--out", str(out)]
            predicate_bench.main()


if __name__ == "__main__":
    main()
