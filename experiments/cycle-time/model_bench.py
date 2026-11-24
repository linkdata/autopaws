#!/usr/bin/env python3
"""Synthetic-only local VLM latency experiment; never reads target captures.

Requires Pillow; run with the Python that has PIL installed. Model requests are
serial. Output contains generated fixture images and JSONL responses only.
"""
import argparse
import base64
import io
import json
import platform
import statistics
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont


DEFAULT_FONT = "/System/Library/Fonts/Supplemental/Arial.ttf"


SCHEMA = {
    "type": "object",
    "properties": {
        "mode": {"type": "string", "enum": ["dashboard", "dialog"]},
        "status": {"type": "string", "enum": ["Ready", "Working", "Complete"]},
        "completed": {"type": "integer"},
        "primary_button": {"type": "string"},
    },
    "required": ["mode", "status", "completed", "primary_button"],
    "additionalProperties": False,
}


def request(base, path, body=None, timeout=120):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        return json.load(response)


def fixture(index, width, font_path=DEFAULT_FONT):
    """A reproducible invented dashboard, unrelated to the connected machine."""
    image = Image.new("RGB", (1920, 1080), "#ecf0f5")
    draw = ImageDraw.Draw(image)
    def font(size):
        return ImageFont.truetype(font_path, size)
    def text(x, y, content, size=36, fill="#152133"):
        draw.text((x, y), content, font=font(size), fill=fill)
    draw.rectangle((0, 0, 1920, 96), fill="#193651")
    text(44, 24, "Autopaws Synthetic Workbench", 40, "white")
    draw.rounded_rectangle((44, 135, 1876, 1035), radius=16, fill="white")
    text(90, 170, "Batch processing", 54)
    text(90, 262, "Generated fixture - no captured screen data", 30, "#526179")
    status = ["Ready", "Working", "Complete"][index % 3]
    completed = 10 + index
    primary = {"Ready": "Start", "Working": "Pause", "Complete": "Restart"}[status]
    text(90, 360, "Status: " + status, 44)
    text(90, 442, "Completed: " + str(completed), 44)
    text(90, 560, "Queue", 36)
    for row in range(3):
        y = 620 + row * 78
        draw.rectangle((90, y, 1775, y + 62), fill="#f0f3f8")
        text(110, y + 10, "Synthetic job " + str(row + 1), 30)
        text(1300, y + 10, ["Processed", "Pending", "Pending"][row], 30)
    draw.rounded_rectangle((1450, 900, 1775, 985), radius=12, fill="#176bc0")
    text(1490, 919, primary, 38, "white")
    mode = "dashboard"
    if index % 4 == 3:
        overlay = Image.new("RGBA", image.size, (0, 0, 0, 95))
        image = Image.alpha_composite(image.convert("RGBA"), overlay).convert("RGB")
        draw = ImageDraw.Draw(image)
        draw.rounded_rectangle((540, 320, 1380, 760), radius=18, fill="white")
        text(600, 375, "Confirm action", 50)
        text(600, 470, "Continue processing this batch?", 38)
        draw.rounded_rectangle((1030, 615, 1310, 700), radius=10, fill="#176bc0")
        text(1060, 633, "Continue", 38, "white")
        text(650, 633, "Cancel", 38)
        primary, mode = "Continue", "dialog"
    if width != 1920:
        image = image.resize((width, width * 1080 // 1920), Image.Resampling.LANCZOS)
    output = io.BytesIO()
    image.save(output, "JPEG", quality=90)
    return output.getvalue(), {"mode": mode, "status": status,
                               "completed": completed, "primary_button": primary}


def data_url(data):
    return "data:image/jpeg;base64," + base64.b64encode(data).decode()


def model_request(base, model, data, *, prior=None, binary=False, native=False, explicit_mode=False):
    if binary:
        schema = {"type": "object", "properties": {"changed": {"type": "boolean"}},
                  "required": ["changed"], "additionalProperties": False}
        prompt = "Compare these two screenshots. Did any UI content change? Return only the JSON object."
        images = [prior, data]
    else:
        schema = SCHEMA
        prompt = ("Read this synthetic UI. Return only a JSON object with mode (dashboard or dialog), "
                  "status (the Status field), completed (the Completed number), and primary_button "
                  "(the filled blue button label, using the dialog button if a dialog is open). "
                  "Read visible text exactly. Do not reason or explain.")
        if explicit_mode:
            prompt += (" A dashboard is the full page with cards and no floating modal. A dialog is "
                       "a floating foreground modal with Continue and Cancel buttons over a dimmed "
                       "background. A white card inside a dashboard does not count as a dialog.")
        images = [data]
    content = [{"type": "text", "text": prompt}]
    content.extend({"type": "image_url", "image_url": {"url": data_url(img)}} for img in images)
    payload = {
        "model": model,
        "messages": [{"role": "system", "content": "You extract UI fields. Output JSON only, without reasoning."},
                     {"role": "user", "content": content}],
        "temperature": 0,
        "max_tokens": 96,
        "reasoning_effort": "none",
        "chat_template_kwargs": {"enable_thinking": False},
        "stream": False,
        "response_format": {"type": "json_schema", "json_schema": {
            "name": "ui_change" if binary else "ui_state", "strict": True, "schema": schema}},
    }
    path = "/v1/chat/completions"
    if native:
        payload = {"model": model, "input": [{"type": "text", "content": prompt + " JSON schema: " + json.dumps(schema)}]
                   + [{"type": "image", "data_url": data_url(img)} for img in images],
                   "system_prompt": "You extract UI fields. Output JSON only, without reasoning.",
                   "reasoning": "off", "store": False, "temperature": 0, "max_output_tokens": 96}
        path = "/api/v1/chat"
    started = time.perf_counter()
    try:
        result = request(base, path, payload)
        elapsed = time.perf_counter() - started
        content = ("".join(item.get("content", "") for item in result["output"] if item["type"] == "message")
                   if native else result["choices"][0]["message"].get("content", ""))
        channel = "content"
        if not native and not content:
            content = result["choices"][0]["message"].get("reasoning_content", "")
            channel = "reasoning_content"
        try:
            parsed = json.loads(content)
        except (json.JSONDecodeError, TypeError):
            parsed = None
        return {"wall_ms": round(elapsed * 1000, 3), "response": result, "parsed": parsed, "json_channel": channel}
    except (urllib.error.URLError, TimeoutError) as error:
        detail = error.read().decode(errors="replace") if isinstance(error, urllib.error.HTTPError) else None
        return {"wall_ms": round((time.perf_counter() - started) * 1000, 3), "error": str(error), "detail": detail}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:11434")
    parser.add_argument("--model", required=True)
    parser.add_argument("--widths", type=int, nargs="+", default=[960, 1920])
    parser.add_argument("--samples", type=int, default=8)
    parser.add_argument("--binary", action="store_true")
    parser.add_argument("--ocr", action="store_true")
    parser.add_argument("--native", action="store_true", help="Native API with reasoning off, prompted JSON (no constrained schema)")
    parser.add_argument("--explicit-mode", action="store_true", help="Define dashboard vs modal explicitly; separate prompt variant")
    parser.add_argument("--font", type=Path, default=Path(DEFAULT_FONT), help="TrueType font; default is macOS Arial")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if args.samples < 0:
        parser.error("--samples must be nonnegative")
    if any(width <= 0 or width * 1080 // 1920 <= 0 for width in args.widths):
        parser.error("--widths must produce positive image dimensions")
    if not args.font.is_file():
        parser.error("--font must name an existing TrueType font")
    args.out.mkdir(parents=True, exist_ok=True)
    metadata = {"platform": platform.platform(), "python": platform.python_version(),
                "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "arguments": vars(args) | {"out": str(args.out), "font": str(args.font)},
                "model_inventory": request(args.base_url, "/api/v1/models")}
    (args.out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    records = []
    with (args.out / "results.jsonl").open("w") as log:
        for width in args.widths:
            # First request is reported separately, never mixed with warm samples.
            for sample in range(args.samples + 1):
                data, expected = fixture(sample, width, str(args.font))
                (args.out / f"synthetic-{width}-{sample:02d}.jpg").write_bytes(data)
                prior, _ = fixture(sample - 1 if sample % 2 else sample, width, str(args.font))
                record = {"model": args.model, "width": width, "sample": sample,
                          "phase": "first" if sample == 0 else "warm_changing",
                          "prompt_variant": "explicit_mode" if args.explicit_mode else "initial",
                          "kind": "binary_two_image" if args.binary else "state_json",
                          "expected": {"changed": bool(sample % 2)} if args.binary else expected}
                record.update(model_request(args.base_url, args.model, data, prior=prior, binary=args.binary,
                                            native=args.native, explicit_mode=args.explicit_mode))
                record["correct"] = record.get("parsed") == record["expected"]
                record["final_content_correct"] = record["correct"] and record.get("json_channel") == "content"
                if args.ocr:
                    begin = time.perf_counter()
                    image = Image.open(io.BytesIO(data)).convert("L")
                    image = image.resize((image.width * 2, image.height * 2), Image.Resampling.NEAREST)
                    buf = io.BytesIO()
                    image.save(buf, "PNG")
                    ocr = subprocess.run(["tesseract", "stdin", "stdout", "-l", "eng", "--psm", "6", "--oem", "3"],
                                         input=buf.getvalue(), capture_output=True, timeout=20)
                    record["ocr"] = {"wall_ms": round((time.perf_counter() - begin) * 1000, 3),
                                     "returncode": ocr.returncode, "text": ocr.stdout.decode(errors="replace")}
                records.append(record)
                log.write(json.dumps(record) + "\n")
                log.flush()
                print(json.dumps({k: record[k] for k in ["model", "width", "sample", "phase", "kind", "wall_ms", "correct"]}
                                 | {"parsed": record.get("parsed"), "json_channel": record.get("json_channel"), "error": record.get("error")}), flush=True)
    summary = []
    for width in args.widths:
        warm = [r for r in records if r["width"] == width and r["phase"] == "warm_changing"]
        if warm:
            values = [r["wall_ms"] for r in warm]
            cell = {"width": width, "n": len(warm), "median_ms": statistics.median(values),
                    "min_ms": min(values), "max_ms": max(values),
                    "correct": sum(r["correct"] for r in warm),
                    "final_content_correct": sum(r["final_content_correct"] for r in warm)}
            if args.ocr:
                cell["ocr_median_ms"] = statistics.median(r["ocr"]["wall_ms"] for r in warm)
            summary.append(cell)
    (args.out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps({"summary": summary}), flush=True)


if __name__ == "__main__":
    main()
