# Hardware validation

These are observations from recorded setups, not general hardware guarantees.
Linked JSON records contain exact builds, actions, pass/fail results and limits.
Cross-builds and generated-media tests do not establish physical coverage.

## Linux host

| Item | Setup |
|---|---|
| Host | Dell XPS 17 9700, Linux amd64, Ubuntu 26.04.1 LTS, kernel 7.0.0-43-generic |
| Target | ThinkPad T14 Gen 1, Ubuntu 26.04.1 LTS, GNOME; mirrored/single display, 1280×720 coordinates |
| Input | CH340/CH9329, USB `1a86:7523`, 9600 baud, relative buttons |
| Capture | UGREEN U15389/MACROSILICON, USB `2b89:5389`, MJPG 1920×1080 |
| Audio | S16_LE, 48 kHz stereo, 20 ms periods, 160 ms kernel buffer |
| Viewer | Chromium 157 and native headless Firefox 154.0; 20 fps viewer limit |

Passed: keyboard/modifiers, clicks, held drags, cancellation, release/shutdown,
MCP corner mapping and coordinate invalidation, serial reconnect, Chromium and
Firefox video/audio, mute/reconnect, token-protected SSH forwarding and HDMI
channel capture. Records: [input/MCP](validation/source-review-2026-11-20.json),
[defaults](validation/cli-defaults-2026-11-18.json),
[serial/Firefox](validation/recovery-firefox-2026-11-18.json),
[capture USB](validation/capture-usb-2026-11-18.json) and
[audio/SSH/MCP](validation/audio-2026-11-17.json).

Automatic capture recovery passed in later
[Linux-host checks](validation/macos-target-linux-recovery-2026-11-27.json).
An existing WebSocket resumed without daemon restart or capture reselection. Older records used explicit same-path
reselection.

### Browser audio timing

The [Chromium audio run](validation/audio-2026-11-17.json) mapped 240,541 real
quanta across 644.887 seconds of output and 641.419 seconds of PCM. Maximum
estimated latency was 102.941 ms, or 115.587 ms with its paired allowance; no
estimate exceeded 200 ms. There were 605 underrun quanta and 76,167 underrun
sample frames, with no unmapped real quanta. The run included an ALSA overrun
and transport stall/reconnect.

This measures capture timestamp to estimated browser device output, not acoustic
latency. See the [measurement method](audio-measurement.md) for clock limits.
Cold HDMI acquisition can produce valid black/noisy frames. Kernel calls have
no universal cancellation deadline; Linux shutdown can report `ENODEV` and exit
1 after cleanup of a disconnected capture device.

## macOS host

macOS 27.2.1 arm64 with FFmpeg 9.0.2 used the CH340/CH9329 and UGREEN card against
a Linux target at 1280×720 coordinates, with 1920×1080 capture. Passed: input,
MCP mapping, serial reconnect, video/audio USB recovery on the existing WebSocket,
HDMI channel capture and clean shutdown without held input. Held-input release at shutdown,
browser/acoustic audio playback and latency were not measured. [macOS record](validation/macos-host-2026-11-20.json).

[Linux-target recovery checks](validation/linux-recovery-2026-11-27.json) on this
macOS host also passed capture recovery without daemon restart; extended
geometry required reapplication.
Capture remained usable while HID was unavailable; input recovered on reconnect.
Relative buttons passed; an optional absolute-button diagnostic did not pass.

An FFmpeg 9.1 replacement-card check (`345f:2109`) passed discovery/open and three
1920×1080 frames after a device-name parsing fix. Audio and browser playback were
not checked. [Replacement-card record](validation/macos-capture-2026-11-26.json).

Extended Linux mapping passed 25 API points within one native pixel, MCP endpoints
and Firefox clicks/drags. Setup: 2560×1440 capture, native 1920×1080 display at
`(1920,0)` in a 3840×1200 input span. [Display-mapping record](validation/linux-display-mapping-2026-11-27.json).

## Windows host

Windows 11 Pro build 26300 amd64 used CH340/CH9329 (`1a86:7523`) and UGREEN 15389
(`2b89:5389`) against Linux. Passed: 1920×1080 capture, 48 kHz stereo WebSocket
audio, idle/reopen, device selection, resolution changes, USB reconnect, keyboard,
held drags, input cancellation/release, MCP coordinates and stdio EOF.
Chromium showed roughly 19–20 fps; both HDMI audio channels were heard.
[Windows-host record](validation/windows-host-2026-11-23.json).

Limits: one USB enumeration failure cleared after moving connections; its cause
is unknown. Ctrl+C released input and closed media, but daemon exit code 0 was
not established. Acoustic latency and physical letterboxing were not measured.
Cleanup reported an error when an explicitly selected COM port was absent.
That build enabled autocrop by default; the current default is off.

## Windows targets

- [Captured-primary mapping](validation/windows-primary-2026-11-26.json): UGREEN
  15389 at 2560×1440, second display extended to the left. Passed 25-point grid,
  API/Firefox clicks and drags, origin clicks in both button modes, held input
  and disconnect release.
- [Extended 4K](validation/windows-extended-4k-2026-11-26.json): USB Video
  (`345f:2109`) with owner-reported 4K output delivered 1920×1080 capture; 4K
  capture requests failed. Cursor stayed on the primary display. Captured
  secondary-display input was not established.
- [Read-only autocrop](validation/autocrop-windows-readonly-2026-11-26.json):
  USB Video (`345f:2109`), default-off and explicit opt-in preserved full
  1920×1080 frames at matching aspect.
- [Limited laptop smoke](validation/windows-readonly-2026-11-18.json): shortcuts,
  focus navigation, pointer movement and positioned clicks passed after owner
  setup. No scripts, file or setting changes; only permitted metadata, actions
  and pass/fail were retained, without screen contents, identifiers, screenshots or OCR.
- [Swedish layout](validation/swedish-keyboard-2026-11-18.json): all 154 printable
  characters, Tab and Enter passed MCP/physical HID in an unsaved test buffer.

## macOS targets

A Linux amd64 host, macOS 27.2.1 target, CH340/CH9329 at 9600 baud and UGREEN 15389
at 1920×1080 passed primary/mirrored 25-point absolute grids within 0.25 Cocoa
logical points. A captured secondary display was unreachable. Release cleanup
passed for a confirmed absolute hold; other macOS-target mouse-release cases
were not established. [Position/recovery record](validation/macos-target-linux-recovery-2026-11-27.json).

The [mouse compatibility follow-up](validation/macos-mouse-compatibility-2026-11-27.json)
tested two firmware variants with combined mouse interfaces. Absolute refresh
restored repeated destinations after relative motion. Relative button presses
did not register, and absolute holds failed during movement in the original USB mode.
On one adapter, USB work mode 2 passed MCP clicks and held text-selection drags
in both button modes; restoring the original raw mode preserved all 50
configuration bytes. The second adapter did not acknowledge reset; its mode-2
behavior remains unverified. Refresh-enabled Windows/Linux behavior was not
physically retested. Follow [target setup](target-setup.md#mouse-compatibility).

## Coverage limits

Broader Windows/macOS target tests, Safari, Intel Mac hardware, 115200-baud
hardware, BIOS/UEFI and ARM64 host hardware were not tested beyond the Apple
silicon Mac. Full input, audio and recovery acceptance used the UGREEN card;
USB Video checks were limited to capture and pointer probes.
