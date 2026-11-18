# v0.1.0 validation and known limits

Required Linux core hardware and browser checks completed on the recorded setup,
including physical serial/capture USB recovery and the fixed ALSA recovery path.
The [validation record](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/hardware-validation.md#release-validation-draft-2026-11-17)
retains original failures, exact evidence and measurement limits. Release assets
remain private and require repository access.

The additional owner-required [limited read-only Windows laptop smoke](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/validation/windows-readonly-2026-11-18.json)
also passed. It covered capture and safe keyboard/pointer actions after
owner-handled setup. Only actions and pass/fail are recorded; screen contents and
images are not retained. The agent placed no scripts on the target and changed no target files
or settings. Text layouts, dragging, audio and broader Windows coverage were
not tested.

## Command line

Run `autopaws-kvm` to start the daemon. It enables the API at `127.0.0.1:9329`
and relative mouse button reports by default; `-web 127.0.0.1:9330` also enables
the browser viewer. Use `--version` and `--list-devices` for those operations.
There are no subcommands: the earlier `serve`, `version` and `list-devices`
spellings are no longer accepted. Explicit flags or YAML can change the defaults.

[Physical startup checks](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/validation/cli-defaults-2026-11-18.json) verified the
bare daemon/default MCP connection and click/drag with the target dimensions
configured while the API/mouse-mode flags remained omitted. Both runs exited 0.

## Recorded configuration and results

- Linux amd64 host: Dell XPS 17 9700, Ubuntu 26.04.1 LTS,
  kernel 7.0.0-43-generic. Linux target: ThinkPad T14 Gen 1, the same OS/kernel,
  GNOME, mirrored/single display and a 1280×720 logical test viewport.
- CH340/CH9329 USB `1a86:7523`, 9600 baud, relative mouse-button mode.
  Physical typing, API/MCP screenshot-scaled clicks and drags work. A second
  process was rejected by flock in 161 µs. Open/configure plus release-all and
  baud pacing took 31.72 ms; joined close took 0.519 ms in the recorded run.
  These observations impose no universal kernel-call deadline. Physical host-USB
  removal while left was held then passed: a failed `(900,550)` move was not
  replayed, and release-all produced target mouseup before the first fresh
  `(500,350)` move after replug.
- UGREEN U15389/MACROSILICON USB `2b89:5389`, MJPG at 1920×1080, monotonic
  capture timestamps, shared snapshots and a 20-fps browser limit. Device/size
  changes, failed-path repair and physical USB replug were exercised. The initial
  unplug exposed rapid ALSA recovery without sample progress. A progress guard
  fixed that loop; physical retest reached bounded unavailable status, then
  resumed fresh video generation 2 and 48 kHz stereo audio generation 3 after
  explicit repair, with zero configuration-order errors.
  Corrupt-frame and missing-Huffman-table handling have synthetic coverage.
- Raw ALSA S16_LE, 48 kHz stereo, 20-ms periods and a 160-ms kernel buffer.
  The device was available exclusively without a WirePlumber policy change.
  A target-generated five-second 440-Hz HDMI tone reached both channels.
- Chromium 157 video/audio, mute/unmute, reconnect and localhost SSH forwarding
  were exercised. The recorded ordinary audio run observed 644.887 seconds of
  mapped output and 641.419 seconds of real PCM. The maximum latency estimate
  was 102.941 ms, or 115.587 ms including the fixture's engineering allowance;
  no mapped quantum exceeded 200 ms and none was unmapped. A real ALSA overrun
  recovered in the same audio graph; a transport pause discarded its backlog
  and reconnected. A separate native injected video-write delay of 201 ms
  replaced the old audio queue and resumed in the same graph, with a 91.637 ms
  maximum estimate. See [measurement method and limits](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/audio-measurement.md):
  this is timestamp-based estimation of its identified pre-guard build, not an
  acoustic guarantee or a new-binary measurement. The later terminal-recovery
  guard has separate physical evidence. A separate
  deliberate DSP pause delayed audio already handed to browser output and is
  retained as diagnostic evidence.
- Native headless Firefox 154.0 passed synthetic DHT/crop/input/corner checks
  and nonzero 48 kHz stereo AudioWorklet playback. A separate strict-view-only
  physical run showed 20-fps video, 48 kHz stereo PCM/worklet progress,
  mute/unmute and reconnect, with zero application input sends. Document-close
  timeout diagnostics also reproduced on a minimal non-product page; the
  comparison and completed teardown are retained in the validation record.
  These checks are not a second ten-minute latency measurement.
- Token-protected API/MCP and browser access worked through authenticated SSH
  forwards for API port 9329 and web port 9330. Missing/wrong API tokens were
  rejected. Browser bootstrap removed the fragment before connecting; video,
  audio and mute/unmute worked. Actual stdio MCP capture/input/OCR exited zero
  after stdin EOF.

After physical capture loss, shutdown can retain the disconnected V4L2 close
`ENODEV` error and exit 1 even after successful repair. The observed owners and
listeners still joined and closed. Kernel calls have no universal cancellation
bound.

Exact binary/source identities, counters, initial failures and fixed-build
results are in the [hardware validation record](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/hardware-validation.md) and
[physical capture record](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/validation/capture-usb-2026-11-18.json). Final local CI,
packaging and reviews follow the [release procedure](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/releasing.md).

## Build targets and coverage limits

Release packaging builds eight binaries with `CGO_ENABLED=0`:
`autopaws-kvm` for Linux amd64/arm64, and `autopaws-mcp` for Linux, macOS and
Windows on amd64/arm64. Linux executables are static; macOS executables link
`libSystem`. SHA-256 checksums accompany the binaries. Cross-builds establish
build compatibility only.

Broader Windows/macOS target tests, ARM64 physical hardware, Safari and browser
checks on other operating systems, a second capture chipset, two physical
identical cards, 115200-baud hardware, BIOS/UEFI and extended cross-platform OCR
have not been run. These remain optional coverage beyond the limited Windows
smoke above; findings that affect shared implementation or required behavior
remain blocking.

Control supports one operator at a time. Built-in target layouts follow Linux;
OS-specific differences require custom files. Browser audio needs a compatible
secure context such as a localhost SSH tunnel. There is no resampler, built-in
HTTPS or supported reverse-proxy setup in v1. The repository and release assets
remain private and require authenticated access.
