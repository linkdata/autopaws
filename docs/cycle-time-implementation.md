# Visual waits and complete cycle measurements

MCP now reads target coordinate dimensions directly from `capture_frame`,
removing capture-device discovery from the screenshot path. Keyboard and mouse
tools also accept a caller-supplied `wait_for` condition. After input succeeds,
MCP checks screenshots with a configured OpenAI-compatible vision service and
returns the original image that was evaluated, plus structured observation
metadata. [Configuration and examples](../README.md#input-with-a-visual-condition)
cover endpoint, model, bearer authentication and provider-specific options.

An older daemon remains compatible: when both target dimensions are absent,
MCP falls back to `get_device_info`. Partial or invalid dimensions are rejected.
Callers without `wait_for` keep the previous action response and make no vision
or capture requests. Observation errors preserve the successful input result;
an observation timeout must not cause an automatic input replay.

## Benchmark method

The baseline is the unmodified `27cd43e` MCP binary. Complete MCP cycles compare
the baseline's external action → screenshot → vision-check loop with a single
native input-and-wait call. Both use the same real model, prompt and request
settings. A local synthetic KVM source supplies deterministic UI transitions
and calibrated delays of 47 ms for input, 58 ms for capture and 133 ms for device
discovery. These are complete stdio/TCP/HTTP/model/image-return cycles, rather
than sums of separately measured component medians. They do not establish
physical target response times or arbitrary controller-agent task performance.

Each timed path/trial uses new fixture indices, alternating baseline/native
order. Identical frames can repeat within a wait and are identified in the
record. Startup, warmup and draining canceled provider requests are outside
timed samples. The checked-image assertions verify input executes once,
observation status matches the source trace, and the returned JPEG is the last
successfully evaluated image. A transient case changes the source immediately
when its positive screenshot is submitted to the model; returning that checked
image is valid even though the source has changed again by response time.

All model imagery is generated. Windows capture testing retains only actions,
outcomes, timings and capture/host metadata, consistent with the target's
read-only testing limits.

## Complete MCP cycles

All 64 timed cycles passed their input, status and checked-image assertions.
The 2B comparison used five samples per scenario/path; Splash used three.
Times are host wall-clock medians in milliseconds, measured from submitting
the action to receiving the checked image and observation result.

| Model and scenario | Baseline external loop | Native wait_for |
|---|---:|---:|
| Qwen 2B, condition already true | 647.4 | 517.6 |
| Qwen 2B, positive after 500 ms | 1255.7 | 960.7 |
| Qwen 2B, transient positive | 1258.3 | 969.3 |
| Qwen 2B, never true, 3000 ms budget | 3053.3 | 3054.5 |
| Splash 27B, condition already true | 2336.7 | 2206.2 |
| Splash 27B, positive after 500 ms | 4642.7 | 4358.4 |
| Splash 27B, transient positive | 4860.7 | 4403.8 |
| Splash 27B, never true, 8000 ms budget | 8056.0 | 8055.7 |

The 2B ready/delayed cycles improved by about 20%/23%. Timeout wall time includes
the 47 ms synthetic input before the observation budget begins. These core
matrices use the same model in both paths, isolating the screenshot and MCP
orchestration changes. They exclude main-controller planning and an additional
final main-model verification. Small samples describe this run, not production
tail latency or an accuracy guarantee. Records:
[Qwen](validation/cycle-time-complete-qwen-2026-11-23.json),
[Splash](validation/cycle-time-complete-splash-2026-11-23.json).

An additional [eight-cycle regression smoke](validation/cycle-time-review-smoke-2026-11-23.json)
passed on the revised implementation. Its runner records its checksum at
startup and asserts that the script remains unchanged during execution.
The earlier records explicitly label checksums sampled after their runs;
those checksums do not assert the exact executed runner version.

## Controller reaction cycles

A second comparison includes a constrained main-model reaction. The baseline
uses Splash 27B to evaluate every screenshot from the old MCP until the
condition is true. The changed path uses Qwen 2B inside `wait_for`, then sends
the returned checked screenshot to Splash once for final verification. Both
use the same visual condition and provider settings. The fixed keyboard action
is submitted at the start of the timer; command planning and generating the
condition are outside the measurement.

| Scenario | Baseline Splash controller ms | Qwen helper plus Splash verification ms |
|---|---:|---:|
| Condition already true | 2350.4 | 2606.3 |
| Positive after 500 ms | 4634.3 | 3046.3 |

All twelve cycles passed, with three samples per scenario/path and fresh images.
The helper added about 256 ms when the first screen already met the condition.
For the delayed transition it saved about 1588 ms, or 34%, by replacing a costly
main-model polling turn with small-model checks. The checked image reached the
native caller in median 535/984 ms, before the final verification step.
Use the helper for expected transitions; it is not an unconditional speedup.

These are real model reactions on controlled synthetic UI, with simulated KVM
delays. They are neither full autonomous GUI tasks nor measured physical
target-to-controller cycles. The
[controller record](validation/cycle-time-complete-controller-2026-11-23.json)
includes image delivery, completed checks, controller decisions and raw timings.

## Physical screenshot optimization

With the physical UGREEN/AVFoundation setup, twenty warm snapshots per MCP
client measured:

| Client | Median ms | p95 ms |
|---|---:|---:|
| Baseline MCP | 198.4 | 211.1 |
| Optimized MCP | 66.9 | 77.2 |

The median fell by 131.5 ms, about 66%. Both clients used the same optimized
daemon and unchanged capture pipeline; the old client still requested device
discovery after every snapshot. Every capture passed. No model inference ran
concurrently, and no target input, file/setting changes, screenshot storage or
OCR were involved. This validates capture transport on the connected Windows
target, while semantic wait tests use the generated source described above.
The [physical record](validation/cycle-time-physical-optimization-2026-11-23.json)
contains raw timings and binary hashes.

## Model selection

The installed Qwen3.8 27B Splash model works with JSON-schema output and
`-vision-request-json '{"reasoning_effort":"none"}'`. Its Splash 1.3.0 engine
was slower than Qwen3-VL 2B on these image conditions, despite its fast engine:

| Model | Unseen image | Warm n | Median check ms | Correct |
|---|---|---:|---:|---|
| Qwen3-VL 2B, MLX 4-bit | 960×540 | 8 | 381.3 | 8/8 |
| Qwen3-VL 2B, MLX 4-bit | 1920×1080 | 8 | 1348.4 | 8/8 |
| Qwen3.8 27B Splash | 960×540 | 8 | 2154.3 | 8/8 |
| Qwen3.8 27B Splash | 1920×1080 | 8 | 6284.8 | 8/8 |

These modal tests have two positives and six negatives per warm cell. Other
small synthetic tests found predicate errors with the 2B model, while Splash
passed the tested status and numeric conditions. That is a reason to choose
models by condition accuracy as well as speed, not a general accuracy ranking.
See the [Splash record](validation/cycle-time-splash-2026-11-23.json) and
[initial predicate tests](validation/cycle-time-predicates-2026-11-23.json).

An exact repeated Splash image took 528 ms, but new 960-pixel images took over
two seconds. Cached-image results are separate from the table above. Model
loading is also separate: Splash took 16.01 seconds, and the 2B model about
three seconds. Keep the chosen model resident for steady-state latency.

The default analysis width is 960; the returned screenshot retains its original
resolution and establishes mouse coordinates. The complete-cycle fixture tests
at 960 preserve original JPEG bytes. Earlier standalone fixtures used Lanczos
resize and JPEG quality 90; production downsizing uses nearest-neighbor resize
and JPEG quality 85. Standalone model accuracy therefore does not validate
every production downsize or real application. Explicit visual conditions and
main-model verification of the returned image remain important. Numeric
conditions can be more reliable when a value is extracted and compared in code.

## Reproduction

Build both commands from the baseline revision and the changed revision in
separate checkouts. Keep their executable paths distinct. With the desired
model loaded in LM Studio on port 11434:

```sh
python3 -u experiments/cycle-time/complete_cycle_bench.py \
  --baseline-mcp /path/to/baseline/autopaws-mcp \
  --current-mcp /path/to/changed/autopaws-mcp \
  --model qwen3-vl-2b-instruct --samples 5 --timeout-ms 3000 \
  --out /private/tmp/complete-qwen.json
```

Use a new fixture offset for each run to avoid previously seen images. For
Splash, select `qwen3.8-27b-splash`, allow a larger timeout, and pass
`--provider-options-json '{"reasoning_effort":"none"}'`. A full-resolution
Splash check can exceed the default five-second observation budget by itself.
For the helper/controller comparison, add
`--controller-model qwen3.8-27b-splash --timeout-ms 8000`; it runs only the two
positive scenarios and includes the final controller verification.

The initial [research report](cycle-time.md) and its raw records remain as the
pre-implementation baseline. The product adds no model runtime dependency;
vision runs through the configured HTTP service in MCP alone.
