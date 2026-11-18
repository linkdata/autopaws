# Hardware validation

This record distinguishes physical observations from builds and unit tests.
Development checks precede the completed required Linux core matrix below.
Browser, serial and capture/audio recovery results retain initial failures and
retests; individual component checks do not establish the entire gate alone.
Recorded commands and archived transcripts retain the CLI used for each run.
Earlier builds used subcommands; the current CLI starts the daemon with
`autopaws-kvm` and uses `--version` and `--list-devices` flags.

## Input development checks, 2026-11-17

- Linux amd64 development host; CH340 `1a86:7523` on `/dev/ttyUSB0`, 9600 baud.
- Dedicated Linux target running GNOME and Firefox. Exact versions are listed in
  the release configuration below.
- UGREEN U15389/MACROSILICON HDMI capture, `2b89:5389`, `/dev/video0`.
- Mirrored or single target display, as confirmed by the owner. Fullscreen
  target-browser viewport: 1280×720, device pixel ratio 1. Reference
  capture: MJPG at 1920×1080, 30 fps, scaling that target image.
- The Go keyboard opened a local `data:text/html` event recorder. No credentials
  or application data were entered. A temporary host HTTP page was unreachable
  from the target, so the final measurements used the local page and HDMI.
- Setup frames and reported manual trackpad activity were excluded. Each
  controlled run reloaded the page, then used only the Go test harness.

### Relative-mode drag

The production `Mouse.Drag` moved from (200,200) to (800,500). Its absolute move
carried the held left-button bit. The page recorded this complete sequence:

| Event | Buttons | Target viewport position |
|---|---:|---|
| mousemove | 0 | (200,200) |
| mousedown | 1 | (200,200) |
| mousemove | 1 | (800,500) |
| mouseup | 0 | (800,500) |

A second run used the same down/up sequence with the move's absolute button bits
cleared, matching the older browser viewer. It produced the same four events.
Both variants preserved a continuous drag on this target. Keep the Go API's
held-button movement behavior and route the new browser movement through that
same HID service, so API/MCP/browser paths agree. No relative-drag correction is
required by this observation.

### Button-mode compatibility

One run clicked (400,300) using absolute mode, then (600,300) using relative mode.
The page recorded only a movement at (400,300), followed by movement, mousedown
and mouseup at (600,300). Absolute button changes were ignored; relative button
reports produced the click. This is the Linux compatibility case required for
the drag experiment above.

The reference OpenCV capture initially returned noisy or outdated-looking setup
frames. Continuous 12-second capture produced clear, current event logs. Capture
startup and latency need their own checks in the video and release work; a
single initial reference frame was not used to decide the input result.

### API daemon and client

The static Go daemon was started with an ephemeral loopback API listener,
`/dev/ttyUSB0`, target size 1280×720 and default relative button mode. The portable
Go client queried `get_device_info`, verified those settings, sent Ctrl+R to reset
the local page, then called one `mouse_drag` from (200,200) to (900,550), with no
per-call button-mode override. The fresh captured page recorded:

| Event | Buttons | Target viewport position |
|---|---:|---|
| mousemove | 0 | (200,200) |
| mousedown | 1 | (200,200) |
| mousemove | 1 | (900,550) |
| mouseup | 0 | (900,550) |

The daemon then received SIGTERM and exited 0. This validates CLI setting wiring,
Go client/TCP API dispatch, physical keyboard/mouse control and ordinary signal
shutdown together. Separate recording-transport tests cover active cancellation,
shared cleanup deadlines and slow kernel-operation ownership; this physical run
does not claim a universal shutdown bound or complete the media/MCP release gate.

### Actual stdio MCP process

The SDK client launched the static `autopaws-mcp` binary over its real stdin/stdout
pipes, connected to the configured daemon above, and listed all twelve non-OCR
tools. `capture_screen` returned an MCP tool error while capture is unavailable;
an absolute move before any successful screenshot was rejected. The client then
sent Ctrl+R, an unscaled relative move (-20,-10), and a click at the current
pointer. The target page recorded movement followed by one down/up pair, at
(892,546). Relative HID deltas are subject to the target OS's pointer processing.

Closing the MCP client's stdin produced MCP process exit 0. The daemon then
exited 0 on SIGTERM. This covers the actual stdio/CLI process boundary as well as
physical input. Screenshot-to-click validation was pending at this stage;
the shared-pipeline checks below cover it with real capture.

## V4L2 development checks, 2026-11-17

The production Go package opened `/dev/video0` on the same UGREEN card. Read-only
format queries reported YUYV and MJPG. Requested MJPG at 1920×1080 was accepted,
with a 4,147,200-byte mapped buffer capacity. No frame-rate setting was applied.

The initial run exposed a real startup ordering bug: registering the fd with
Go's event poller before `STREAMON` cached the driver's pre-stream `EPOLLERR`.
A syscall trace confirmed that no `DQBUF` was reached. Setup now keeps sole
ownership of an unpublished nonblocking descriptor and transfers it to Go's
poller after `STREAMON`. Both subsequent capture runs succeeded.

- Opening took about 0.67 seconds; the first delivered frame arrived about
  0.92 seconds after the request.
- An eight-second run copied 469 frames, sequences 0 through 468, with no gaps.
  After the first second, 420 sequence intervals covered 7.002 seconds, about
  60 fps. Only nine sampled frames were decoded by the test harness, avoiding
  the CPU cost of decoding every frame.
- Frames carried flags `0x12001`, identifying `CLOCK_MONOTONIC` timestamps at
  start of exposure. Compressed sizes observed across cold starts ranged from
  41,109 to 805,626 bytes, within mapped capacity. All sampled JPEGs decoded
  without Huffman-table repair.
- A separate eight-second run decoded all 383 delivered frames successfully.
  Decoding each frame reduced throughput and produced sequence gaps, which is
  why the normal viewer path must avoid a full decode on every frame.
- Initial decodable pictures varied between black and noisy source-acquisition
  images; the current target page was visible by roughly 3.7 seconds in these
  runs. Capture timestamps alone do not prove that HDMI content has stabilized.
  No fixed warmup delay or content filter was added: a black or noisy target can
  be legitimate content.
- Both streams closed successfully, and a read after close returned the
  matchable closed error. No capture or input helper was left running.

Fake/pipe-backed tests separately exercise cancellation, a blocked-read close,
buffer ownership, partial setup failures and descriptor errors under the race
detector. The pipeline checks below cover selection/reopen and fresh API/MCP
snapshots; physical device-loss recovery remains a release check.

## Device discovery development check, 2026-11-17

The production `autopaws-kvm list-devices` command completed successfully against
the attached hardware without starting video/audio or opening serial transport:

- It selected CH340 `/dev/ttyUSB0`, USB `1a86:7523`, port `1-13`.
- It selected UGREEN `/dev/video0`, USB `2b89:5389`, port `4-1`, and listed YUYV
  and MJPG. The metadata sibling `/dev/video1` was omitted.
- Webcam capture nodes `/dev/video2` and `/dev/video4` remained visible, with
  their respective MJPG/YUYV and GREY formats. Their metadata siblings were
  omitted, and the webcam-name rule preferred the HDMI card.
- It matched `/dev/snd/pcmC1D0c` (`U15389`) to the selected video's same USB
  ancestor. Non-USB `/dev/snd/pcmC0D1c` and `/dev/snd/pcmC0D4c` were listed and
  remained unselected.

Fake-tree tests separately cover two identical USB capture cards and selecting
each one's own audio. Only one physical HDMI capture card is attached here.

## Shared capture and MCP development check, 2026-11-17

The static Go daemon started with automatic video selection, an ephemeral
loopback API, `/dev/ttyUSB0`, target coordinates 1280×720 and relative button mode.
The actual stdio MCP binary connected through the SDK client. Before any capture,
device information selected `/dev/video0` with streaming idle, and an absolute
mouse action was correctly rejected for lacking a screenshot.

The client reset the target's local event page through Ctrl+R and requested a
real screenshot. The initial 1920×1080 capture took 2.377 seconds including source
startup, within the five-second acquisition budget. Four seconds later the warm
capture took 88 ms. Clicking screenshot pixel (960,540) then produced these
target-page events, visible in a subsequent real MCP JPEG:

| Event | Buttons | Target viewport position |
|---|---:|---|
| mousemove | 0 | (640,360) |
| mousedown | 1 | (640,360) |
| mouseup | 0 | (640,360) |

This exercises screenshot-to-target scaling through MCP, API and physical HID.
The next capture took 85 ms. These are sample call times, including the MCP/API
path; they do not establish a general latency bound or target-app readiness.

Changing capture resolution to 1280×720 invalidated the session's old screenshot
coordinates; an absolute move was rejected until the next capture. Subsequent
JPEGs reported 1280×720. One MCP `mouse_drag` then produced:

| Event | Buttons | Target viewport position |
|---|---:|---|
| mousemove | 0 | (320,180) |
| mousedown | 1 | (320,180) |
| mousemove | 1 | (960,540) |
| mouseup | 0 | (960,540) |

The client next selected a deliberately missing capture path. Selection remained
repairable and its capture request returned `unavailable`. Selecting `/dev/video0`
again restored screenshots at the retained 1280×720 size and again required a
new screenshot before absolute input. The recovered JPEG showed the same completed
drag log. Resized/recovered sample captures took 42–88 ms on this run.

Closing stdin produced MCP exit 0; SIGTERM produced daemon exit 0 with video still
recently active. No helper or capture process remained. This used one physical
card and proves a failed-path repair, rather than unplug/replug recovery or a
second card model. Cold letterbox cropping, stale/corrupt queued frames, blocked
kernel ownership, concurrent retries and generation-switch races are separately
covered by fake-source and loopback tests.

## WebSocket development check, 2026-11-17

The static daemon started both API and web listeners on ephemeral loopback ports,
with the same physical serial/capture devices and relative button mode. A real
Go WebSocket client checked configuration, the 64-byte video header, increasing
per-connection delivery sequences, monotonic timestamps and 1920×1080 JPEG/crop
metadata. It received the current target picture through the shared pipeline.

One passive viewer and one controlling connection remained connected together.
The controlling connection sent one complete Ctrl+R physical-key shortcut, then
pressed at target (320,180). The passive viewer disconnected while that button
was held. Movement to (960,540) and release produced exactly:

| Event | Buttons | Target viewport position |
|---|---:|---|
| mousemove | 0 | (320,180) |
| mousedown | 1 | (320,180) |
| mousemove | 1 | (960,540) |
| mouseup | 0 | (960,540) |

The recorded picture confirms that passive disconnect did not release another
connection's held button. This tests multiple viewers with one input operator;
it does not establish concurrent-operator support.

A new passive observer then watched the controlling connection press at
(500,350) and disconnect without an up message. The subsequent WebSocket JPEG
showed movement, mousedown with buttons 1, then mouseup with buttons 0 at that
position. Finally SIGTERM closed the still-connected observer's socket, and the
daemon exited 0. All physical helper processes stopped.

Separate fake/loopback tests verify shared API/shortcut serialization, exact
cleanup-deadline handoff, retained kernel ownership, listener-failure cleanup,
slow-client isolation and source-generation flushing. The complete browser
interface and its browser-runtime checks are tracked separately.


## Browser client development checks, 2026-11-17

A loopback-only fixture served the actual embedded client with the production
WebSocket server, capture manager and HID coordinator. Generated 1280×800 JPEGs
contained a 1280×720 or 1120×640 visible rectangle, labels and black borders;
transport and capture devices were fakes. These checks exercised a real Chromium
browser without sending input to the physical target.

- All four visible image corners produced exactly `(0,0)`, `(4095,0)`,
  `(0,4095)` and `(4095,4095)` in the server's received input and encoded HID
  reports. Integer CSS origins and dimensions make endpoint pixels reachable.
- View-only mode disabled shortcuts and sent no input for clicks or keys.
  Both shortcut buttons produced one complete chord message each.
- Fit/original-size and cursor toggles changed the actual canvas geometry and
  cursor style. Fullscreen, toolbar dragging, auto-hide and top-edge reveal
  worked. Keyboard Lock was exposed but denied by this browser; the viewer
  displayed the fallback explanation and kept toolbar shortcuts available.
- A source change was triggered only after the server observed a held mouse
  press. The client sent `release_all` on the new generation and suppressed the
  later mouseup, with matching relative-button release in the HID trace.
- Restarting the fixture reconnected the viewer while preserving view-only mode.
  No held input was replayed. No browser console warnings or errors appeared.

The built-in Node tests separately exercise slow/deferred image decoding,
generation and sequence rejection, focus/visibility cleanup, multibutton input,
ordered movement coalescing and optional-capability fallbacks. They are not a
substitute for browser-engine validation. Firefox and the remaining release
checks are tracked in the release matrix.

With the real CH340 transport at 9600 baud, 120 complete warm absolute moves took
1.994 seconds: mean 16.616 ms, median 16.653 ms, p95 17.080 ms and maximum
17.516 ms (60.18 moves/second). The browser sends coalesced movement at 30 Hz,
leaving serial capacity for keys and button transitions. This is a measured
configuration choice, not a universal transport throughput guarantee.


## MCP OCR development check, 2026-11-17

The OCR-enabled static MCP command connected over stdio to the real daemon API
and UGREEN HDMI capture source, with local Tesseract 5.5.0. It offered fourteen
tools and called the read-only `get_screen_text`; no keyboard or mouse event was
sent. After the card's initial acquisition frames, the third fresh capture read
the controlled target's `INPUT TEST` heading, 1280×720/DPR1 viewport and all seven
recorded mouse events. Recognition completed 4.709 seconds after the first OCR
request; the initial two valid acquisition pictures returned empty text. This
observes the existing HDMI acquisition behavior rather than imposing a content
filter or startup delay. MCP stdin EOF and daemon SIGTERM both exited zero.

The required controlled corpus additionally compares four generated image styles
against the original Python preprocessing. Both produced identical recognized
text with Tesseract 5.5.0, scoring 240/241 characters by the documented edit
metric. Process cancellation/timeout/reaping, command ordering and waits, optional
registration and unchanged screenshot-coordinate state are covered with owned
helper-process and real loopback MCP/API tests. No target image is part of the
committed corpus, and the synthetic PowerShell style does not claim Windows
runtime testing.


## Raw ALSA development checks, 2026-11-17

The explicit hardware tests selected `/dev/snd/pcmC1D0c`, the capture PCM belonging
to MACROSILICON UGREEN 15389 on USB path `4-1`. `/proc/asound` identified one USB
capture substream; no other process held it at test start, so no PipeWire or
WirePlumber configuration change was needed.

- Negotiation returned S16_LE, 48,000 Hz, two channels, 960-frame/20 ms periods
  and a 7,680-frame/160 ms kernel buffer. At least 12,480 frames were read with
  owned PCM bytes and positive monotonic first-sample timestamp estimates.
- A second nonblocking open returned the matchable busy error in 231.913 µs,
  within the 100 ms acceptance bound.
- DROP and joined descriptor shutdown completed in 1.078 ms on this hardware.
  This observation does not impose a universal bound on Linux driver calls.
- Deliberately stalling the real reader beyond its kernel buffer produced one
  overrun. PREPARE recovered capture, incremented the generation from 2 to 3,
  flushed old samples and delivered a new-generation block.

Fake-kernel tests separately cover terminal notification while close retains
ownership, cancellation between read admission and DROP, suspend recovery,
100 ms period usability, exact stale-expiry boundaries, duration queue bounds and
subscriber independence. GCC measurements verify 89 LP64 UAPI values on amd64;
arm64 is cross-built, with no claim of separate ARM hardware execution. Browser
playback, end-to-end timing and the ten-minute audio run remain separate checks.


## Release validation draft, 2026-11-17

**Required Linux core checks completed, 2026-11-18.** This section records the
scoped observations, initial failures and fixed-build retests. Tagged release
artifacts are checked separately; these results do not assert universal latency
or browser compatibility.
The owner-scoped release checks use the available Linux host and target. The
additional owner-required limited read-only Windows laptop smoke also passed,
as recorded below. Broader Windows/macOS target tests and ARM64 physical hardware
remain outside the required scope. Chromium and Firefox are the required
available browser engines; Safari and other platform browsers are optional.
Keep supported cross-builds.

### Current setup and build identity

| Item | Recorded setup |
|---|---|
| KVM/development host | Dell XPS 17 9700, Linux amd64, Ubuntu 26.04.1 LTS, kernel 7.0.0-43-generic |
| Target | ThinkPad T14 Gen 1, Ubuntu 26.04.1 LTS, kernel 7.0.0-43-generic, GNOME |
| Target display | Owner-confirmed mirrored/single output, fullscreen 1280×720 logical viewport, DPR 1 |
| Serial input | CH340/CH9329, USB `1a86:7523`, `/dev/ttyUSB0`, 9600 baud, relative button mode |
| HDMI capture/audio | UGREEN U15389/MACROSILICON `2b89:5389`, USB path `4-1`; `/dev/video0`, `/dev/snd/pcmC1D0c` |
| Video reference | MJPG 1920×1080; native driver rate, viewer limit 20 fps; negotiated source metadata and dimensions were checked during capture/WS/MCP runs |
| Negotiated audio | S16_LE, 48,000 Hz stereo, 960-frame/20 ms periods, 7,680-frame/160 ms kernel buffer |
| Exclusive PCM access | No competing PCM owner at test start; no PipeWire/WirePlumber policy change applied |
| Browser under validation | Linux Chromium 157 and native headless Firefox 154.0; exact user agents are retained with their evidence. The Firefox checks below do not claim a second ten-minute latency run. |
| Build under validation | Audio implementation merged in [PR 61](https://github.com/linkdata/autopaws/pull/61); observed module hashes are in [the compact result](validation/audio-2026-11-17.json). The subsequent token-enabled smoke used the same audio DSP code. Tagged artifacts and checksums are verified separately through the release procedure. |

The earlier input section's unspecified OS versions describe the same lab setup;
this table records the subsequently obtained exact versions. It does not turn
historical component observations into a completed release matrix.

### Current core evidence

| Check | Result/evidence |
|---|---|
| Linux keyboard/API input and relative drag | Observed on real hardware. Latest Ctrl+R, move `(120,110)` and drag `(240,180)` → `(1040,560)` produced the expected five mouse events in a fresh OCR read; no coordinate code change was required. Earlier screenshot-to-click and scaled drag evidence is above. |
| 9600-baud timing, exclusivity and cancellation | Physical flock passed: a second `Port.Prepare` returned `ErrBusy` in 161.209 µs, generation 0, with no input admitted. Successful `Prepare` (open/configure, release-all reset and baud pacing together) took 31.71986 ms; joined `Close` took 518.406 µs. Component tests cover host-queue wait/baud guard and cancellation of application waits. These observations do not impose a universal Linux kernel-call bound. |
| Ordinary shutdown and input release | Earlier actual API/MCP/WebSocket runs exited 0; controlling disconnect released held buttons, while passive disconnect did not. The 2026-11-18 physical serial replug check also released the held left button before fresh input, with no failed-action replay. The Firefox document-close diagnostic and completed backend cleanup are described below. |
| Serial physical unplug/replug | **PASS, 2026-11-18.** The owner removed the host serial USB connection while left was held at `(240,220)`. A move to `(900,550)` while absent returned `unavailable` in 1.509 ms. After replug, the first fresh move to `(500,350)` completed in 51.123 ms; release-all preceded that move, and the target recorded mouseup at `(240,220)` before the new movement, with no `(900,550)` replay. Details below. |
| Capture/audio physical unplug/replug | **PASS after the ALSA progress-guard fix, 2026-11-18.** Initial removal exposed a rapid recovery-generation loop, retained below. On the fixed build, configuration count rose only 2 → 4 (one recovery then unavailable), stayed at 4 while absent and reached 5 after explicit same-path repair. Video generation 2 and 48 kHz stereo audio generation 3 resumed with timestamps after the observed replug and zero config/media ordering errors. Warm target events and fresh capture confirmed useful recovery; shutdown joined all owners and retained the old V4L2 close diagnostic. |
| 1080p MJPG, timestamp/format listing and live size/device repair | Physical component checks above pass; valid acquisition black/noisy images were not content-filtered. Corrupt-frame/DHT normalization is covered synthetically. A native Chromium fixture additionally displayed production-normalized DHT-less input: raw 1280×800 JPEG with zero DHT segments became a decodable one-DHT frame, cropped to `(40,40,1200,720)`. Its displayed corners produced exact fake-HID endpoints 0/4095. This fixture used synthetic source/HID, not another physical card. |
| Real HDMI audio content | Observed: a target-generated quiet 440 Hz tone reached both captured channels for about five seconds. Details below. |
| Real ALSA overrun and same-format playback recovery | Run 4 paused the owned reader for 0.400324 s: generation 2 → 3 retained context 1/node 1, with no new gesture or reported browser error. The final result is recorded below and in the compact JSON. |
| Chromium video/audio, mute/unmute and reconnect | The completed run 4 recorded 644.887 s of mapped output span and 641.419 s of real PCM, including overrun and transport-stall recovery. All measured estimates plus their engineering allowance remained below 200 ms on this run. Component crop/toolbar/denied-lock results are above; this is not an all-browser or acoustic guarantee. |
| Firefox video/audio, mute/unmute/reconnect/crop | **PASS, native headless Firefox 154.0.** The synthetic DHT/crop/input/corner fixture rendered nonzero 48 kHz stereo PCM through its AudioWorklet. A separate physical strict-view-only run decoded 1920×1080 video near 20 fps and received 48 kHz stereo PCM with advancing native worklet clock/ACKs, mute/unmute and reconnect, with zero application input sends. The document-close warning reproduced without product/audio code is retained as a known diagnostic below; this is not an all-warning-free or ten-minute Firefox latency claim. |
| SSH viewer/API access | Token-protected `http://localhost:9330` displayed video near 20 fps and 48 kHz stereo audio; enable/mute/unmute worked without browser errors. The fragment was removed, cookie authentication worked, and API port 9329 rejected missing/wrong tokens. Both ports were forwarded; no secret is in the evidence. |
| AudioWorklet unavailable explanation | Native Chromium loaded the synthetic fixture over a private non-loopback HTTP address. Enabling its advertised audio displayed “AudioWorklet needs a compatible browser and secure context. A localhost SSH tunnel is sufficient.” Video continued at 20 fps; a click and key down/up reached the fake HID controller, with no browser errors. The fixture supplied no PCM and opened no hardware. |
| MCP screenshot coordinates and endpoints | Earlier real screenshot `(960,540)` → target `(640,360)`, resize invalidation and scaled drag pass. Token-enabled MCP again showed center `(640,360)` and drag endpoints `(320,180)` → `(960,540)`; this smoke's first OCR did not establish the complete event sequence. All four physical corners subsequently passed in a local-file Firefox kiosk recorder: screenshot pixels `(0,0)`, `(1919,0)`, `(1919,1079)`, `(0,1079)` produced target `(0,0)`, `(1279,0)`, `(1279,719)`, `(0,719)`, with all twelve move/down/up events. Autocrop was enabled. The harness first waited for the page to finish reloading; API completion alone does not establish target-page readiness. Earlier normal-window attempts hit desktop controls. No coordinate code change was needed. |
| Limited read-only Windows laptop smoke | **PASS, 2026-11-18.** Capture and safe keyboard/pointer actions passed after owner-handled setup. Only actions and pass/fail are recorded; no screen content or images are retained. The agent placed no scripts on the target and changed no target files or settings. Scope and limits are below. |
| Local gate and release packaging | The preparation gate includes actionlint, release-script syntax/ShellCheck, 85 Node cases, Go race/lint and supported cross-builds. Eight CGO-disabled release-scheme binaries are checked locally with a validation version; these are not published release artifacts. Repeat after final release edits and record the actual tagged identity before publication. |

Physical input checks remain one-operator tests. Multiple passive viewers do not
establish concurrent-operator support. The old transient coordinate concern was
rechecked after restarting the same-code daemon and did not reproduce; it is not
recorded as an active product defect.

### HDMI signal content

A 90.001-second metadata-only WebSocket observer received 4,500 audio blocks,
4,320,000 stereo sample frames at 48 kHz, with no missing source blocks. A bounded
quiet tone generated on the target's explicit HDMI output produced 2,200 positive
zero crossings per channel over approximately 5.02 seconds, with a 440 Hz matched
power ratio of approximately 0.999928 in each channel. Peak magnitude was 128 in
signed 16-bit PCM; the valid surrounding stream was quiet. This establishes real
HDMI signal content through capture, not acoustic output latency or loudness.
The target's default speaker route was not changed to infer success.

The observer sent no application input and stored metadata/counters only. Source
lab evidence `real-signal-hdmi-clean.json` is retained alongside the separate
latency harness and its raw metadata in the local
`autopaws-audio-validation-2026-11-17.tar.gz` archive
(626,324 bytes, 62 files). Its SHA256 is
`0b89ebad5daeb51ef70aa6108ebdf6c235517d8343f223f7734eb5cc4bb93717`.
The repository retains [compact result JSON](validation/audio-2026-11-17.json);
the archive contains the exact latency harness/copies and raw runs 1–4, with
no PCM, images or secrets. The passive signal checker source is not included
in that archive.

### Audio timing and diagnostic history

Follow [the measurement procedure](audio-measurement.md) for the timestamp domains,
calibration, output-position interpolation, allowance, gaps and reproducible lab
commands. Application queues are 100 ms server + 50 ms worklet + at most one
50 ms acknowledged transfer. That limit excludes kernel, TCP/WebSocket and
output-device buffering and does not itself prove the 200 ms end-to-end target.

- Historical run 1 measured about 883 seconds with low estimates, but used an
  earlier implementation and is not final-code acceptance.
- Run 2 failed: maximum 555.512 ms, eight quanta above 200 ms and 14 unmapped.
  An audio timeline pause relative to elapsed time was observed, but old raw
  output brackets were absent: the exact physical mechanism and queue replay
  are unproved. This exposed a currentTime-based aging risk; synthetic tests
  validate the retained elapsed-clock and timing-anchor fixes.
- Diagnostic run 3 forced a 500 ms worklet pause. Six already-rendered pre-pause
  quanta mapped near 553 ms; 11 earlier quanta were unmapped. No post-pause stale
  rendering was observed. Already-rendered browser output cannot be recalled;
  this is a downstream output/measurement limitation, not a latency pass.
- Final ordinary run 4 completed and its proxy was stopped and joined. At
  48 kHz stereo it recorded 644.887 seconds between mapped output observations
  and 30,788,089 real sample frames (641.419 seconds). It included the controlled
  0.400324-second ALSA-reader pause, followed later by a roughly two-second proxy
  transport stall and automatic reconnect. That reconnect created context 2/node
  2; Audio and view-only choices were retained, with no reported browser error.

| Run 4 metric | Completed observation |
|---|---:|
| Mapped / unmapped real quanta | 240,541 / 0 |
| Negative / above-200-ms estimates | 0 / 0 |
| Above 200 ms with paired engineering allowance | 0 |
| Minimum / maximum estimate | 66.032 / 102.941 ms |
| 95th percentile, upper 1 ms histogram bin | 87 ms |
| Maximum estimate plus its paired allowance | 115.587 ms |
| Largest individual engineering allowance | 22.444 ms |
| Maximum observed worklet ring | 49.875 ms |
| Underrun quanta / sample frames | 605 / 76,167 |
| Report errors / skipped reports | 0 / 0 |

Underruns and the intentional reconnect gap are retained in these counts;
dropped audio is not counted as fast playback. Context 2 was still running in
the last completed report saved before shutdown. That report state does not
mean a QA process was left running. The [compact result](validation/audio-2026-11-17.json)
retains context/generation history, clock statistics, the historical failures and
precise pre-pause outliers. These source-to-estimated-output observations meet
the stated target on this Chromium run, within the method's limits. They do not
alone establish physical USB recovery or all browser behavior; the later checks
above record that separate evidence.

The subsequent token-enabled MCP smoke completed fresh screenshot, typed-key,
click, drag and OCR calls in approximately 63, 79, 72, 180 and 743 ms respectively;
stdin EOF exited 0. These are sample call times, not general latency bounds.
Earlier dedicated physical checks above establish complete drag sequences;
the first OCR after this newer smoke had not yet shown its final mouseup.

### Physical serial recovery, 2026-11-18

The daemon used automatic serial selection at 9600 baud and relative button
mode. The owner physically disconnected the host serial USB cable while the
CH9329 target-side connection remained in place and left was held at `(240,220)`.
Sysfs recorded `/sys/class/tty/ttyUSB0` disappearing and returning. While absent,
the distinct move to `(900,550)` returned `unavailable` in 1.509 ms, with a write
I/O error and no CH340/CH341 available for release preparation.

After replug, the first fresh move to `(500,350)` completed in 51.123 ms. The
successful transport trace first sent zero-keyboard and zero-relative-mouse
reset reports, then the fresh absolute move. The target recorder showed:

| Event | Buttons | Target position |
|---|---:|---|
| mousemove | 0 | `(240,220)` |
| mousedown | 1 | `(240,220)` |
| mouseup | 0 | `(240,220)` |
| mousemove | 0 | `(500,350)` |

The failed `(900,550)` move was never replayed. This validates recovery and
release before fresh input, without asserting that an unplugged host UART can
release the still-connected target immediately. A passive video/audio observer
reported zero generation/config ordering errors during this serial phase.
Exact records are `serial-summary.json`, `serial-removed.json`,
`serial-returned.json`, `api-events.jsonl` and `serial-phase-observer.json` in the
local `/tmp/autopaws-usb-recovery` evidence directory.

### Firefox 154 browser checks, 2026-11-18

Native headless Firefox 154.0 on the recorded Linux host decoded the
production-normalized DHT-less fixture and its 1200×720 crop. Trusted browser
pointer actions at all four displayed corners reached fake HID endpoints
0/4095. Modifier/button release, view-only input rejection, toolbar shortcuts,
fit/native sizing and cursor controls passed. Native AudioWorklet playback
rendered nonzero quiet synthetic 48 kHz stereo PCM; mute/unmute and reconnect
preserved the intended audio and view-only choices. The application browser-log
collection was empty during these checks.

A separate real-media check enabled strict view-only mode before trusted UI
actions. It decoded 1920×1080 video near 20 fps and received physical 48 kHz
stereo PCM. Native AudioContext time and worklet ACK counts advanced after
initial enable, unmute and reconnect. The physical stream was quiet in the
sampled analysis; this is playback/progress evidence, not another audible-tone
or latency measurement. Every WebSocket application-input send list remained
empty, and no target screenshot was stored. Keyboard Lock was unavailable and
the viewer showed its fallback explanation. Raw results are `native.json` and
`physical.json` in `/tmp/autopaws-firefox-qa`.

**Known Firefox 154 document-close diagnostic.** Forced WebDriver BiDi tab close
and script `window.close()` emitted a script-timeout warning. The same warning
reproduced on a minimal HTTP page containing only a synchronous one-assignment
`pagehide` handler, with no product or audio code. Plain pages without that
handler and minimal-page ordinary navigation-away were clean. Traced viewer teardown entered
and returned from its pagehide handler, and backend cleanup completed. The
comparison reproduced the warning independently of product code; no product
functional defect or useful code change was identified.

`close-probe.json`, `close-probe-minimal.json`, `close-probe-script-close.json`
and their exact scripts/stderr are retained in
`autopaws-firefox-validation-2026-11-18.tar.gz`, SHA256
`37bd0c1d27445c54a39e340e50338f36223cc800c86b2963f885bd6c0ec3be75`
(526,833 bytes, 30 files). It contains the exact fixtures and result metadata,
plus synthetic-only PNGs; no physical-target pictures or PCM are included.
The owned Firefox processes and their synthetic fixture/helper processes were
reaped after the checks.

The viewer navigation-before-close probe additionally retained an `updateLayout`
interruption warning and a RemoteAgent `Realm.sys.mjs` TypeError; it is not
claimed as warning-free. Their exact logs are in the Firefox evidence archive.

### Capture/audio USB recovery and ALSA fix, 2026-11-18

The initial physical UGREEN removal returned `unavailable` for capture and later
restored media after explicit same-path selection. It nevertheless exposed a
real ALSA recovery defect: audio configuration count rose from 2 to 3845, with
generations reaching 10501 in under 0.6 seconds before unavailable. This failed
trial remains in the evidence; eventual video/audio restoration was not accepted
as a pass.

The fix permits one PREPARE without an intervening validated PCM read. Another
EPIPE/ESTRPIPE before valid sample progress ends the source rather than spinning
through recovery generations. Valid PCM re-arms recovery for a later genuine
overrun. The [ALSA notes](../alsa/README.md) describe terminal repair and ownership.

The owner repeated physical removal on the identified fixed build. Configuration
count rose only from 2 to 4 (available generation 2, then unavailable) and remained
4 through the absent interval. Capture returned `unavailable` in 1.674 ms.
Serial input remained independent and released its button. Coordinates differed
while HDMI capture was absent—requested `(400,280)` appeared at `(600,420)`,
consistent with target display reconfiguration; no desktop-resolution query was
taken during absence. This establishes input/release, with restored mapping
checked separately.

The same UGREEN USB port `4-1` and `/dev/video0` returned. Explicit same-path
`set_capture_device` completed in 241.014 ms. Video generation 2 and 48 kHz stereo
audio generation 3 resumed; config count reached 5, both media timestamps were
after the host-monotonic replug observation, and config/media ordering errors
remained zero. The first reopened capture was a valid initial black acquisition
image. A later fresh 1920×1080 JPEG took 61.498 ms, and the settled target recorded
all three move/down/up events exactly at `(520,330)`.

The observer exited 0, the daemon exited 1, both processes were reaped and ports
9329/9330 closed. Exit 1 retains the earlier disconnected V4L2 descriptor's close
`ENODEV` diagnostic; it does not mean ownership was abandoned. Serial zero-keyboard
and zero-relative-mouse releases succeeded. No universal kernel-call cancellation
or shutdown bound is inferred.

The [capture USB record](validation/capture-usb-2026-11-18.json) preserves the
initial failure, fixed binary/source identity, raw metadata and limits. Exact
helpers/metadata are in `autopaws-capture-usb-validation-2026-11-18.tar.gz`, SHA256
`071a7ea668f8c8102d1c426643f5704b221631209fe408be2d32698dcde4a8ac`
(20,698 bytes, 36 files); it contains no physical pictures, PCM, credentials or
binaries. This retest completes the required physical capture/audio recovery row;
it makes no new acoustic or ten-minute latency claim.

### Limited read-only Windows laptop smoke, 2026-11-18

The native Linux daemon used the clean `965b89c` release candidate with
`-autocrop=false` on the host. The API and relative button mode used their
defaults. Capture API metadata reported 1920×1080. The owner prepared a stable
video signal and reconnected the target HID USB after an initial input attempt
had no effect. Post-reconnect checks passed: keyboard modifiers/shortcuts,
Tab/arrow focus navigation, Escape/menu dismissal, pointer movement, positioned
left/right clicks and a repeated click. Testing finished with a return to the
normal idle interface. All owned processes stopped with exit 0.

The agent typed no target text or commands, launched no target applications,
placed no scripts on the target and changed no target files or settings. Only test actions and
pass/fail are recorded; no screen contents, labels, identifiers, screenshots or
OCR are retained. The exact target OS/build and model were not recorded. No
text-layout, dragging, audio or broader Windows compatibility claim is made.
The [sanitized record](validation/windows-readonly-2026-11-18.json) contains only
permitted metadata and test outcomes.

### Optional or explicitly unrequired coverage not run

| Coverage | Status |
|---|---|
| ARM64 physical host | Not run; not required by the current owner scope. Linux arm64 cross-build remains required. |
| Broader Windows target layouts/input and macOS physical targets | Not run; optional beyond the separately recorded limited Windows laptop smoke. Layout fixtures and portable MCP builds are separate evidence. |
| Safari and other platform browser engines | Not run; optional under the current issue 43 scope. No video/audio compatibility claim is made. |
| Second capture chipset / two identical physical cards | Not run; one UGREEN card only. USB-path matching has fake-tree coverage. |
| CH9329 configured for 115200 baud | Not run; physical serial observations use 9600 baud. |
| BIOS/UEFI target | Not run. |
| Claude Code/Desktop launcher/platform matrix; MCP Inspector | Not run as a release matrix; SDK stdio/loopback process tests are documented above. |
| Remote laptop MCP/API use | Not run as a separate client-host check. Localhost SSH forwarding was exercised. |
| Extended cross-platform OCR accuracy/performance | Not run; the required controlled corpus and Linux Tesseract 5.5.0 hardware observation are above. |
| ARM64 CPU/input-latency performance survey | Not run; no ARM64 physical setup is claimed. The measured 9600-baud movement throughput is above. |

Optional unrun coverage must be stated in release notes or linked follow-ups.
Unfinished core checks and any observed wrong/stuck input keep first-release
acceptance open; an optional label does not waive a core failure.

### Native injected-writer recovery and reader check

The separate real-media writer probe inserted a userspace delay in one actual
production JPEG write. The 4,096-byte write had its JPEG marker at offset 76,
waited 201.052 ms and completed in 201.073 ms. The old audio subscription closed
and a new one opened while the PCM owner stayed open once. Chromium kept its
running context 1/node 1, audio generation 1 and enabled audio choice. Across
75.965 seconds it recorded 28,421 mapped real quanta, zero unmapped or above
200 ms, a 91.637 ms maximum estimate and 104.297 ms maximum with paired allowance.
All subscriptions/PCM ownership joined on shutdown. This is an injected writer
wait, separate from physical USB loss or a blocked kernel syscall; see the
[procedure](audio-measurement.md#native-server-writer-recovery-probe).

A reader who did not write the installation section followed its authenticated
private clone, exact CGO-disabled build and isolated-prefix installation on the
existing Linux development setup. Both static native commands were produced;
help/version, strict configuration validation and MCP initialization/EOF passed.
The freshly installed binaries then ran the documented foreground configuration
on the real setup: localhost video/audio near 20 fps, physical keyboard reload,
a 1920×1080 MCP screenshot and a click at target `(640,360)` all worked; MCP EOF
exited zero. No system service or udev rule was installed. Example
rule/unit syntax checks and the real foreground/SSH checks are separate evidence;
this is not a clean-room distribution installation claim.

### Connected cancellation, 2026-11-18

With the serial device connected, the client began a drag at `(720,220)` toward
`(1000,600)`. It closed its TCP connection only after the successful relative
button-down report appeared in the trace, while the action was in its application
wait. The API logged `canceled` and completed host cleanup about 15.455 ms after
the close. The target recorded down then up at `(720,220)`, followed by a fresh
button-free move to `(600,300)`; the cancelled endpoint never appeared. This
observes host cleanup and target event ordering, not a universal kernel or
physical input-latency deadline.

The [2026-11-18 compact record](validation/recovery-firefox-2026-11-18.json)
contains serial, cancellation and Firefox results and archive identities. The
[capture USB record](validation/capture-usb-2026-11-18.json) separately records the
initial ALSA failure and successful fixed-build physical recovery. Required Linux
core validation is complete; final artifacts and publication follow the release
procedure.

### Flattened CLI and default startup, 2026-11-18

The revised binary started with no arguments and listened only on
`127.0.0.1:9329`; the web port stayed closed. Device info reported default
`relative` buttons. The native MCP client also launched with no arguments and
read the physical target through its default connection. `--version`, help and
`--list-devices` worked without starting the daemon.

The bare run used the built-in 1920×1080 target dimensions while Firefox reported
a 1280×720 viewport. Its raw drag/clipping events remain in the evidence; this
run establishes connectivity and released buttons, not exact coordinate mapping.
Viewport size alone does not independently establish desktop geometry or explain
those events. With the previously validated 1280×720 target configuration and the
API/mouse-mode flags still omitted, a fresh page recorded click move/down/up at
`(640,360)` and drag down at `(320,180)`, held movement to `(960,540)`, then release
at that endpoint. Both daemon runs exited 0; helpers exited and test listeners
closed. No new USB or long audio measurement is claimed.

The [CLI/default record](validation/cli-defaults-2026-11-18.json) retains command
arguments, source/binary identity, raw OCR/API metadata and scope. Its local
`autopaws-cli-validation-2026-11-18.tar.gz` archive has SHA256
`695e39b750884765a5e05a2622b0ba2935a50d7a97ec0c4c55031eb3d1af8f62`
(19,031 bytes, 26 files); it contains no physical pictures, PCM, credentials or
binaries.

## Swedish keyboard patch validation, 2026-11-18

The proposed v0.1.1 layout data was tested before merge against the connected
Windows laptop using its standard Swedish layout. A host SDK client launched
the candidate `autopaws-mcp`, which sent every text batch through the candidate
`autopaws-kvm` and physical HID connection. The complete custom Windows map
passed 13 batches covering all 154 printable characters, Tab and Enter, including
all 32 ASCII punctuation characters, `åäöÅÄÖ`, the 49 accented outputs in the
standard composition tables, literal accents, repetition and modifier release.
Four of those accented outputs use their native letter keys in the text map. The original 101-character
subset also passed. The 53 additions had been individually rejected by v0.1.0's
`type_text` before target input.

Only controlled synthetic inputs, pass/fail and permitted host/capture metadata
are recorded. Visual comparisons used transient MCP captures in memory, without
saved screenshots or OCR; they do not constitute Unicode readback. The owned
Notepad test buffer was cleared and closed without saving, its original zoom
was restored, and both test services exited zero with no remaining processes or
listeners. No script was placed on the target.

The built-in Linux map and complete Windows custom map differ only for literal
`´` and `¨`; all 154 Linux mappings were independently checked with libxkbcommon
and its compose table. This is a data-only patch check, separate from the
historical first-release media and recovery matrix. The
[sanitized record](validation/swedish-keyboard-2026-11-18.json) identifies the
tested candidate binaries and exact layout files.
