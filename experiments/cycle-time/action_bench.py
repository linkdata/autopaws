#!/usr/bin/env python3
"""Measure transient input-to-observed-change; retain only actions and timings.

Uses Ctrl+Escape / Escape pairs and alternating relative pointer movements.
Run only with one operator and an owner-prepared read-only target. No commands,
text, files, settings, screenshots, or OCR are written on the target or retained.
"""

import argparse
import base64
import io
import json
from pathlib import Path
import socket
import time

import numpy as np
from PIL import Image

from kvm_bench import API, MAX_MESSAGE, MCP, summarize


def call(api, method, params):
    request = {"id": 1, "method": method, "params": params}
    if api.token:
        request["token"] = api.token
    with socket.create_connection(api.address, api.timeout) as connection:
        connection.sendall(json.dumps(request).encode() + b"\n")
        with connection.makefile("rb") as reader:
            line = reader.readline(MAX_MESSAGE + 1)
    if len(line) > MAX_MESSAGE or not line.endswith(b"\n"):
        raise ValueError("invalid API framing")
    result = json.loads(line)
    if result.get("ok") is not True:
        raise RuntimeError("input API failure")


def gray(encoded):
    with Image.open(io.BytesIO(base64.b64decode(encoded, validate=True))) as image:
        return np.asarray(image.convert("L").resize((480, 270), Image.Resampling.BILINEAR)).astype(np.int16)


class Backend:
    def __init__(self, api, mcp):
        self.api, self.mcp = api, mcp

    def input(self, method, params):
        if self.mcp is None:
            call(self.api, method, params)
        else:
            result = self.mcp.request("tools/call", {"name": method, "arguments": params})
            if result.get("isError"):
                raise RuntimeError("input MCP failure")

    def capture(self):
        if self.mcp is None:
            return gray(self.api.call("capture_frame")["jpeg_b64"])
        result = self.mcp.request("tools/call", {"name": "capture_screen", "arguments": {}})
        if result.get("isError"):
            raise RuntimeError("capture MCP failure")
        images = [item for item in result.get("content", []) if item.get("type") == "image"]
        if len(images) != 1:
            raise ValueError("missing MCP image")
        return gray(images[0]["data"])


def trial(backend, action, fraction, timeout):
    baseline_start = time.perf_counter()
    baseline = backend.capture()
    start = time.perf_counter()
    if action:
        backend.input(*action)
    acknowledged = time.perf_counter()
    polls = 0
    changed = False
    while True:
        # The deadline bounds polling, not a capture already in progress.
        if time.perf_counter() - start >= timeout:
            break
        frame = backend.capture()
        polls += 1
        changed = bool(np.mean(np.abs(frame - baseline) > 12) >= fraction)
        if changed:
            break
    end = time.perf_counter()
    return {
        "action": {"method": action[0], "params": action[1]} if action else None,
        "baseline_ms": (start - baseline_start) * 1000,
        "input_ack_ms": (acknowledged - start) * 1000 if action else None,
        "changed": changed,
        "action_to_detection_ms": (end - start) * 1000 if changed else None,
        "observed_elapsed_ms": (end - start) * 1000,
        "polls": polls,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="127.0.0.1:19329")
    parser.add_argument("--mcp", default="/private/tmp/autopaws-cycle-mcp")
    parser.add_argument("--backend", choices=("api", "mcp"), default="api")
    parser.add_argument("--allow-read-only-input", action="store_true", required=True)
    parser.add_argument("--pairs", type=int, default=10)
    parser.add_argument("--timeout", type=float, default=1)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if args.pairs < 1 or args.timeout <= 0:
        parser.error("pairs and timeout must be positive")
    api = API(args.api, 15, "")
    mcp = MCP(args.mcp, args.api, 15, None) if args.backend == "mcp" else None
    backend = Backend(api, mcp)
    groups = {}
    try:
        for name, threshold in (("idle_keyboard", 0.001), ("idle_pointer", 0.00002)):
            groups[name] = [trial(backend, None, threshold, args.timeout) for _ in range(10)]
        # Always dismiss the transient keyboard UI, including on exceptions.
        keyboard = groups["keyboard"] = []
        try:
            for _ in range(args.pairs):
                for params in ({"key": "escape", "modifiers": ["ctrl"]}, {"key": "escape"}):
                    keyboard.append(trial(backend, ("send_key", params), 0.001, args.timeout))
                    time.sleep(0.25)
        finally:
            backend.input("send_key", {"key": "escape"})
        pointer = groups["pointer"] = []
        for _ in range(args.pairs):
            for dx in (100, -100):
                pointer.append(trial(backend, ("mouse_move", {"x": dx, "y": 0, "relative": True}), 0.00002, args.timeout))
    finally:
        if mcp:
            mcp.close()
    report = {
        "recorded_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "backend": args.backend,
        "method": "Fresh baseline before input; sequential fresh-frame polling until threshold or timeout; 480x270 bilinear grayscale; luma delta>12.",
        "thresholds": {"idle_keyboard": 0.001, "idle_pointer": 0.00002, "keyboard": 0.001, "pointer": 0.00002},
        "timeout_seconds": args.timeout,
        "limits": [
            "Pixel change does not establish causality, semantic completion, or true HDMI capture time.",
            "macOS upstream video buffering is included but unmeasured; polling adds quantization.",
            "Detection includes client JPEG decode and pixel comparison, excludes baseline acquisition and controller LLM latency.",
            "Idle controls calibrate both thresholds on this scene, not all possible UI activity.",
            "Input acknowledgement means input-call completion and host serial pacing; it proves neither UART completion nor target acknowledgement.",
        ],
        "groups": {},
    }
    for name, rows in groups.items():
        report["groups"][name] = {
            "attempts": len(rows), "changes": sum(row["changed"] for row in rows),
            "detection": summarize([row["action_to_detection_ms"] for row in rows if row["changed"]]),
            "baseline_plus_detection": summarize([row["baseline_ms"] + row["action_to_detection_ms"] for row in rows if row["changed"]]),
            "input_ack": summarize([row["input_ack_ms"] for row in rows if row["input_ack_ms"] is not None]),
            "rows": rows,
        }
    Path(args.output).write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
