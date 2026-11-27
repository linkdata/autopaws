# Performance measurements

These measurements describe specific hardware and synthetic fixtures from
23 November 2026. They are not latency or model-accuracy guarantees.
See [MCP visual waits](mcp.md#input-with-a-visual-condition) for current behavior
and [browser audio measurement](audio-measurement.md) for audio timing.

## Physical capture

An Apple M3 Ultra host used UGREEN 15389/AVFoundation capture at 1920×1080,
CH340/CH9329 at 9600 baud, with audio and autocrop disabled. The Windows target
received no scripts or file/setting changes; no screenshots or OCR were retained.

| Operation | Samples | Median ms | p95 ms |
|---|---:|---:|---:|
| KVM warm `capture_frame` | 30 | 57.9 | 72.3 |
| KVM `get_device_info` | 10 | 132.6 | 186.9 |
| Baseline MCP warm `capture_screen` | 20 | 198.4 | 211.1 |
| Optimized MCP warm `capture_screen` | 20 | 66.9 | 77.2 |

The optimized MCP reads target dimensions from `capture_frame`, avoiding device
discovery after each screenshot. Both MCP clients used the same daemon. Older
daemons without target dimensions use a `get_device_info` fallback.
The first two rows and MCP comparison are separate runs. Full setup, raw timings
and binary hashes: [component measurements](validation/cycle-time-2026-11-23.json)
and [capture comparison](validation/cycle-time-physical-optimization-2026-11-23.json).
A fresh frame does not prove the target finished processing input.

## Complete visual-wait cycles

These tests timed complete MCP input, capture, HTTP inference and image-return
cycles using generated UI transitions. The baseline was commit `27cd43e` with
an external action/capture/check loop; the comparison used native `wait_for`.
Both paths used the same model and prompt, with simulated input/capture/discovery
delays of 47/58/133 ms. Controller planning was excluded.

| Model and scenario | External loop median ms | Native wait median ms |
|---|---:|---:|
| Qwen3-VL 2B, already true | 647.4 | 517.6 |
| Qwen3-VL 2B, true after 500 ms | 1255.7 | 960.7 |
| Qwen3-VL 2B, never true, 3000 ms budget | 3053.3 | 3054.5 |
| Qwen3.8 27B Splash, already true | 2336.7 | 2206.2 |
| Qwen3.8 27B Splash, true after 500 ms | 4642.7 | 4358.4 |
| Qwen3.8 27B Splash, never true, 8000 ms budget | 8056.0 | 8055.7 |

All 64 timed cycles, including transient-positive cases, passed input-once,
status and checked-image assertions. Qwen used five samples per scenario/path;
Splash used three. Timeout wall time includes input before the observation budget.
Records: [Qwen](validation/cycle-time-complete-qwen-2026-11-23.json) and
[Splash](validation/cycle-time-complete-splash-2026-11-23.json). A separate
eight-cycle [regression smoke](validation/cycle-time-review-smoke-2026-11-23.json)
also passed.

With one final Splash verification, Qwen helper checks reduced the delayed-cycle
median from 4634.3 to 3046.3 ms, but increased the already-true median from
2350.4 to 2606.3 ms. Each path/scenario had three samples. This was a constrained
[controller comparison](validation/cycle-time-complete-controller-2026-11-23.json),
not a complete autonomous task or physical target response measurement.

## Model checks

Eight warm checks per cell used unseen generated modal-dialog images:

| Model | Image | Median ms | Correct |
|---|---|---:|---:|
| Qwen3-VL 2B, MLX 4-bit | 960×540 | 381.3 | 8/8 |
| Qwen3-VL 2B, MLX 4-bit | 1920×1080 | 1348.4 | 8/8 |
| Qwen3.8 27B Splash, 4-bit | 960×540 | 2154.3 | 8/8 |
| Qwen3.8 27B Splash, 4-bit | 1920×1080 | 6284.8 | 8/8 |

Small synthetic fixtures do not establish accuracy on arbitrary applications.
Separate Qwen tests falsely accepted the status condition on 1 of 12 frames
and falsely rejected one numeric condition on 5 of 12 frames. Verify returned images; extract numbers and compare them
in code when possible. Model loading, first inference and repeated-image cache
results are excluded from warm timings. Keep the model loaded for steady-state
latency measurements. Full-resolution Splash checks can exceed
the default five-second observation budget.

Standalone fixtures used Lanczos resize/JPEG quality 90; production downsizing
uses nearest-neighbor/quality 85. The complete-cycle tests preserved original
960-pixel JPEGs, so these results do not validate every production downsize.
Records: [modal comparison](validation/cycle-time-splash-2026-11-23.json),
[predicates](validation/cycle-time-predicates-2026-11-23.json) and
[state extraction](validation/cycle-time-models-2026-11-23.json).

## Reproduce

The [benchmark scripts](../experiments/cycle-time/) require Python 3, Pillow and
NumPy. Build baseline and current MCP binaries in separate checkouts. With a
compatible vision service running on port 11434 and the selected model loaded:

```sh
python3 -u experiments/cycle-time/complete_cycle_bench.py \
  --baseline-mcp /path/to/baseline/autopaws-mcp \
  --current-mcp /path/to/current/autopaws-mcp \
  --model qwen3-vl-2b-instruct --samples 5 --timeout-ms 3000 \
  --out /tmp/complete-qwen.json
```

Use a new `--fixture-offset` for each run. On non-macOS hosts, pass
`--font /path/to/font.ttf` to this command. For the Splash comparison, replace
the model with `qwen3.8-27b-splash`, samples with `3`, and timeout with `8000`;
also pass `--provider-options-json '{"reasoning_effort":"none"}'`.
For the helper/controller comparison, keep the Qwen model, add
`--controller-model qwen3.8-27b-splash`, set timeout to `8000`, and use the same
provider options; this runs only the two positive scenarios.

For physical scripts, pass `--api 127.0.0.1:9329` to use a default daemon and
`--mcp /path/to/autopaws-mcp` for MCP checks. Their defaults use port 19329 and
a `/private/tmp` executable path. Cold capture requires no other video demand.

`kvm_bench.py` measures physical snapshots without input. `action_bench.py`
requires `--allow-read-only-input` and an owner-prepared target where its
shortcuts and pointer movements are safe; it does not verify the target OS.
Keep capture logging disabled and use one operator. Model-only scripts generate
their own images.
