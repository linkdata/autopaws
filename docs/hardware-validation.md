# Hardware validation

This record distinguishes physical observations from builds and unit tests. It
will be extended with the required browser video/audio and recovery checks before
the first release. The input and API/MCP capture observations below do not
establish the entire release gate by themselves.

## Input development checks, 2026-11-17

- Linux amd64 development host; CH340 `1a86:7523` on `/dev/ttyUSB0`, 9600 baud.
- Dedicated Linux target running GNOME and Firefox. Exact versions remain to be
  recorded during release validation.
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
