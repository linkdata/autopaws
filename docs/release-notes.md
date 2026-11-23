# v0.3.0

- Windows 11 KVM hosts with native COM serial I/O, FFmpeg DirectShow video/audio,
  and the existing API, MCP and browser controls.
- Shared FFmpeg media handling for Windows and macOS, including graceful process
  shutdown, bounded queues and automatic media reconnect.
- Windows amd64/arm64 KVM executables and ZIP bundles containing both commands,
  published alongside the existing Linux and macOS assets and checksums.
- Native Windows CI with hardware-free media fixtures. Local validation uses the
  development host's tests plus all supported-platform cross-builds and release
  checks. OCR corpus tests skip when Tesseract is unavailable.

Windows and macOS require FFmpeg on the daemon's PATH and explicit audio
selection. Windows uses the COM port and DirectShow selectors returned by
`autopaws-kvm --list-devices`; see
[Windows setup](https://github.com/linkdata/autopaws/blob/v0.3.0/docs/windows-setup.md).

Recorded physical validation covers Linux amd64, Apple silicon macOS and
Windows 11 amd64 hosts against Linux targets. Windows checks include input,
MCP coordinates, browser video/audio, physical reconnect and shutdown cleanup.
Windows ARM64 is cross-built without physical hardware coverage. See
[validation and limits](https://github.com/linkdata/autopaws/blob/v0.3.0/docs/hardware-validation.md).
