# macOS KVM host setup

The daemon and MCP client build for Intel and Apple silicon Macs with
`CGO_ENABLED=0`. Serial I/O uses macOS termios; video and audio use the external
[FFmpeg AVFoundation input](https://ffmpeg.org/ffmpeg-devices.html#avfoundation).
Install FFmpeg separately and ensure it is on the daemon's `PATH`:

```sh
brew install ffmpeg
CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
./dist/autopaws-kvm --version
./dist/autopaws-kvm --list-devices
```

Discovery uses the system `ioreg` command for CH340/CH341 USB serial devices and
FFmpeg for cameras/capture cards and microphones/audio inputs. Host display
capture entries are excluded. Listing does not start capture streams. It can
still be subject to macOS privacy restrictions. Missing FFmpeg or failed discovery
returns an error. Daemon startup requires FFmpeg on `PATH`, including API-only
or serial-only use, and reports `brew install ffmpeg` before opening devices or
listeners when it is missing. `--help` and `--version` work without FFmpeg;
`--list-devices` needs it for AVFoundation discovery.

Connect the capture card's USB and the CH340/CH341 side of the HID board to the
Mac. Connect target HDMI to the card, and target USB to the CH9329 HID side.
Use the `/dev/cu.*` serial path shown in the inventory, or leave it empty when
exactly one supported adapter is attached. Discovery filters USB VID/PID
`1a86:7523` and `1a86:7522`, rather than guessing from serial filenames.

Copy the exact `avfoundation:NAME` selectors printed by `--list-devices`, quoting
names containing spaces. For example, substitute your inventory's values here:

```sh
./dist/autopaws-kvm \
  -serial-device /dev/cu.REPLACE_WITH_YOUR_ADAPTER \
  -capture-device 'avfoundation:REPLACE_WITH_YOUR_VIDEO_NAME' \
  -audio-device 'avfoundation:REPLACE_WITH_YOUR_AUDIO_NAME' \
  -capture-width 1920 -capture-height 1080 \
  -screen-width 1920 -screen-height 1080 \
  -target-layout us104 -web 127.0.0.1:9330
```

Open <http://127.0.0.1:9330>. The same API, MCP tools, browser controls, layouts,
autocrop and shared-token settings work on both hosts. The target's keyboard
layout determines `-target-layout`, independently of the host OS.

macOS may request Camera access for video and Microphone access for audio.
Grant access to the application launching the daemon (for example Terminal),
then restart the daemon if needed. Check System Settings → Privacy & Security
if access was denied. External HDMI capture does not use host screen recording.
A launch service or GUI client can have a different `PATH` and privacy identity
from your terminal; verify FFmpeg availability in that environment.

Video is selected by exact discovered name. Duplicate names are rejected rather
than opening an arbitrary card. Names containing a colon, numeric/reserved
prefixes, or ambiguous name prefixes are also rejected because FFmpeg treats
those as selectors rather than literal device names.
For duplicate names, disconnect the duplicate or rename it through
its vendor's supported tooling. AVFoundation indices can change with device
attachment, so repeat discovery after reconnecting hardware. The backend
requests 30 fps at the configured size and encodes MJPEG for the shared pipeline;
unsupported modes report unavailable. Discovery does not enumerate camera pixel
formats, so the inventory's format list is empty. The default video selection
uses the same non-webcam name preference as Linux and falls back to the first
camera; select the capture card explicitly when several inputs are present.

Audio requires an explicit `-audio-device 'avfoundation:NAME'`. Empty audio
selection reports unavailable on macOS because this backend cannot reliably
pair video and audio by USB ancestry. Use `-audio-device off` to disable it.
The output is 48 kHz stereo S16_LE, converted by FFmpeg, with a maximum 100 ms
per-subscriber application queue and expiry. Video and audio use separate
processes and have no hardware synchronization guarantee.

Native macOS media retries failed opens or reads after one second while demand
remains. A five-second FFmpeg read-inactivity deadline retires a stalled process;
the old owner is joined before reopening. Successful automatic video recovery
advances the generation and clears old frame/crop state. Healthy capture still
stops after ten seconds without viewers or pending snapshots. Explicitly
reselecting the video device also repairs/reopens media.

Video and audio timestamps are **estimated FFmpeg pipe receipt times** on the
server monotonic clock. The browser protocol marks them estimated. A snapshot
waits for a frame whose pipe read began after the request, but FFmpeg/OS buffering
can contain older content. This does not provide Linux V4L2's device capture
freshness guarantee or establish that preceding input has reached the target.
The worker continuously drains output into a latest-video slot; the daemon
kills and joins its FFmpeg processes on stream shutdown.

Physical testing on an Apple silicon Mac and Ubuntu target verified requested
Swedish characters, API/MCP pointer coordinates, video, HDMI speaker content,
serial unplug/replug recovery and clean shutdown. In the baseline capture reconnect
trial, reconnection alone left FFmpeg stalled; same-device selection restored
video and PCM transport. The updated code passed an FFmpeg process stall test
and physical USB reconnect: video/audio resumed on the existing WebSocket
without a daemon restart or host capture reselection. The healthy video stream
closed about ten seconds after the last viewer left.

Ubuntu's native left/right speaker tests produced captured HDMI signal only on
the requested channel before and after reconnect. Ubuntu fell back to internal
speakers during unplugging, so its HDMI output was selected again for the later
speaker tests. Acoustic/browser audio playback and latency remain unmeasured.
The [2026-11-20 record](validation/macos-host-2026-11-20.json) retains the baseline
failure/manual repair, initial timeout, setup retries and an inconclusive trial
whose observer expired before replugging. Deliberate unplug gaps are not
recovery-latency measurements.

Automated checks cover the shared daemon/API/viewer, macOS pseudo-terminal
serial I/O, fixture discovery, media parsing, process cancellation and bounded
queues. Synthetic FFmpeg checks exercise media pipes without opening devices.
The existing Linux host observations remain in
[hardware validation](hardware-validation.md).
