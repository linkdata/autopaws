[![build](https://github.com/linkdata/autopaws/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/linkdata/autopaws/actions/workflows/ci.yml)
[![coverage](https://github.com/linkdata/autopaws/blob/gitcoverage/main/badge.svg)](https://html-preview.github.io/?url=https://github.com/linkdata/autopaws/blob/gitcoverage/main/report.html)

# autopaws

autopaws controls a computer through a CH9329 USB HID adapter and a USB HDMI
capture card. It provides a browser KVM viewer with keyboard, mouse, video and
audio, plus a JSON Lines API and an MCP server for automation. The target needs
no installed software.

- `autopaws-kvm` runs on the host connected to the hardware and serves the API and viewer.
- `autopaws-mcp` connects MCP clients to the daemon over TCP, with optional Tesseract OCR and visual waits through a configured vision service.

Hosts: Linux, macOS and Windows 11 on amd64 or arm64. Physical coverage and
known limits are listed in [hardware validation](docs/hardware-validation.md).

## Hardware

You need a CH9329 adapter with a CH340/CH341 USB-UART interface and a UVC HDMI
capture card. USB audio is needed for sound. The target needs HDMI output and
USB HID keyboard/mouse support.

1. Connect the target's HDMI output to the capture card, and the card's USB connection to the host.
2. Connect the adapter's serial USB side to the host and its HID USB side to the target.
3. Use a single or mirrored target display to start. For an extended desktop, follow [target setup](docs/target-setup.md).

Follow the board's pinout if its UART components are separate. Match target
output and capture resolution where possible, and set the target keyboard
layout and coordinate dimensions separately from capture dimensions.

## Install

Download and extract the archive for your host from the
[latest release](https://github.com/linkdata/autopaws/releases/latest).
ZIP files contain both commands; checksums are in `SHA256SUMS`.

| Host | Archive suffix |
|---|---|
| Linux | `linux_amd64.zip` or `linux_arm64.zip` |
| macOS (Apple silicon) | `darwin_arm64.zip` |
| Windows | `windows_amd64.zip` or `windows_arm64.zip` |

Intel Macs have standalone `autopaws-kvm_TAG_darwin_amd64` and
`autopaws-mcp_TAG_darwin_amd64` downloads. Rename them to `autopaws-kvm` and
`autopaws-mcp`.

On Linux/macOS, install from the extracted directory:

```sh
sudo install -d /usr/local/bin
sudo install -m 0755 autopaws-kvm autopaws-mcp /usr/local/bin/
autopaws-kvm --version
```

On Windows, keep the executables in a permanent folder and add it to `PATH`.
macOS and Windows also require FFmpeg on the daemon's `PATH`. See host setup:
[Linux](docs/linux-setup.md), [macOS](docs/macos-setup.md),
[Windows](docs/windows-setup.md).

macOS binaries are not notarized. If quarantine blocks a verified download,
run `xattr -d com.apple.quarantine /absolute/path/to/binary` for each binary.

## Quick start

On Debian/Ubuntu, grant device access, then sign out and back in:

```sh
sudo usermod --append --groups dialout,video,audio "$USER"
```

Run `autopaws-kvm` to start with defaults and API access. To enable the browser
viewer, add `-web`:

```sh
autopaws-kvm -web
```

Open [the viewer](http://127.0.0.1:9330), turn off **View only**, then click the
image to focus input. Check pointer alignment before clicking target controls.
Use one operator at a time. See [mouse compatibility](docs/target-setup.md#mouse-compatibility)
for macOS targets.

Serial and video devices are selected automatically. Override paths, dimensions or
keyboard layout only when needed; see [configuration](docs/configuration.md).
Linux selects audio automatically; configure [macOS audio](docs/macos-setup.md#audio-and-recovery)
or [Windows audio](docs/windows-setup.md#audio-recovery-and-shutdown) when needed.
The API listens on `127.0.0.1:9329` and the viewer on `127.0.0.1:9330`.
For remote access, use [authentication and SSH forwarding](docs/security.md).
If Linux audio is busy, follow the [WirePlumber setup](alsa/README.md#exclusive-access-with-wireplumber-05).

## Build and develop

With Go 1.27.4 or newer:

```sh
git clone https://github.com/linkdata/autopaws.git
cd autopaws
CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

In PowerShell, set `$env:CGO_ENABLED = '0'` before `go build`.
Both commands build on all supported hosts. Source builds report version `devel`.
Install the binaries from `dist/` using the steps above.

Before submitting changes, run the [local CI gate](docs/development.md#local-ci-gate).
It includes native race tests, viewer JavaScript tests, linters, builds for all
six supported platforms and release checks.

## Documentation

- [Configuration](docs/configuration.md): flags, defaults and shared YAML settings.
- [Target setup](docs/target-setup.md): display geometry, pointer alignment and adapter compatibility.
- [Browser and capture](docs/browser.md): viewer controls, cropping and audio.
- [MCP server](docs/mcp.md): client setup, tools, OCR and visual waits.
- [JSON Lines API](api/PROTOCOL.md) and [browser protocol](web/PROTOCOL.md).
- [Keyboard layouts](CREATING_LAYOUTS.md): built-in maps and custom layouts.
- [Development](docs/development.md), [performance measurements](docs/cycle-time.md) and [release notes](docs/release-notes.md).

## License

[MIT](LICENSE).
