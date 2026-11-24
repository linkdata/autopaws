# Input and observation cycle times

This is the initial research baseline and proposal. See the
[implemented flow and complete-cycle measurements](cycle-time-implementation.md)
for the subsequent changes and comparisons.

Measurements on 23 November 2026 show that removing repeated capture-device
discovery is the first optimization to pursue. Warm MCP screenshots took a
median 199 ms, compared with 58 ms for the underlying KVM snapshot. The extra
device-info call took 133 ms. Client JPEG decoding and pixel comparison cost
about 5 ms per frame.

An optional input-and-observe tool can save controller round trips and fixed
sleeps. A caller-supplied visual condition plus the evaluated screenshot is a
promising interface: the small model checked conditions in about 377–379 ms.
It also made false decisions, so the screenshot remains evidence for the main
model to assess. Use pixels to select candidate frames and make full UI-state
JSON optional. Local inference pays off when it replaces more expensive
controller turns or fixed waits.

## Physical measurements

The host was an Apple M3 Ultra with 256 GiB memory, macOS 26.7.3, Go 1.27.4 and
FFmpeg 9.0.2. Both commands were built from commit `27cd43e`. The connected target
was the owner's Windows machine. The UGREEN 15389 capture card provided
1920×1080 video through AVFoundation; CH340/CH9329 input used 9600 baud and
relative button reports. Autocrop and audio were disabled for this experiment.

Only actions, outcomes, timings and host/capture metadata were retained. No
target scripts were installed, no target files or settings were changed, and no
Windows screenshots, screen contents, identifiers or OCR were retained.

All times below are host wall-clock milliseconds. Percentiles interpolate
between sorted samples; small samples describe this run and are not tail-latency
guarantees. The controller LLM and its network are outside these measurements.

| Operation | Samples | Median | p95 |
|---|---:|---:|---:|
| KVM `ping` | 20 | 0.23 | 1.03 |
| KVM `get_device_info` | 10 | 132.6 | 186.9 |
| KVM warm `capture_frame` | 30 | 57.9 | 72.3 |
| MCP warm `capture_screen` | 10 | 198.8 | 207.5 |
| Client JPEG decode, grayscale resize and difference | 30 | 5.35 | 5.99 |

One capture after eleven idle seconds took 572 ms; MCP process startup and
initialization took 372 ms. Neither is included in warm timings. Before the
owner confirmed target signal readiness, three capture calls timed out and one
returned unavailable. Those acquisition failures are separate from the ready
signal measurements.

For input trials, a fresh baseline was captured before each action. The client
then sent input and polled fresh frames until a threshold was exceeded or one
second elapsed. Each backend ran ten Ctrl+Escape / Escape pairs and ten pairs
of relative pointer moves (+100, −100 horizontal pixels). These are transient
read-only actions; their results contain no target UI descriptions.

| Path and action | Input call median | Action to pixel change median | p95 | Median including baseline |
|---|---:|---:|---:|---:|
| KVM keyboard | 47.1 | 326.6 | 406.7 | 395.4 |
| MCP keyboard | 47.7 | 494.9 | 666.4 | 716.1 |
| KVM pointer | 14.8 | 141.3 | 171.6 | 204.2 |
| MCP pointer | 15.0 | 428.6 | 439.8 | 632.7 |

All twenty trials per action/backend observed a change. Ten one-second idle
trials per threshold/backend produced zero detections. Comparison used
480×270 bilinear grayscale frames, absolute luma difference greater than 12,
and changed-area fractions of 0.001 for keyboard and 0.00002 for pointer. The
pointer threshold is approximately three resized pixels and needs broader
noise testing before becoming a default.

These results measure an action-associated visual change, not semantic task
completion or causality. An animated region, caret, clock, cursor or unrelated
notification can satisfy a pixel threshold. Polling and upstream HDMI/OS/FFmpeg
buffering are included; true target presentation timestamps are unavailable on
this capture path. The polling deadline does not cancel an already-running
capture. Input-call completion establishes host input processing and serial
pacing, without proving UART completion, CH9329 receipt or target processing.

## Local model measurements

Model measurements used generated UI fixtures, separate from the Windows
capture. LM Studio was version `0.4.28+2`, using MLX runtime `1.14.0` and
8192-token contexts. The CLI at `~/.lmstudio/bin/lms` matched the bundled CLI
byte for byte, with commit `43d4b26` and SHA-256
`8cf97829a121dd5dcc80babf39f9cfc47ab47736d3abd5284bc4664ef32e8dc5`.
One sandboxed CLI inventory attempt launched an app process that aborted in
macOS application registration; the original app/API remained running. The
subsequent CLI and API calls used normal local access.

Requests were sequential, temperature zero, capped at 96 output tokens, and
used `/v1/chat/completions` JSON schema output. The four requested fields were
mode, status, completed count and primary button text. Each warm request used
the next generated fixture; first requests were kept separate. These timings
include request serialization, HTTP and response parsing, but exclude fixture
generation, JPEG encoding and data-URL construction.

| Model and quantization | Image | Warm n | Median ms | Range ms | Exact four fields | JSON channel |
|---|---|---:|---:|---|---|---|
| Qwen3-VL 2B Instruct, MLX 4-bit | 960×540 | 8 | 466.5 | 456.9–477.7 | 8/8 | Normal content |
| Qwen3-VL 2B Instruct, MLX 4-bit | 1920×1080 | 5 | 1450.0 | 1442.0–1456.2 | 5/5 | Normal content |
| Holo 3.1 9B, MLX 8-bit | 960×540 | 5 | 1345.1 | 1344.3–1349.1 | 1/5 | Reasoning only |
| Holo 3.1 9B, MLX 8-bit | 1920×1080 | 5 | 3913.3 | 3896.1–3924.1 | 5/5 | Reasoning only |
| Holo4 27B Abliterix OQ6e, MLX 6-bit | 960×540 | 5 | 3304.6 | 3268.5–3311.7 | 5/5 | Reasoning only |
| Holo4 27B Abliterix OQ6e, MLX 6-bit | 1920×1080 | 5 | 8734.0 | 8728.9–8760.2 | 5/5 | Reasoning only |

The Qwen rows use a clarified prompt defining a dialog as a foreground modal
over a dimmed background. With the initial ambiguous prompt, Qwen and the
960-pixel Holo 9B run read status/count/button correctly but usually called
the main white dashboard card a dialog. Qwen scored only 1/5 exact objects at
each resolution with that initial prompt. The clarified runs are a prompt
experiment, not a controlled ranking of model accuracy. All fixtures are
simple, high-contrast invented screens; arbitrary applications, bounding boxes,
small fonts and actionable full-screen JSON remain untested.

Both Holo models returned parseable JSON in `reasoning_content` with empty
normal `content`. Their exact-field counts above use that diagnostic channel;
**none passed the ordinary final-content contract**. Sending
`reasoning_effort: "none"` and `chat_template_kwargs.enable_thinking: false`
did not change this behavior in the installed setup. The native API rejected
an explicit reasoning-off request. A production integration must resolve this
template/channel behavior rather than treating a fast response as success.

For two-image change detection, Qwen 2B at 960×540 took a median 583.1 ms
(519.1–647.8 ms), correctly classifying four changed and four identical pairs.
This small, noise-free test supports using pixels for detection: the model
comparison is much slower than the 5.35 ms client decode/difference operation,
and semantic analysis can run once after a pixel event. Compression noise,
animation and subtle real UI transitions were not tested with the model.

Qwen's first inference after loading took 2715 ms; loading its 1.80 GB model
took 3.11 seconds separately. The initial 960-pixel Holo requests took about
4387 ms (9B) and 4860 ms (27B). These are first-request observations, not
controlled cold-start distributions. Keep the selected model resident and
separate its first inference from steady-state measurements.

Tesseract with the MCP's 2× nearest-neighbor preprocessing and `--oem 3 --psm 6`
took median 187 ms at 960×540 and 283 ms at 1920×1080 on the generated screens.
The harness fed PNG while MCP feeds PGM. It omitted the blue button text, so
it did not produce equivalent UI state.

Adding the 467 ms compact Qwen analysis to the physical raw-API medians suggests
roughly 608 ms for pointer input or 794 ms for keyboard input with a maintained
baseline; acquiring a baseline first suggests 671/862 ms. These are sums of
medians from different experiments, **not measured end-to-end JSON cycles**.
They exclude controller inference and any settling interval. A complete UI
inventory will need a different output budget and accuracy benchmark.

The [model record](validation/cycle-time-models-2026-11-23.json) contains the
synthetic expectations, responses, first requests, prompt variants and raw
timings, including failed probes.
The original 27B model was left loaded; the 9B and 2B instances were unloaded.
The downloaded Qwen 2B model remains available for a follow-up prototype.

## Caller supplied visual conditions

The main model can provide a short condition, let the local model evaluate
candidate screenshots until true or timeout, then receive the evaluated image
as ordinary MCP image content. This avoids generating a complete UI inventory
and gives the main model visual evidence for its next decision. It still pays
the main model's screenshot-processing cost, which was not measured here.

Qwen 2B at 960×540 evaluated three conditions against twelve changing generated
screens each. Requests used a single image, a 32-token cap, temperature zero
and a strict `{"satisfied": boolean}` schema. First requests were separate.

| Condition | Warm n | Median ms | Correct | False accept | False reject |
|---|---:|---:|---|---:|---:|
| Status is Complete | 12 | 378.8 | 11/12 | 1 | 0 |
| A foreground confirmation modal with Continue and Cancel is visible | 12 | 377.0 | 12/12 | 0 | 0 |
| Completed count is at least 15 | 12 | 377.2 | 7/12 | 0 | 5 |

All 36 responses were schema-valid normal content. The status condition
prematurely accepted a modal frame with underlying status Ready. The numeric
condition rejected counts 17–20 and 22. These are failure examples from a small
synthetic test, not production error-rate estimates. For numeric predicates,
extract the observed value and compare it in code; arbitrary natural-language
conditions need separate accuracy testing. Requiring repeated positive results
does not eliminate systematic model errors.

This prompt/output configuration was about 90 ms faster than four-field state
extraction. Its first inference took 2441 ms, separately from a 2.87-second
model reload. Continuous inference would check about 2.6 frames per second
before capture overhead. Pixel gating can skip unchanged candidates, but
evaluate the first fresh post-action frame unconditionally so an already-true
condition can return. Use regions of interest or periodic semantic checks to
avoid suppressing relevant changes below a pixel threshold.

For example, a proposed keyboard call could add:

```json
{
  "key": "enter",
  "wait_for": "A foreground confirmation modal with Continue and Cancel is visible",
  "wait_timeout_ms": 3000
}
```

Return separate `input.sent`, `observation.status` (`condition_met`, `timeout`
or an error), the condition, boolean result, model timing and frame identity,
alongside the image. Predicate inference must fit within the remaining wait
budget. Keep one inference in flight and replace pending candidates with the
newest frame, rather than building a queue.

Return the most recent screenshot actually evaluated as positive, with its age.
A new capture taken after acceptance might no longer satisfy the condition.
If a newer frame is available, mark the result superseded or re-evaluate it
within the deadline; never attach an old positive decision to an unchecked
new image. The returned image should establish MCP coordinates through the
same successful-image/session checks as `capture_screen`. A local-model
positive is an observation to verify, not guaranteed task completion.

The [predicate record](validation/cycle-time-predicates-2026-11-23.json)
includes every expectation, response, first request and confusion count.

## Changes to prioritize

1. Remove device enumeration from the screenshot path. MCP currently calls
   `capture_frame` and then `get_device_info` to obtain configured target
   coordinate dimensions. `get_device_info` calls capture discovery, which
   launches FFmpeg on macOS. Return target coordinate dimensions alongside the
   snapshot, or expose a cheap configuration read. Preserve coordinate
   invalidation and cancellation semantics. The observed 141 ms screenshot
   gap is about 71% of current MCP screenshot time; this is an opportunity,
   not a measured patched implementation. The relevant paths are
   [MCP capture](../cmd/autopaws-mcp/capture.go),
   [API capture information](../api/capture.go) and
   [AVFoundation discovery](../avfoundation/devices.go).
2. Add a daemon-side change observer using the existing latest-frame
   [subscriptions](../capture/manager.go). Keep it subscribed while observation is active so capture
   stays warm. Decode/downsample and compare frames without repeated
   JSON/base64 snapshot transport. Benchmark cursor/clock masks, regions of
   interest, changed-area thresholds and optional settling time on the
   unrestricted Linux target before choosing defaults.
3. Add optional analysis in MCP after selecting candidate frames. Keep provider
   and model configuration out of KVM. Support a caller-supplied visual
   condition returning an evaluated screenshot, an inexpensive change-only
   result, and optional full UI JSON. Bound generated output and validate the
   schema and fields before returning it.
4. Measure complete controller tasks after the component improvements. A local
   model helps only when its inference cost is smaller than the controller
   turns and fixed waits it avoids. Compare success rate, controller tokens,
   retries and total task time using the same tasks and controller model.

Existing input delays also matter. The key hold is 10 ms, ordinary text adds
20 ms per character by default, sequences add 100 ms per step by default, and
dragging adds two 50 ms pauses. Explicit zero character/sequence delays are
already supported. At 9600 baud the serial guard alone is 15.625 ms per
keyboard report, yielding a 41.25 ms minimum for a key press/release before
extra drain and driver costs. A higher baud rate needs matching hardware
configuration and separate physical validation.

## Proposed observation contract

Arm a baseline before sending input. Make an optional `wait_timeout_ms` available
on keyboard and mouse tools, with an optional region and `settle_ms`. Keep the
observation deadline separate from optional full-state `analysis_timeout_ms`,
with a total call budget. When `wait_for` is supplied, predicate inference is
part of the observation deadline. A change event and a model predicate are
different observation modes; neither guarantees task completion.

Return input and observation outcomes independently so an observation timeout
never encourages replaying an input that was already sent:

```json
{
  "input": {"sent": true, "duration_ms": 47},
  "observation": {
    "status": "changed",
    "duration_ms": 280,
    "frame": {"generation": 1, "frame_id": "session-example:42", "width": 1920, "height": 1080}
  },
  "analysis": {
    "status": "ok",
    "model": "configured-local-model",
    "frame_id": "session-example:42",
    "duration_ms": 1000,
    "frame_age_ms": 1000,
    "superseded": false
  },
  "ui": {"summary": "Synthetic example", "elements": []}
}
```

This is a proposed schema, with illustrative values. Observation outcomes
should include `changed`, `timeout` and `capture_error`; analysis needs its own
`disabled`, `ok`, `timeout`, `invalid_output` and `error` statuses. Return the
latest available observation on timeout if possible, while preserving the
input result. Cancellation must release input through the existing cleanup
path and stop observation/inference work.

Use at most one inference in flight and replace pending frames with the newest
frame. An analyzed screenshot can become stale while a model generates its
answer. Include the analyzed frame's generation, stable ID, age and superseded
status instead of promising that a completed answer describes the current
screen. Model-derived coordinates need an explicit image coordinate space and
the same generation/dimension checks as screenshot coordinates; JSON output
must not silently establish coordinates using existing screenshot semantics.

The proposed `frame_id` must combine a daemon-incarnation identifier with a
monotonic observation counter. Existing capture sequence numbers reset when
the source reopens, while configuration generation can remain unchanged;
generation plus the current source sequence is not a unique observation ID.

## Reproduction

The host-only scripts are in [experiments/cycle-time](../experiments/cycle-time/).
They require Python 3, Pillow and NumPy. Build the current binaries, start a
daemon on a separate port, and use one operator:

```sh
CGO_ENABLED=0 go build -trimpath -o /private/tmp/autopaws-cycle-kvm ./cmd/autopaws-kvm
CGO_ENABLED=0 go build -trimpath -o /private/tmp/autopaws-cycle-mcp ./cmd/autopaws-mcp
/private/tmp/autopaws-cycle-kvm -api 127.0.0.1:19329 \
  -capture-device 'avfoundation:UGREEN 15389' -audio-device off -autocrop=false
```

From another terminal:

```sh
python3 experiments/cycle-time/kvm_bench.py --cold --output /private/tmp/kvm.json
python3 experiments/cycle-time/action_bench.py --allow-read-only-input --output /private/tmp/api-actions.json
python3 experiments/cycle-time/action_bench.py --backend mcp --allow-read-only-input --output /private/tmp/mcp-actions.json
```

The action script requires an owner-prepared target where these shortcuts and
pointer movements are safe. It does not verify the target OS. The snapshot
script sends no target input. Capture logging stays disabled; neither script
saves target screenshots or OCR. Cold capture requires no other capture demand.
Raw timing records are in the [validation record](validation/cycle-time-2026-11-23.json).

For the local model experiment, load `mlx-community/Qwen3-VL-2B-Instruct-4bit`
in LM Studio with context length 8192 and its API listening at port 11434:

```sh
python3 experiments/cycle-time/model_bench.py --model qwen3-vl-2b-instruct \
  --widths 960 --samples 8 --explicit-mode --out /private/tmp/ui-state
python3 experiments/cycle-time/model_bench.py --model qwen3-vl-2b-instruct \
  --widths 960 --samples 8 --binary --out /private/tmp/ui-change
python3 experiments/cycle-time/predicate_bench.py --model qwen3-vl-2b-instruct \
  --width 960 --samples 12 --out /private/tmp/ui-predicates
```

The model script generates its own fixture images; it never reads KVM images.
It writes those synthetic JPEGs, JSON responses and optional synthetic OCR to
the output directory. On other hosts, supply `--font /path/to/font.ttf`.

## Primary references

- [LM Studio structured output](https://lmstudio.ai/docs/developer/openai-compat/structured-output)
  describes JSON schema output and warns that model support varies,
  particularly below 7B parameters. Syntactic validity is separate from
  correct UI interpretation.
- [LM Studio native chat](https://lmstudio.ai/docs/developer/rest/chat) and
  [streaming events](https://lmstudio.ai/docs/developer/rest/streaming-events)
  expose image input, reasoning controls and timing information. Client wall
  time remains the main cycle-time metric.
- [Model residency and TTL](https://lmstudio.ai/docs/developer/core/ttl-and-auto-evict)
  explain cold loading and auto-eviction. Loading and warm inference should
  be reported separately.
- [llama.cpp multimodal architecture](https://github.com/ggml-org/llama.cpp/blob/master/tools/mtmd/README.md)
  describes separate vision encoding/projector work. A smaller language model
  still pays image-processing costs.
- [OpenCV background subtraction](https://docs.opencv.org/4.x/d1/dc5/tutorial_background_subtraction.html)
  documents inexpensive image-based foreground detection. A simpler frame
  difference is sufficient for the initial experiment; neither establishes
  action completion.

## Splash comparison and unseen frames

A follow-up compared the installed `incoai/Qwen3.8-27B-Splash` 4-bit model on
Splash runtime `1.3.0` with Qwen3-VL 2B on MLX `1.14.0`. Both used 8192-token
contexts, parallel setting four, and serialized requests. These are synthetic
inference measurements; capture, MCP and controller time are excluded.

The modal-only comparison used new fixture indices 1000–1008 at 960 pixels and
2000–2008 at 1920 pixels for each model. First requests are separate from eight
warm changing frames, containing two modal positives and six negatives. All
four cells returned valid final-content JSON with eight correct decisions and
zero false accepts or rejects.

| Model | Image | First ms | Warm median ms | Warm range ms |
|---|---|---:|---:|---|
| Qwen3-VL 2B, MLX 4-bit | 960×540 | 1039.7 | 381.3 | 379.3–387.3 |
| Qwen3-VL 2B, MLX 4-bit | 1920×1080 | 1393.4 | 1348.4 | 1344.6–1364.3 |
| Qwen3.8 27B Splash, 4-bit | 960×540 | 2406.5 | 2154.3 | 2145.2–2169.0 |
| Qwen3.8 27B Splash, 4-bit | 1920×1080 | 6317.3 | 6284.8 | 6257.3–6330.3 |

Splash also extracted all four state fields correctly in five warm trials per
size, with medians 2160.4/6559.8 ms at 960/1920 pixels. Its initial three-condition
series scored 36/36 at 960 pixels and 15/15 at 1920 pixels. Those earlier cells
reused fixture images across tasks. An exact repeated first state request took
527.6 ms, while the following changing requests took 2151.1–2346.0 ms. Treat
that repeated request as a cache observation; use the unseen-frame comparison
for latency planning.

Both models accepted strict JSON schema output at `/v1/chat/completions`,
temperature zero and a 32-token predicate cap. Splash returned normal content
with `reasoning_effort: "none"`; configure this through
`-vision-request-json '{"reasoning_effort":"none"}'`. Its 1920-pixel inference
alone exceeded five seconds, so the observation budget must allow for that.
Splash loading took 16.01 seconds and its first subsequent inference 3259.6 ms;
Qwen reloading took 3.00 seconds. Loading is excluded from the table, and these
first requests are not controlled cold-start distributions.

For these small modal tests, the 2B model provides the shorter inference cycle.
The simple fixtures and prompt defining a modal versus a page card do not
establish accuracy on arbitrary applications. The
[comparison record](validation/cycle-time-splash-2026-11-23.json) preserves all
106 responses, first requests, expectations, settings and per-run source hashes.
Use [splash_bench.py](../experiments/cycle-time/splash_bench.py) with a new
`--fixture-offset` and `--condition modal_visible` to repeat the unseen-image
comparison without changing the original benchmark scripts.
