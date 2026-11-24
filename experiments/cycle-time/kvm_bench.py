#!/usr/bin/env python3
"""Measure host-side KVM/MCP latency without input or capture retention.

Target images exist only in memory. Output contains timings, capture dimensions,
configuration metadata, and aggregate grayscale differences; never images, OCR,
tool text, or device identifiers. Requires Pillow and NumPy for frame comparison.
"""

import argparse
import base64
import io
import json
import os
from pathlib import Path
import platform
import select
import socket
import subprocess
import time

import numpy as np
from PIL import Image


MAX_MESSAGE = 64 << 20


def summarize(values):
    ordered = sorted(values)
    if not ordered:
        return {"count": 0}

    def percentile(fraction):
        position = fraction * (len(ordered) - 1)
        lower = int(position)
        upper = min(lower + 1, len(ordered) - 1)
        return ordered[lower] + (ordered[upper] - ordered[lower]) * (position - lower)

    return {
        "count": len(ordered),
        "min_ms": min(ordered),
        "p50_ms": percentile(0.50),
        "p90_ms": percentile(0.90),
        "p95_ms": percentile(0.95),
        "max_ms": max(ordered),
        "samples_ms": values,
    }


class API:
    def __init__(self, address, timeout, token):
        host, port = address.rsplit(":", 1)
        self.address = (host.strip("[]"), int(port))
        self.timeout = timeout
        self.token = token

    def call(self, method):
        request = {"id": 1, "method": method, "params": {}}
        if self.token:
            request["token"] = self.token
        with socket.create_connection(self.address, self.timeout) as connection:
            connection.sendall(json.dumps(request).encode() + b"\n")
            with connection.makefile("rb") as reader:
                line = reader.readline(MAX_MESSAGE + 1)
        if len(line) > MAX_MESSAGE or not line.endswith(b"\n"):
            raise ValueError("invalid API response framing")
        response = json.loads(line)
        if response.get("id") != 1 or response.get("ok") is not True:
            raise RuntimeError("API request failed")
        return response["result"]


class MCP:
    def __init__(self, executable, address, timeout, token_file):
        command = [executable, "-api", address, "-tesseract", "/nonexistent/autopaws-benchmark-ocr-disabled"]
        if token_file:
            command.extend(["-token-file", token_file])
        self.process = subprocess.Popen(
            command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
        )
        self.timeout = timeout
        self.next_id = 0
        self.pending = bytearray()
        try:
            self.request("initialize", {
                "protocolVersion": "2025-06-18", "capabilities": {},
                "clientInfo": {"name": "autopaws-cycle-benchmark", "version": "1"},
            })
            self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        except Exception:
            self.close()
            raise

    def send(self, request):
        self.process.stdin.write(json.dumps(request).encode() + b"\n")
        self.process.stdin.flush()

    def read(self, deadline):
        while b"\n" not in self.pending:
            remaining = deadline - time.perf_counter()
            if remaining <= 0 or not select.select([self.process.stdout], [], [], remaining)[0]:
                raise TimeoutError("MCP response timeout")
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise EOFError("MCP exited")
            self.pending.extend(chunk)
            if len(self.pending) > MAX_MESSAGE:
                raise ValueError("MCP response too large")
        line, _, rest = self.pending.partition(b"\n")
        self.pending = bytearray(rest)
        return json.loads(line)

    def request(self, method, params):
        self.next_id += 1
        request_id = self.next_id
        self.send({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
        deadline = time.perf_counter() + self.timeout
        while True:
            response = self.read(deadline)
            if response.get("id") != request_id:
                continue
            if "error" in response:
                raise RuntimeError("MCP request failed")
            return response["result"]

    def capture(self):
        result = self.request("tools/call", {"name": "capture_screen", "arguments": {}})
        if result.get("isError"):
            raise RuntimeError("MCP tool failed")
        images = [content for content in result.get("content", []) if content.get("type") == "image"]
        if len(images) != 1 or not images[0].get("data"):
            raise ValueError("MCP capture missing image")
        # Validate delivery without decoding, retaining, or exposing tool text.
        return len(images[0]["data"])

    def close(self):
        if self.process.poll() is None:
            self.process.stdin.close()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.terminate()
                try:
                    self.process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    self.process.kill()
                    self.process.wait()
        self.process.stdout.close()


def measure(count, operation, consume=None):
    timings = []
    failures = []
    for index in range(count):
        start = time.perf_counter()
        try:
            result = operation()
            elapsed = (time.perf_counter() - start) * 1000
            if consume is not None:
                consume(result)
            timings.append(elapsed)
        except Exception as error:
            # Exception messages may contain provider/tool data; retain types only.
            failures.append({"sample": index, "error_type": type(error).__name__})
    return {**summarize(timings), "failures": failures}


def safe_metadata(info):
    config = info.get("config", {})
    capture = info.get("capture", {})
    return {
        "config": {key: config[key] for key in (
            "screen_width", "screen_height", "serial_baud", "keyboard_layout", "mouse_button_mode"
        ) if key in config},
        "capture": {key: capture[key] for key in (
            "requested_width", "requested_height", "autocrop", "running", "generation", "format"
        ) if key in capture},
        "capture_device_count": len(capture.get("available_devices", [])),
    }


class FrameDifferences:
    def __init__(self):
        self.previous = None
        self.cpu_ms = []
        self.frames = []

    def consume(self, result):
        start = time.perf_counter()
        jpeg = base64.b64decode(result["jpeg_b64"], validate=True)
        with Image.open(io.BytesIO(jpeg)) as image:
            dimensions = list(image.size)
            gray = np.asarray(image.convert("L").resize((480, 270), Image.Resampling.BILINEAR))
        frame = {"dimensions": dimensions, "jpeg_bytes": len(jpeg)}
        if self.previous is not None:
            delta = np.abs(gray.astype(np.int16) - self.previous.astype(np.int16))
            frame.update({
                "mean_absolute_luma_delta": float(np.mean(delta)),
                "changed_fraction_threshold_12": float(np.mean(delta > 12)),
            })
        self.previous = gray
        self.cpu_ms.append((time.perf_counter() - start) * 1000)
        self.frames.append(frame)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api", default="127.0.0.1:19329")
    parser.add_argument("--mcp", default="/private/tmp/autopaws-cycle-mcp")
    parser.add_argument("--token-file")
    parser.add_argument("--timeout", type=float, default=30)
    parser.add_argument("--samples", type=int, default=30, help="warm raw capture samples")
    parser.add_argument("--ping-samples", type=int, default=20)
    parser.add_argument("--info-samples", type=int, default=10)
    parser.add_argument("--mcp-samples", type=int, default=10)
    parser.add_argument("--cold", action="store_true", help="idle 11 seconds before one cold capture")
    parser.add_argument("--skip-mcp", action="store_true")
    parser.add_argument("--output", help="write metadata/timing JSON here; otherwise stdout")
    args = parser.parse_args()
    if args.timeout <= 0 or min(args.samples, args.ping_samples, args.info_samples, args.mcp_samples) < 1:
        parser.error("timeout and sample counts must be positive")

    token = Path(args.token_file).read_text().strip() if args.token_file else ""
    api = API(args.api, args.timeout, token)
    report = {
        "recorded_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "host": {"os": platform.system(), "release": platform.release(), "architecture": platform.machine()},
        "method": "Sequential calls; monotonic host round-trip milliseconds; no target input; images only in memory.",
        "percentiles": "Linear interpolation between sorted samples.",
        "benchmarks": {},
    }
    results = report["benchmarks"]
    results["api_ping"] = measure(args.ping_samples, lambda: api.call("ping"))
    results["api_get_device_info"] = measure(
        args.info_samples, lambda: api.call("get_device_info"),
        lambda info: report.update({"device_metadata": safe_metadata(info)}),
    )
    if args.cold:
        time.sleep(11)
        results["api_capture_frame_after_11s_idle"] = measure(1, lambda: api.call("capture_frame"))
        report["cold_capture_note"] = "Cold only if no other capture subscriber/request kept the source running."
    else:
        results["api_capture_frame_warmup"] = measure(1, lambda: api.call("capture_frame"))

    differences = FrameDifferences()
    results["api_capture_frame_warm"] = measure(
        args.samples, lambda: api.call("capture_frame"), differences.consume
    )
    report["frame_difference"] = {
        "resize": [480, 270], "luma_threshold": 12,
        "processing": "JPEG base64/decode, grayscale, bilinear resize, absolute delta; outside API timing.",
        "cpu": summarize(differences.cpu_ms), "frames": differences.frames,
    }
    if not args.skip_mcp:
        start = time.perf_counter()
        try:
            mcp = MCP(args.mcp, args.api, args.timeout, args.token_file)
            results["mcp_start_and_initialize_ms"] = (time.perf_counter() - start) * 1000
            try:
                results["mcp_capture_screen_warm"] = measure(args.mcp_samples, mcp.capture)
            finally:
                mcp.close()
        except Exception as error:
            results["mcp_setup_error_type"] = type(error).__name__

    encoded = json.dumps(report, indent=2) + "\n"
    if args.output:
        Path(args.output).write_text(encoded)
    else:
        print(encoded, end="")
    failed = any(isinstance(value, dict) and value.get("failures") for value in results.values())
    return 1 if failed or "mcp_setup_error_type" in results else 0


if __name__ == "__main__":
    raise SystemExit(main())
