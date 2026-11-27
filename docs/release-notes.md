# Release notes

## v0.7.3

- Reject video/audio selectors with a missing backend prefix or empty name before
  FFmpeg discovery on Windows/macOS.
- Simplify the README and host quick starts around default settings. Split
  configuration, target setup, browser, MCP, remote access and development guides.
- Use automatic serial selection in the example YAML and trim historical notes.

## v0.7.2

- Add opt-in `-mouse-absolute-refresh` for targets that ignore repeated absolute
  destinations after relative motion. Default remains off. With refresh enabled,
  button releases retain exact coordinates.
- Add API `get_adapter_usb_mode` / `set_adapter_usb_mode` for supported adapters.
  The persistent setter preserves other settings and verifies reset/readback,
  with best-effort rollback. Save the exact raw mode before changing it.
- Setter attempts invalidate mouse mapping. Reapply geometry, recapture and check
  pointer/click/hold behavior. Startup and MCP `setup` never change adapter settings.
- Update the MCP `setup` checklist with per-OS mouse instructions.
- Document adapter-dependent macOS holds and optional USB mode 2. See
  [mouse compatibility](https://github.com/linkdata/autopaws/blob/v0.7.2/README.md#target-mouse-operation) and its
  [validation record](https://github.com/linkdata/autopaws/blob/v0.7.2/docs/validation/macos-mouse-compatibility-2026-11-27.json).

## v0.7.1

- Retry Linux capture after USB reconnect. Native capture on all hosts reopens
  after five seconds without a successfully processed frame. Cleanup precedes
  the one-second retry backoff; recovery advances capture generation and rematches
  automatic audio.
- Reapply extended geometry after recovery. Prefer stable capture-device paths
  when reconnecting USB can renumber nodes.
- Document primary/mirrored target display setup and matching output/capture
  resolution. [Validation record](https://github.com/linkdata/autopaws/blob/v0.7.1/docs/validation/macos-target-linux-recovery-2026-11-27.json).

## v0.7.0

- Open the viewer in **View only** mode; turn it off to send input.
- Add shared display geometry through **Pointer setup** and API/MCP mapping tools.
  Geometry lasts for the daemon run and must be reapplied after capture changes.
  `-mouse-mode extended` blocks positioned input until geometry is configured;
  default legacy identity mapping remains available.
- Preserve held buttons across absolute/relative report types.
- Add the read-only MCP `setup` checklist.
- Keep capture usable when HID is unavailable. Reconnect checks against a Linux
  target passed without daemon restart. [Validation](https://github.com/linkdata/autopaws/blob/v0.7.0/docs/hardware-validation.md).

## v0.6.0

- Disable cropping by default. Explicit `-autocrop` validates centered scaling bars
  using the configured target aspect. Existing `autocrop: true` remains opted in;
  set it to `false` to preserve full frames.
- Handle FFmpeg 9.1 AVFoundation UID/serial metadata during capture discovery.
- Remove the inline `-token` flag. Use `-token-file` or YAML `token`. Viewer
  credentials use tab-session storage and WebSocket subprotocols; reopen the
  token URL when authentication is needed.
- Bound network admission and request deadlines, synchronize layout changes,
  and prevent Linux video opens from acquiring a controlling terminal.
- [Validation and limits](https://github.com/linkdata/autopaws/blob/v0.6.0/docs/hardware-validation.md).
