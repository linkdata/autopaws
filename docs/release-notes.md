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
one. Use a single or mirrored target display; see the
[hardware validation](https://github.com/linkdata/autopaws/blob/v0.6.0/docs/hardware-validation.md)
for the extended-4K capture and input limits.

Existing configurations with `autocrop: true` remain opted in. Set it to `false`
to preserve full frames.

See [usage and configuration](https://github.com/linkdata/autopaws/blob/v0.6.0/README.md).
