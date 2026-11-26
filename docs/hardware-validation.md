# Hardware validation

These are observations from the recorded setups. Cross-builds and synthetic
tests are separate from physical validation. Exact builds, actions and limits
are in the linked JSON records.

## Linux host

| Item | Setup |
|---|---|
| Host | Dell XPS 17 9700, Linux amd64, Ubuntu 26.04.1 LTS, kernel 7.0.0-43-generic |
| Target | ThinkPad T14 Gen 1, Ubuntu 26.04.1 LTS, GNOME |
| Display | Mirrored/single display, 1280×720 logical coordinates |
| Input | CH340/CH9329, USB `1a86:7523`, 9600 baud, relative button mode |
| Capture | UGREEN U15389/MACROSILICON, USB `2b89:5389`, MJPG 1920×1080 |
| Audio | S16_LE, 48 kHz stereo, 20 ms periods, 160 ms kernel buffer |
| Viewer | Chromium 157 and native headless Firefox 154.0; 20 fps viewer limit |

Physical checks passed for keyboard entry, modifiers, clicks, held-button drags,
cancellation, input release and shutdown. MCP screenshots mapped correctly to
target coordinates, including all four corners. Capture device/resolution
changes required a fresh screenshot before absolute input. See the
[2026-11-20 input/MCP record](validation/source-review-2026-11-20.json) and
[default-startup record](validation/cli-defaults-2026-11-18.json).

| Check | Observation |
|---|---|
| Serial unplug/replug | Unavailable input failed without replay. Release-all preceded fresh input after reconnect. |
| Capture/audio unplug/replug | Sources became unavailable; explicit same-path selection restored fresh video and 48 kHz stereo audio without configuration-order errors. |
| Chromium | Video, audio, mute/unmute, reconnect, crop and viewer controls passed. |
| Firefox | Synthetic crop/input checks and physical video/audio progress, mute/unmute and reconnect passed. |
| SSH access | Both forwarded ports worked with token-protected API and viewer access. |
| HDMI audio content | A target-generated 440 Hz signal reached both capture channels. |

See the [serial/Firefox record](validation/recovery-firefox-2026-11-18.json),
[capture USB record](validation/capture-usb-2026-11-18.json), and
[audio record](validation/audio-2026-11-17.json).

### Browser audio timing

The Chromium run recorded 644.887 seconds of mapped output and 641.419 seconds
of real PCM, including an ALSA overrun and transport stall/reconnect.

| Metric | Result |
|---|---:|
| Mapped / unmapped real quanta | 240,541 / 0 |
| Estimates above 200 ms, including paired allowance | 0 |
| Maximum estimate | 102.941 ms |
| Maximum estimate plus paired allowance | 115.587 ms |
| 95th percentile, upper 1 ms histogram bin | 87 ms |
| Maximum worklet ring | 49.875 ms |
| Underrun quanta / sample frames | 605 / 76,167 |
| Report errors / skipped reports | 0 / 0 |

This measures capture timestamp to estimated browser device output. It is not
an acoustic measurement or a latency guarantee. The application cannot recall
audio already handed to the browser output path. See the
[measurement method](audio-measurement.md) for clock and mapping limits.

### Known limits

- Input checks used one operator and one capture card at 9600 baud.
- Cold HDMI acquisition can produce valid black/noisy frames before the signal
  settles. A valid JPEG does not establish stable target content.
- Firefox 154 forced document close emitted a script-timeout diagnostic that
  also reproduced on a minimal page without product code; backend cleanup
  completed.
- Linux shutdown can retain a disconnected V4L2 descriptor's `ENODEV` close
  diagnostic and exit 1 after all owners have joined. Kernel calls have no
  universal cancellation deadline.

## macOS host

macOS 27.2.1 on arm64 with FFmpeg 9.0.2 used the same CH340/CH9329 and UGREEN
card, a Linux target at 1280×720 logical coordinates, and 1920×1080 capture.
Keyboard/pointer input, MCP capture and coordinate mapping, serial reconnect,
and automatic video/audio reconnect passed. Physical USB recovery resumed the
existing WebSocket without daemon restart or capture reselection. Native
left/right speaker samples verified captured HDMI channel content before and
after reconnect.

The [macOS record](validation/macos-host-2026-11-20.json) separates each tested
build and probe. Browser/acoustic playback and latency were not measured on
macOS. Timestamps estimate FFmpeg pipe receipt; upstream buffering is unmeasured.

A [replacement-card check](validation/macos-capture-2026-11-26.json) with FFmpeg
9.1 and USB Video (`345f:2109`) reproduced a device-name parsing failure and
verified the fix by opening capture and reading three 1920×1080 frames; audio
and browser playback were not checked.

## Windows host

Windows 11 Pro build 26300 on amd64 used a CH340/CH9329 (`1a86:7523`) and UGREEN
15389 capture card (`2b89:5389`) against the connected Linux target. The recorded
development binary acquired decodable 1920×1080 JPEGs and simultaneous WebSocket
video/audio, with 48 kHz stereo PCM in 10 ms blocks. Three idle/reopen cycles,
serial contention, live resolution changes, canonical device selection and
explicit missing-device behavior passed their transport/media metadata checks.

Physical USB disconnect/reconnect checks restored serial writes and video/audio
on an existing WebSocket without daemon restart or video reselection. One capture
cycle first reported Windows' Configuration Descriptor Request Failed. A complete
power cycle did not resolve it; moving USB connections restored enumeration and
media. The cause remains unknown. The serial adapter changed from COM3 to COM4,
and later checks used automatic serial selection after restarting the daemon.

The in-app Chromium viewer displayed live video at approximately 19–20 fps.
Positioned clicks operated target controls, and held-button dragging visibly
selected text. An absolute move followed by a relative move placed the target
cursor at the expected location within display scaling. Read-only target
configuration confirmed the Swedish X11 layout;
with daemon layout `se105`, mixed-case ASCII, digits, punctuation and Swedish
letters matched visually. A browser keyboard shortcut opened a target terminal.
Closing a slow API typing request after 200 ms canceled it and emitted a release
report. Closing a WebSocket with a held modifier also emitted an all-zero release
before fresh input. Subsequent lowercase input matched visually without replaying
the interrupted text.

Native left/right sound samples reached both HDMI capture channels, with observed
PCM peaks of 1045 and 1049. The user confirmed hearing both through the viewer.
The mute control returned to the unmuted state, and the original target audio
route was restored afterward. No acoustic latency measurement was made.

Ctrl+C with a held modifier and mouse button emitted zero keyboard and mouse
reports, closed both WebSocket clients and left no daemon or FFmpeg processes.
COM4 reopened exclusively afterward. The outer PowerShell PTY returned 1 on
Ctrl+C; no daemon cleanup error was emitted, and daemon exit code 0 was not
established. An earlier shutdown with explicitly selected COM3 absent reported a
release-cleanup error.

Restarting the same binary with default autocrop enabled restored the live viewer
automatically and produced a fresh 1920×1080 JPEG. Simultaneous video/audio
continued without invalid headers. The physical full-frame signal was not falsely
cropped; deliberately imposed letterboxing was not tested physically and is
covered by common synthetic crop tests.

Native MCP stdio initialization and tools passed. Absolute input required a fresh
capture, and each resolution change invalidated old screenshot coordinates.
Fresh JPEGs decoded at the requested 1920×1080 or 1280×720 dimensions, and each
screenshot center mapped to target coordinates (640, 360) in the API response.
MCP exited 0 on stdin EOF.

The [Windows record](validation/windows-host-2026-11-23.json) reports passing
results for the recorded checklist with the stated limits. No screenshots, OCR
or audio recordings were retained.

Windows CI uses mocks/generated media and does not establish physical coverage.
Windows ARM64 is cross-built; ARM64 hardware has not been tested. See
[Windows setup](windows-setup.md).

## Windows target checks

A [read-only autocrop check](validation/autocrop-windows-readonly-2026-11-26.json)
verified that the new default and explicit opt-in preserve full 1920×1080
snapshots and web frames from the attached Windows target. No input was sent
or target settings changed; scaling bars and click mapping were tested with
synthetic frames and a recording HID transport. Cropping is now off by default.

The limited read-only laptop smoke passed keyboard shortcuts, focus navigation,
pointer movement and positioned clicks after owner-handled setup. It placed no
scripts on the target and changed no files or settings. Only permitted metadata,
actions and pass/fail are retained in the
[read-only record](validation/windows-readonly-2026-11-18.json); it contains no
screen contents, identifiers, screenshots or OCR.

The separate Swedish layout check passed all 154 printable characters, Tab and
Enter through MCP and physical HID into an unsaved test buffer. Linux mappings
were checked against libxkbcommon and its compose table. See the
[Swedish keyboard record](validation/swedish-keyboard-2026-11-18.json).

## Coverage limits

Broader Windows/macOS target tests, Safari, Intel Mac hardware, a second capture
chipset, 115200-baud hardware, BIOS/UEFI, and an ARM64 Linux host were not tested.
Supported-platform cross-builds do not establish runtime or hardware support
beyond these observations.
