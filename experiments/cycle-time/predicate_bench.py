#!/usr/bin/env python3
"""Benchmark caller-supplied screenshot conditions using synthetic UI only.

Imports the frozen model_bench fixture generator. No target image is read and
no image is retained. Requires Pillow and a local LM Studio vision model.
"""
import argparse
import hashlib
import json
import platform
import statistics
import time
import urllib.error
from pathlib import Path

from model_bench import DEFAULT_FONT, data_url, fixture, request


SCHEMA = {"type": "object", "properties": {"satisfied": {"type": "boolean"}},
          "required": ["satisfied"], "additionalProperties": False}
INSTRUCTIONS = ("Evaluate the caller's condition against this screenshot. Return only "
                'the JSON object {"satisfied":true} or {"satisfied":false}. '
                "A foreground modal is a separate floating dialog over a dimmed page; "
                "a card inside the full page is not a modal. Read visible text exactly. "
                "Do not explain or reason. Caller condition: ")
CONDITIONS = [
    ("status_complete", "Status is Complete", lambda state: state["status"] == "Complete"),
    ("modal_visible", "A foreground confirmation modal with Continue and Cancel is visible",
     lambda state: state["mode"] == "dialog"),
    ("count_at_least_15", "Completed count is at least 15", lambda state: state["completed"] >= 15),
]


def infer(base, model, jpeg, condition):
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": [
            {"type": "text", "text": INSTRUCTIONS + condition},
            {"type": "image_url", "image_url": {"url": data_url(jpeg)}},
        ]}],
        "temperature": 0, "max_tokens": 32, "stream": False,
        "response_format": {"type": "json_schema", "json_schema": {
            "name": "condition_result", "strict": True, "schema": SCHEMA}},
    }
    started = time.perf_counter()
    try:
        response = request(base, "/v1/chat/completions", payload)
        elapsed = (time.perf_counter() - started) * 1000
        message = response["choices"][0]["message"].get("content", "")
        try:
            parsed = json.loads(message)
        except (json.JSONDecodeError, TypeError):
            parsed = None
        valid = isinstance(parsed, dict) and set(parsed) == {"satisfied"} and type(parsed["satisfied"]) is bool
        return {"wall_ms": round(elapsed, 3), "response": response,
                "valid": valid, "satisfied": parsed["satisfied"] if valid else None}
    except (urllib.error.URLError, TimeoutError) as error:
        detail = error.read().decode(errors="replace") if isinstance(error, urllib.error.HTTPError) else None
        return {"wall_ms": round((time.perf_counter() - started) * 1000, 3),
                "error": str(error), "detail": detail, "valid": False, "satisfied": None}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:11434")
    parser.add_argument("--model", default="qwen3-vl-2b-instruct")
    parser.add_argument("--width", type=int, default=960)
    parser.add_argument("--samples", type=int, default=12)
    parser.add_argument("--font", type=Path, default=Path(DEFAULT_FONT))
    parser.add_argument("--out", type=Path, default=Path("/private/tmp/autopaws-predicate-bench"))
    args = parser.parse_args()
    if args.samples < 1:
        parser.error("--samples must be positive")
    if args.width <= 0 or args.width * 1080 // 1920 <= 0:
        parser.error("--width must produce positive image dimensions")
    if not args.font.is_file():
        parser.error("--font must name an existing TrueType font")
    args.out.mkdir(parents=True, exist_ok=True)
    generator = Path(__file__).with_name("model_bench.py")
    metadata = {
        "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "platform": platform.platform(), "python": platform.python_version(),
        "arguments": vars(args) | {"font": str(args.font), "out": str(args.out)},
        "fixture_generator_sha256": hashlib.sha256(generator.read_bytes()).hexdigest(),
        "instructions": INSTRUCTIONS, "response_schema": SCHEMA,
        "conditions": [{"id": key, "prompt": condition} for key, condition, _ in CONDITIONS],
        "temperature": 0, "max_tokens": 32,
        "notes": ["All images generated in memory; no target captures or retained image files.",
                  "First request per condition reported separately; it is not necessarily cold.",
                  "Warm indices1..samples change each frame; all requests are serial.",
                  "Wall time covers HTTP request through full response; fixture/base64 creation excluded."],
        "model_inventory": request(args.base_url, "/api/v1/models"),
    }
    (args.out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    records = []
    with (args.out / "results.jsonl").open("w") as log:
        for key, condition, expected_fn in CONDITIONS:
            for index in range(args.samples + 1):
                jpeg, state = fixture(index, args.width, str(args.font))
                record = {"condition_id": key, "condition": condition, "fixture_index": index,
                          "phase": "first" if index == 0 else "warm_changing", "width": args.width,
                          "expected": expected_fn(state), "fixture_state": state}
                record.update(infer(args.base_url, args.model, jpeg, condition))
                record["correct"] = record["valid"] and record["satisfied"] == record["expected"]
                records.append(record)
                log.write(json.dumps(record) + "\n")
                log.flush()
                print(json.dumps({k: record[k] for k in ["condition_id", "fixture_index", "phase",
                    "wall_ms", "expected", "satisfied", "valid", "correct"]}), flush=True)
    summary = []
    for key, condition, _ in CONDITIONS:
        warm = [row for row in records if row["condition_id"] == key and row["phase"] == "warm_changing"]
        values = [row["wall_ms"] for row in warm]
        summary.append({"condition_id": key, "condition": condition, "n": len(warm),
                        "median_ms": statistics.median(values), "min_ms": min(values), "max_ms": max(values),
                        "expected_true": sum(row["expected"] for row in warm),
                        "correct": sum(row["correct"] for row in warm),
                        "invalid": sum(not row["valid"] for row in warm),
                        "false_accept": sum(row["valid"] and row["satisfied"] and not row["expected"] for row in warm),
                        "false_reject": sum(row["valid"] and not row["satisfied"] and row["expected"] for row in warm)})
    (args.out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps({"summary": summary}), flush=True)


if __name__ == "__main__":
    main()
