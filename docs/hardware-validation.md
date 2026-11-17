# Hardware validation

This record distinguishes physical observations from builds and unit tests. It
will be extended with the required video, audio, recovery and MCP checks before
the first release. The input observations below do not establish that release
gate by themselves.

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
physical input. Screenshot-to-click validation remains pending real capture
support; canned-JPEG SDK tests cover the conversion contract meanwhile.

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
detector. Live source switching, device-loss recovery and fresh API/MCP snapshots
remain part of the shared-pipeline and release checks.
