# v0.7.2

Optional `-mouse-absolute-refresh` restores absolute cursor destinations that
macOS can ignore after relative motion. It also positions the cursor before
new absolute button presses. The setting defaults off, preserving existing
report sequences for Windows/Linux targets, and `get_device_info` reports its
active value. With refresh enabled, button releases and cleanup keep exact
release coordinates.

The MCP `setup` checklist now gives concise Windows, Linux and macOS mouse
instructions. The README documents adapter-dependent macOS button holds and an
optional, physically verified CH9329 USB-mode workaround. It does not require a
specific cable manufacturer or change adapter configuration automatically.

The JSON Lines API adds `get_adapter_usb_mode` and `set_adapter_usb_mode` for
supported adapters. The explicit setter preserves other configuration bytes,
releases held input, and verifies reset/re-enumeration and readback, with
best-effort rollback on failure. Save the exact raw mode before changing it to
retain hardware-pin selection when restoring. Setter attempts invalidate mouse
mapping; recapture and recheck afterward. MCP `setup` does not change adapter
settings.

Physical checks used two adapters against a mirrored macOS target. The
[compatibility record](validation/macos-mouse-compatibility-2026-11-27.json)
separates repeat-position fixes, native clicks and held text selection from
unverified combinations. The complete local CI gate checks default and enabled
behavior, cancellation, report failures, configuration and supported-platform
cross-builds. Refresh-enabled Windows/Linux behavior was not physically tested.

# v0.7.1

Linux capture now retries automatically after USB reconnect. Native capture
also reopens after five seconds without a successfully processed frame,
including a stream of corrupt MJPEG frames. Source cleanup finishes before the
one-second retry backoff; successful recovery advances the capture generation
and rematches automatic audio. Reapply extended display geometry afterward,
and use a stable capture-device path when USB reconnection can renumber nodes.

The README now collects manual target display setup in one section: place the
captured display top-left and make it primary, or use mirroring, and select the
highest pixel-for-pixel matching output/capture resolution. macOS target
measurements confirmed primary-display-only absolute input on the tested
CH9329 setup. Mouse-button and repeat-after-relative defects remain tracked
separately in [#106](https://github.com/linkdata/autopaws/issues/106) and
[#107](https://github.com/linkdata/autopaws/issues/107).

Validation includes the complete local CI gate, native Linux, macOS and Windows
CI, and physical Linux USB recovery with sustained video and audio on the same
WebSocket and daemon. See the
[numerical record](https://github.com/linkdata/autopaws/blob/v0.7.1/docs/validation/macos-target-linux-recovery-2026-11-27.json)
for exact builds and limits.

# v0.7.0

The viewer opens in View only mode. Turn it off to send keyboard/mouse input.

Optional shared display geometry maps captured-image coordinates into the
target's actual absolute HID input span. Configure signed offset, native display
size and span through web **Pointer setup → Apply** or API/MCP
`get_mouse_mapping`, `set_mouse_mapping` and `reset_mouse_mapping`. Geometry
activates directly after bounds validation; image coordinates remain local.
It lives in the running daemon and must be reapplied after capture changes.
Optional `-mouse-mode extended` blocks positioned input until configured;
default legacy identity behavior remains available for single/mirrored displays
and the tested Windows captured-primary layout. Geometry cannot redirect input
to a display outside the target's absolute HID range.

Mouse reports preserve each held button on its absolute or relative report,
avoiding duplicate presses when dragging across report types.

MCP adds a read-only `setup` checklist for pointer alignment, condition-based
waiting, keyboard layout and maximizing the working window.

Capture remains usable when the HID adapter is unavailable. Physical Linux
unplug/replug checks passed with fresh keyboard output and drag selection in
the same normal terminal, without restarting the terminal or daemon. See the
[hardware validation](https://github.com/linkdata/autopaws/blob/v0.7.0/docs/hardware-validation.md)
for tested configurations and limits.

# v0.6.0

Cropping is now **disabled by default**, preserving the full capture frame
without requiring the target's aspect ratio. Explicit `-autocrop` remains
available for centered, aspect-preserving capture: configure `screen-width` and
`screen-height` to match the target's output aspect. Opt-in cropping validates
the expected bars instead of shrinking to bright screen content, so dark edges
and letterboxed applications no longer redefine the target area. Bars too
narrow to verify remain visible.

macOS capture discovery now handles the UID and serial metadata appended by
FFmpeg 9.1, fixing devices that appeared in the inventory but could not open.

The inline `-token` command-line flag has been removed. Use `-token-file` or the
YAML `token` setting. Viewer credentials use tab-session storage and WebSocket
subprotocols rather than cookies. Reopen the viewer using its token URL if an
existing session needs authentication.

Network admission and request deadlines are bounded. Concurrent keyboard-layout
changes are synchronized, and Linux video opens no longer acquire a controlling
terminal.

Validation includes the complete local CI gate, all six supported-platform
cross-builds and native Linux, macOS and Windows CI. Read-only capture checks
against an attached Windows target confirmed that the new default and explicit
`-autocrop` both preserve full frames at matching aspect. Tests changed no target
files or settings, and no screenshots or OCR were retained. Synthetic
capture-to-MCP-to-HID tests cover dark-content and scaling-bar click mapping.

In the recorded extended-desktop test, pointer commands were acknowledged, but
the owner reported the cursor on the primary display rather than the captured
one. That check did not establish absolute input support for a captured secondary
display; see the
[hardware validation](https://github.com/linkdata/autopaws/blob/v0.6.0/docs/hardware-validation.md)
for the extended-4K capture and input limits.

Existing configurations with `autocrop: true` remain opted in. Set it to `false`
to preserve full frames.

See [usage and configuration](https://github.com/linkdata/autopaws/blob/v0.6.0/README.md).
