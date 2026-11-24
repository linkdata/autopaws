#!/usr/bin/env python3
"""Compare complete external and native MCP predicate cycles on invented UI.

Runs real MCP subprocesses and a real vision model. Only KVM is simulated:
input, capture and device-discovery delays and timed UI transitions are explicit.
The forwarding model proxy records image hashes, never target screenshots.
"""

import argparse
import base64
import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import platform
import socketserver
import statistics
import subprocess
import threading
import time
import urllib.error
import urllib.request

from kvm_bench import MAX_MESSAGE, MCP
from model_bench import DEFAULT_FONT, fixture


CONDITION = "A foreground confirmation modal with Continue and Cancel is visible"
CASES = {
    "initially_true": [(0, "positive")],
    "delayed_true": [(0, "negative"), (500, "positive")],
    "never_true": [(0, "negative")],
    # Positive lasts until its screenshot is submitted to the model, then
    # disappears. This tests checked-image identity independently of model speed.
    "transient_true": [(0, "negative"), (500, "positive")],
}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def decision(response):
    try:
        choices = response["choices"]
        if len(choices) != 1 or choices[0].get("finish_reason") != "stop":
            return None
        value = json.loads(choices[0]["message"]["content"],
                           object_pairs_hook=lambda items: dict(items) if len(items) == 1 else None)
        if isinstance(value, dict) and set(value) == {"satisfied"} and type(value["satisfied"]) is bool:
            return value["satisfied"]
    except (KeyError, IndexError, TypeError, json.JSONDecodeError):
        pass
    return None


class Client(MCP):
    def __init__(self, executable, address, extra=()):
        self.process = subprocess.Popen(
            [executable, "-api", address, "-tesseract", "/nonexistent/benchmark-no-ocr", *extra],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        )
        self.timeout, self.next_id, self.pending = 30, 0, bytearray()
        try:
            self.request("initialize", {
                "protocolVersion": "2025-06-18", "capabilities": {},
                "clientInfo": {"name": "autopaws-complete-cycle-benchmark", "version": "1"},
            })
            self.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        except Exception:
            self.close()
            raise

    def tool(self, name, arguments, timeout=30):
        self.timeout = timeout
        try:
            result = self.request("tools/call", {"name": name, "arguments": arguments})
        except TimeoutError:
            self.send({"jsonrpc": "2.0", "method": "notifications/cancelled",
                       "params": {"requestId": self.next_id, "reason": "Benchmark observation deadline"}})
            raise
        if result.get("isError"):
            raise RuntimeError("MCP tool error")
        return result


def image_from(result):
    images = [item for item in result.get("content", []) if item.get("type") == "image"]
    if len(images) > 1:
        raise ValueError("multiple returned images")
    return base64.b64decode(images[0]["data"], validate=True) if images else None


def metadata_from(result):
    for item in result.get("content", []):
        if item.get("type") == "text":
            try:
                parsed = json.loads(item["text"])
            except (KeyError, json.JSONDecodeError):
                continue
            if isinstance(parsed, dict) and "observation" in parsed:
                return parsed
    raise ValueError("native observation metadata missing")


class SyntheticAPI(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, input_ms, capture_ms, discovery_ms):
        super().__init__(("127.0.0.1", 0), APIHandler)
        self.delays = (input_ms, capture_ms, discovery_ms)
        self.lock, self.trial = threading.Lock(), None

    def start_trial(self, case, frames):
        trial = {"case": case, "trace": CASES[case], "frames": frames,
                 "action_ack_at": None, "positive_submitted_at": None, "calls": []}
        with self.lock:
            self.trial = trial
        return trial

    @staticmethod
    def frame_stage(trial, at):
        if trial["case"] == "transient_true" and trial["positive_submitted_at"] is not None:
            return "after"
        elapsed = (at - trial["action_ack_at"]) * 1000 if trial["action_ack_at"] is not None else 0
        return next(stage for threshold, stage in reversed(trial["trace"]) if elapsed >= threshold)


class APIHandler(socketserver.StreamRequestHandler):
    def handle(self):
        request = json.loads(self.rfile.readline(MAX_MESSAGE + 1))
        method, params = request.get("method"), request.get("params", {})
        server = self.server
        with server.lock:
            trial = server.trial
        event = {"method": method, "params": params}
        if trial is not None:
            trial["calls"].append(event)
        result, error = None, None
        if method == "send_key" and set(params) <= {"key", "modifiers"}:
            time.sleep(server.delays[0] / 1000)
            trial["action_ack_at"] = time.perf_counter()
            result = {"sent": True}
        elif method == "capture_frame" and not params:
            time.sleep(server.delays[1] / 1000)
            stage = server.frame_stage(trial, time.perf_counter())
            index, jpeg = trial["frames"][stage]
            event["fixture_index"], event["image_sha256"] = index, digest(jpeg)
            result = {"jpeg_b64": base64.b64encode(jpeg).decode(), "width": 960, "height": 540,
                      "target_width": 1920, "target_height": 1080}
        elif method == "get_device_info" and not params:
            time.sleep(server.delays[2] / 1000)
            result = {"config": {"screen_width": 1920, "screen_height": 1080}}
        else:
            error = "invalid params"
        response = {"id": request.get("id"), "ok": error is None}
        response.update({"error": error} if error else {"result": result})
        try:
            self.wfile.write(json.dumps(response).encode() + b"\n")
        except (BrokenPipeError, ConnectionResetError):
            pass


class ModelProxy(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, upstream, hashes):
        super().__init__(("127.0.0.1", 0), ProxyHandler)
        self.upstream, self.hashes = upstream.rstrip("/"), hashes
        self.current, self.current_source, self.template = None, None, None
        self.seen_images = set()
        self.condition, self.active, self.requests_started = threading.Condition(), 0, 0

    def drain(self):
        deadline = time.perf_counter() + 120
        with self.condition:
            while True:
                if not self.condition.wait_for(lambda: self.active == 0, timeout=max(0, deadline - time.perf_counter())):
                    raise TimeoutError("model proxy did not drain")
                version = self.requests_started
                self.condition.wait(timeout=0.05)
                if self.active == 0 and self.requests_started == version:
                    return


class ProxyHandler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        with self.server.condition:
            self.server.active += 1
            self.server.requests_started += 1
            self.server.condition.notify_all()
        try:
            self.forward()
        finally:
            with self.server.condition:
                self.server.active -= 1
                self.server.condition.notify_all()

    def forward(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        payload = json.loads(body)
        with self.server.condition:
            trial, source = self.server.current, self.server.current_source
            if self.server.template is None:
                self.server.template = copy.deepcopy(payload)
        encoded = next(part["image_url"]["url"] for message in payload["messages"]
                       if isinstance(message.get("content"), list) for part in message["content"]
                       if part.get("type") == "image_url")
        image_hash = digest(base64.b64decode(encoded.split(",", 1)[1], validate=True))
        info = self.server.hashes.get(image_hash, {})
        cache_key = (payload["model"], image_hash)
        event = {"model": payload["model"], "image_sha256": image_hash, "fixture_index": info.get("index"),
                 "fixture_stage": info.get("stage"),
                 "exact_image_previously_requested": cache_key in self.server.seen_images,
                 "started_at": time.perf_counter(), "satisfied": None}
        self.server.seen_images.add(cache_key)
        if source["case"] == "transient_true" and info.get("stage") == "positive":
            source["positive_submitted_at"] = time.perf_counter()
        trial["model_calls"].append(event)
        try:
            req = urllib.request.Request(self.server.upstream + self.path, data=body,
                                         headers={"Content-Type": "application/json"})
            try:
                with urllib.request.urlopen(req, timeout=120) as response:
                    status, response_body = response.status, response.read()
            except urllib.error.HTTPError as error:
                status, response_body = error.code, error.read()
            event["http_status"] = status
            try:
                event["satisfied"] = decision(json.loads(response_body)) if status == 200 else None
            except json.JSONDecodeError:
                pass
            event["finished_at"] = time.perf_counter()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response_body)))
            self.end_headers()
            try:
                self.wfile.write(response_body)
            except (BrokenPipeError, ConnectionResetError):
                event["downstream_closed"] = True
        finally:
            event["finished_at"] = event.get("finished_at", time.perf_counter())


def check_image(proxy_url, template, jpeg, timeout, model=None):
    payload = copy.deepcopy(template)
    if model:
        payload["model"] = model
    for message in payload["messages"]:
        if isinstance(message.get("content"), list):
            for part in message["content"]:
                if part.get("type") == "image_url":
                    part["image_url"]["url"] = "data:image/jpeg;base64," + base64.b64encode(jpeg).decode()
    req = urllib.request.Request(proxy_url + "/v1/chat/completions", data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=max(0.001, timeout)) as response:
        return decision(json.load(response))


def external_cycle(client, proxy_url, template, timeout_ms, controller_model=None):
    client.tool("send_key", {"key": "enter"})
    deadline = time.perf_counter() + timeout_ms / 1000
    checked, checked_delivered_at, evaluations, satisfied, status = None, None, 0, None, "timeout"
    while time.perf_counter() < deadline:
        try:
            result = client.tool("capture_screen", {}, max(0.001, deadline - time.perf_counter()))
            image_delivered_at = time.perf_counter()
            jpeg = image_from(result)
            value = check_image(proxy_url, template, jpeg, deadline - time.perf_counter(), controller_model)
            if time.perf_counter() >= deadline:
                break
            evaluations += 1
            if value is None:
                status = "invalid_output"
                break
            checked, checked_delivered_at, satisfied = jpeg, image_delivered_at, value
            if value:
                status = "condition_met"
                break
        except urllib.error.HTTPError:
            status = "analysis_error"
            break
        except TimeoutError:
            break
        except urllib.error.URLError as error:
            if not isinstance(error.reason, TimeoutError):
                status = "analysis_error"
            break
    return {"input": {"sent": True}, "observation": {"status": status},
            "predicate": {"satisfied": satisfied, "evaluations": evaluations},
            "frame": {"id": digest(checked)} if checked else None}, checked, checked_delivered_at


def run_trial(client, path, api, proxy, case, timeout_ms, base_index, font, controller_model=None):
    frames = {stage: (index, fixture(index, 960, font)[0])
              for stage, index in (("negative", base_index), ("positive", base_index + 3), ("after", base_index + 4))}
    for stage, (index, jpeg) in frames.items():
        proxy.hashes[digest(jpeg)] = {"index": index, "stage": stage}
    trace = api.start_trial(case, frames)
    log = {"path": path, "case": case, "fixture_base_index": base_index, "model_calls": []}
    proxy.current, proxy.current_source = log, trace
    start = time.perf_counter()
    if path == "native":
        result = client.tool("send_key", {"key": "enter", "wait_for": CONDITION,
                                         "wait_timeout_ms": timeout_ms})
        checked_result_at = time.perf_counter()
        meta, image = metadata_from(result), image_from(result)
        image_delivered_at = checked_result_at if image else None
    else:
        meta, image, image_delivered_at = external_cycle(
            client, f"http://127.0.0.1:{proxy.server_port}", proxy.template, timeout_ms, controller_model)
        checked_result_at = time.perf_counter()
    controller_satisfied, controller_error = None, None
    if controller_model:
        if path == "external":
            controller_satisfied = meta["predicate"]["satisfied"]
        elif image and meta["observation"]["status"] == "condition_met":
            remaining = trace["action_ack_at"] + timeout_ms / 1000 - time.perf_counter()
            if remaining > 0:
                try:
                    controller_satisfied = check_image(f"http://127.0.0.1:{proxy.server_port}",
                        proxy.template, image, remaining, controller_model)
                except (TimeoutError, urllib.error.URLError) as error:
                    controller_error = type(error).__name__
    returned_at = time.perf_counter()
    proxy.drain()
    image_hash = digest(image) if image else None
    completed = [call for call in log["model_calls"] if call.get("finished_at", float("inf")) <= returned_at
                 and call["satisfied"] is not None]
    last_checked = completed[-1] if completed else None
    status = meta["observation"]["status"]
    expected = "timeout" if case == "never_true" else "condition_met"
    returned_info = proxy.hashes.get(image_hash, {})
    returned_index = returned_info.get("index")
    log.update({
        "command_to_response_ms": (checked_result_at - start) * 1000, "metadata": meta,
        "command_to_image_delivery_ms": (image_delivered_at - start) * 1000 if image_delivered_at else None,
        "returned_image_sha256": image_hash, "returned_fixture_index": returned_index,
        "source_stage_at_return": api.frame_stage(trace, returned_at), "api_calls": trace["calls"],
        "verification": {
            "expected_status": expected, "status_matches": status == expected,
            "input_sent": meta.get("input", {}).get("sent") is True,
            "one_input_request": sum(call["method"] == "send_key" for call in trace["calls"]) == 1,
            "image_matches_last_completed_check": image_hash == (last_checked["image_sha256"] if last_checked else None),
            "frame_hash_matches_returned_image": meta.get("frame", {}).get("sha256", meta.get("frame", {}).get("id")) == image_hash if image else meta.get("frame") is None,
            "condition_met_has_true_checked_image": status != "condition_met" or (
                image is not None and last_checked is not None and last_checked["satisfied"] is True
                and returned_info.get("stage") == "positive"),
        },
    })
    if controller_model:
        log.update({"controller_model": controller_model, "controller_satisfied": controller_satisfied,
                    "controller_error_type": controller_error,
                    "command_to_controller_verdict_ms": (returned_at - start) * 1000})
        log["verification"]["controller_verdict_true"] = controller_satisfied is True
    for call in log["model_calls"]:
        call["started_ms_after_command"] = (call.pop("started_at") - start) * 1000
        call["finished_ms_after_command"] = (call.pop("finished_at") - start) * 1000
    return log


def main():
    harness_sha256 = digest(Path(__file__).read_bytes())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline-mcp", required=True)
    parser.add_argument("--current-mcp", required=True)
    parser.add_argument("--model", default="qwen3-vl-2b-instruct")
    parser.add_argument("--controller-model", help="Use this model for baseline polling and one final native-image verdict; runs positive cases only")
    parser.add_argument("--model-base-url", default="http://127.0.0.1:11434")
    parser.add_argument("--provider-options-json", default="{}")
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--fixture-offset", type=int, default=10000,
                        help="Fresh multiple-of-four fixture offset; change between benchmark runs to avoid image-cache reuse")
    parser.add_argument("--timeout-ms", type=int, default=6000)
    parser.add_argument("--input-ms", type=float, default=47)
    parser.add_argument("--capture-ms", type=float, default=58)
    parser.add_argument("--discovery-ms", type=float, default=133)
    parser.add_argument("--font", type=Path, default=Path(DEFAULT_FONT))
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    if args.samples < 1 or not 1 <= args.timeout_ms <= 60000 or min(args.input_ms, args.capture_ms, args.discovery_ms) < 0:
        parser.error("positive samples, 1..60000 timeout and nonnegative delays required")
    if args.fixture_offset < 0 or args.fixture_offset % 4:
        parser.error("--fixture-offset must be a nonnegative multiple of four")
    if not args.font.is_file():
        parser.error("--font must name an existing TrueType font")
    try:
        options = json.loads(args.provider_options_json)
    except json.JSONDecodeError:
        parser.error("--provider-options-json must be valid JSON")
    if not isinstance(options, dict):
        parser.error("--provider-options-json must be a JSON object")
    api = SyntheticAPI(args.input_ms, args.capture_ms, args.discovery_ms)
    proxy = ModelProxy(args.model_base_url, {})
    servers = [threading.Thread(target=server.serve_forever, daemon=True) for server in (api, proxy)]
    for thread in servers:
        thread.start()
    address = f"127.0.0.1:{api.server_address[1]}"
    proxy_url = f"http://127.0.0.1:{proxy.server_port}"
    clients, trials, controller_warmup = {}, [], None
    cases = ("initially_true", "delayed_true") if args.controller_model else tuple(CASES)
    try:
        clients["external"] = Client(args.baseline_mcp, address)
        clients["native"] = Client(args.current_mcp, address, [
            "-vision-api", proxy_url + "/v1", "-vision-model", args.model,
            "-vision-max-width", "960", "-vision-request-json", json.dumps(options),
        ])
        # Excluded warmup also records the native provider payload, reused exactly
        # by the external baseline apart from each screenshot's data URL.
        warmup_index = args.fixture_offset + 8 * args.samples * len(CASES) * 2 + 8000
        warmup = run_trial(clients["native"], "native", api, proxy, "initially_true", 10000, warmup_index, str(args.font))
        if not all(value for key, value in warmup["verification"].items() if key != "expected_status"):
            raise RuntimeError("native warmup failed")
        if args.controller_model:
            controller_warmup = {"model_calls": []}
            proxy.current = controller_warmup
            started = time.perf_counter()
            satisfied = check_image(proxy_url, proxy.template, api.trial["frames"]["positive"][1], 60, args.controller_model)
            proxy.drain()
            controller_warmup = {"model": args.controller_model, "satisfied": satisfied,
                                 "wall_ms": (time.perf_counter() - started) * 1000}
            if satisfied is not True:
                raise RuntimeError("controller warmup failed")
        trial_number = 0
        for sample in range(args.samples):
            for case in cases:
                for path in (("external", "native") if sample % 2 == 0 else ("native", "external")):
                    base_index = args.fixture_offset + 8 * trial_number
                    trial_number += 1
                    trial = run_trial(clients[path], path, api, proxy, case, args.timeout_ms,
                                      base_index, str(args.font), args.controller_model)
                    trial["sample"] = sample
                    trials.append(trial)
                    print(json.dumps({"sample": sample, "case": case, "path": path,
                                      "ms": round(trial["command_to_response_ms"], 3),
                                      "status": trial["metadata"]["observation"]["status"],
                                      "verification": trial["verification"]}), flush=True)
    finally:
        for client in clients.values():
            client.close()
        proxy.drain()
        for server in (api, proxy):
            server.shutdown()
            server.server_close()
    summaries = []
    for case in cases:
        for path in ("external", "native"):
            rows = [trial for trial in trials if trial["case"] == case and trial["path"] == path]
            values = [row["command_to_response_ms"] for row in rows]
            summary = {"case": case, "path": path, "n": len(rows),
                              "median_ms": statistics.median(values), "min_ms": min(values), "max_ms": max(values),
                              "all_verifications_pass": all(all(v for k, v in row["verification"].items()
                                  if k != "expected_status") for row in rows)}
            if args.controller_model:
                summary["image_delivery_median_ms"] = statistics.median(row["command_to_image_delivery_ms"] for row in rows)
                summary["controller_verdict_median_ms"] = statistics.median(row["command_to_controller_verdict_ms"] for row in rows)
            summaries.append(summary)
    assert digest(Path(__file__).read_bytes()) == harness_sha256, "benchmark script changed during execution"
    report = {"harness_sha256": harness_sha256, "recorded_at_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "host": {"platform": platform.platform(), "python": platform.python_version()},
              "arguments": vars(args) | {"font": str(args.font), "out": str(args.out)},
              "condition": CONDITION, "fixture_generator_sha256": digest(Path(__file__).with_name("model_bench.py").read_bytes()),
              "source_traces": CASES,
              "binaries": {key: {"path": path, "sha256": digest(Path(path).read_bytes())}
                           for key, path in (("external", args.baseline_mcp), ("native", args.current_mcp))},
              "provider_payload_without_images": {key: value for key, value in proxy.template.items() if key != "messages"},
              "provider_text_messages": [{"role": message["role"], "texts": [part["text"]
                  for part in message["content"] if part.get("type") == "text"]}
                  for message in proxy.template["messages"] if isinstance(message.get("content"), list)],
              "limits": ["Synthetic KVM timings and controlled traces; actual MCP and local vision HTTP inference.",
                         "Startup/warmup and request draining excluded; controller inference is only the constrained boolean reaction." if args.controller_model else "Startup/warmup and request draining excluded; no controller LLM inference included.",
                         "Timed-out upstream requests are drained before the next trial; proxy does not measure backend cancellation latency.",
                         "Every timed trial/path uses fresh fixture indices; exact static-frame repeats within a wait are labeled per request.",
                         "Transient positive reverts when submitted for inference, a deterministic checked-image freshness stress.",
                         "Sources are960px and preserve JPEG identity; production1920-to960 resize accuracy is not validated by these cycles.",
                         "All fixture images are generated; no target images, OCR or hardware accessed."],
              "controller_measurement": "Fixed-action boolean reaction on the supplied image; command planning and full agent reasoning are outside timing." if args.controller_model else None,
              "warmup": warmup, "controller_warmup": controller_warmup, "summary": summaries, "trials": trials}
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2) + "\n")
    return 0 if all(row["all_verifications_pass"] for row in summaries) else 1


if __name__ == "__main__":
    raise SystemExit(main())
