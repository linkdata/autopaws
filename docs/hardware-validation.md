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

## Windows target checks

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
