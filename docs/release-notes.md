# First release validation and coverage

RELEASE_VALIDATION_PENDING

The implementation and release packaging are ready for validation. No first
release has been published. Required physical serial/capture unplug-and-replug,
available Firefox checks and the remaining input/recovery evidence in
[the validation record](hardware-validation.md#release-validation-draft-2026-11-17)
keep publication blocked. Remove the marker only after every required Linux
core row passes; a follow-up issue cannot replace a required check.

## Recorded configuration and results

- Linux amd64 host: Dell XPS 17 9700, Ubuntu 26.04.1 LTS,
  kernel 7.0.0-43-generic. Linux target: ThinkPad T14 Gen 1, the same OS/kernel,
  GNOME, mirrored/single display and a 1280×720 logical test viewport.
- CH340/CH9329 USB `1a86:7523`, 9600 baud, relative mouse-button mode.
  Physical typing, API/MCP screenshot-scaled clicks and drags work. A second
  process was rejected by flock in 161 µs. Open/configure plus release-all and
  baud pacing took 31.72 ms; joined close took 0.519 ms in the recorded run.
  These observations impose no universal kernel-call deadline.
- UGREEN U15389/MACROSILICON USB `2b89:5389`, MJPG at 1920×1080, monotonic
  capture timestamps, shared snapshots and a 20-fps browser limit. Device/size
  changes and failed-path repair were exercised; physical replug is pending.
  Corrupt-frame and missing-Huffman-table handling have synthetic coverage.
- Raw ALSA S16_LE, 48 kHz stereo, 20-ms periods and a 160-ms kernel buffer.
  The device was available exclusively without a WirePlumber policy change.
  A target-generated five-second 440-Hz HDMI tone reached both channels.
- Chromium 157 video/audio, mute/unmute, reconnect and localhost SSH forwarding
  were exercised. The final ordinary audio run observed 644.887 seconds of
  mapped output and 641.419 seconds of real PCM. The maximum latency estimate
  was 102.941 ms, or 115.587 ms including the fixture's engineering allowance;
  no mapped quantum exceeded 200 ms and none was unmapped. A real ALSA overrun
  recovered in the same audio graph; a transport pause discarded its backlog
  and reconnected. A separate native injected video-write delay of 201 ms
  replaced the old audio queue and resumed in the same graph, with a 91.637 ms
  maximum estimate. See [measurement method and limits](audio-measurement.md):
  this is timestamp-based estimation, not an acoustic guarantee. A separate
  deliberate DSP pause delayed audio already handed to browser output and is
  retained as diagnostic evidence.
- Token-protected API/MCP and browser access worked through authenticated SSH
  forwards for API port 9329 and web port 9330. Missing/wrong API tokens were
  rejected. Browser bootstrap removed the fragment before connecting; video,
  audio and mute/unmute worked. Actual stdio MCP capture/input/OCR exited zero
  after stdin EOF.

Exact binary/source identities, counters, failed diagnostic runs and pending
checks are recorded in the [hardware validation record](hardware-validation.md)
and its compact evidence file. Complete local CI and packaging checks are
required again after final release edits.

## Build targets and coverage limits

Release packaging builds eight binaries with `CGO_ENABLED=0`:
`autopaws-kvm` for Linux amd64/arm64, and `autopaws-mcp` for Linux, macOS and
Windows on amd64/arm64. Linux executables are static; macOS executables link
`libSystem`. SHA-256 checksums accompany the binaries. Cross-builds establish
build compatibility only.

Separate Windows/macOS target tests, ARM64 physical hardware, Safari and browser
checks on other operating systems, a second capture chipset, two physical
identical cards, 115200-baud hardware, BIOS/UEFI and extended cross-platform OCR
have not been run. These are optional coverage under the current plan; findings
that affect shared implementation or required Linux behavior remain blocking.

Control supports one operator at a time. Built-in target layouts follow Linux;
OS-specific differences require custom files. Browser audio needs a compatible
secure context such as a localhost SSH tunnel. There is no resampler, built-in
HTTPS or supported reverse-proxy setup in v1. The repository and release assets
remain private and require authenticated access.
