[![build](https://github.com/linkdata/autopaws/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/linkdata/autopaws/actions/workflows/ci.yml)
[![coverage](https://github.com/linkdata/autopaws/blob/gitcoverage/main/badge.svg)](https://html-preview.github.io/?url=https://github.com/linkdata/autopaws/blob/gitcoverage/main/report.html)

# autopaws

autopaws controls a computer through a CH9329 USB HID emulator and a USB HDMI
capture card. Use a browser for keyboard, mouse, video and audio, or an MCP client
for agent access.

- `autopaws-kvm`: Linux/macOS daemon serving the hardware, JSON Lines API and web viewer.
- `autopaws-mcp`: Linux/macOS/Windows stdio MCP server connecting to the daemon, with optional Tesseract OCR.

## Hardware and wiring

Use an amd64 or arm64 Linux/macOS host, a CH9329 behind a CH340/CH341 USB-UART,
and a UVC HDMI capture card with USB audio. The target can run any OS.

1. Connect the target's HDMI output to the capture card, and the card's USB connection to the host.
2. Connect the adapter's serial USB side to the host and its HID USB side to the target.
3. Set the target to a single or mirrored display. Configure its keyboard layout and coordinate dimensions separately from the capture resolution.

Follow the board's pinout if its UART components are separate.

## Installation

Repository and release access require an authenticated GitHub account with access:

```sh
gh auth login
gh auth setup-git
```

### Published binaries

List releases, then download and verify the matching asset. For a Linux amd64 host:

```bash
gh release list --repo linkdata/autopaws
read -r -p "Release tag: " autopaws_version
autopaws_asset="autopaws-kvm_${autopaws_version}_linux_amd64"
autopaws_download=$(mktemp -d)
gh release download "$autopaws_version" --repo linkdata/autopaws \
  --pattern "$autopaws_asset" --pattern SHA256SUMS --dir "$autopaws_download"
(
  cd "$autopaws_download"
  awk -v file="$autopaws_asset" '$2 == file' SHA256SUMS | sha256sum --check - &&
    sudo install -m 0755 "$autopaws_asset" /usr/local/bin/autopaws-kvm
)
autopaws-kvm --version
```

| Command | OS/architecture pairs |
|---|---|
| `autopaws-kvm` | `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64` |
| `autopaws-mcp` | `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`, `windows_amd64`, `windows_arm64` |

Assets are named `COMMAND_TAG_OS_ARCH`, with `.exe` on Windows. Install the MCP
asset as `autopaws-mcp` or `autopaws-mcp.exe`. Use its absolute path in GUI clients.
On macOS, verify with `shasum -a 256 --check -`; on Windows, compare `SHA256SUMS`
with `Get-FileHash -Algorithm SHA256 PATH`.

macOS requires [FFmpeg and device permissions](docs/macos-setup.md). Binaries are
not notarized; if quarantine blocks a verified download, run
`xattr -d com.apple.quarantine /absolute/path/to/binary`.

### Build from source

With Go 1.27.4 or newer:

```sh
gh repo clone linkdata/autopaws
cd autopaws
CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

On Windows PowerShell, use `$env:CGO_ENABLED = "0"` before `go build`.
Linux/macOS builds produce both commands; Windows builds produce only
`autopaws-mcp`. Source builds report version `devel`. On Linux/macOS, install with:

```sh
sudo install -m 0755 dist/autopaws-kvm dist/autopaws-mcp /usr/local/bin/
```

## Quick start

On Debian/Ubuntu, grant device access, then sign out and back in:

```sh
sudo usermod --append --groups dialout,video,audio "$USER"
```

See [Linux setup](docs/linux-setup.md) for permissions, stable device paths and
systemd, or [macOS setup](docs/macos-setup.md) for FFmpeg and device selectors.
List devices, then start the daemon with the target's actual settings:

```sh
autopaws-kvm --list-devices
autopaws-kvm \
  -serial-device /dev/serial/by-id/REPLACE_WITH_YOUR_CH340_OR_CH341 \
  -screen-width 1920 -screen-height 1080 -target-layout us104 \
  -web 127.0.0.1:9330
```

Open `http://localhost:9330` and click the image to focus input. Use one operator
at a time. For remote access and authentication, see [Security](#security).
If capture audio is busy, follow the [WirePlumber setup](alsa/README.md#exclusive-access-with-wireplumber-05).

## Commands and configuration

```sh
autopaws-kvm --help
autopaws-kvm --version
autopaws-kvm --list-devices
```

With no arguments, the daemon serves the API at `127.0.0.1:9329` with relative
mouse button reports. Enable the viewer with `-web 127.0.0.1:9330`; for a viewer
without the API, add `-api ''`. At least one listener must be enabled.
Use `--version` and `--list-devices` separately from daemon flags.

`--list-devices` marks automatic defaults with `*`. It probes video formats on
Linux without starting a stream or opening serial/audio devices. Multiple serial
adapters require `-serial-device`. Prefer stable `/dev/serial/by-id` or
`/dev/serial/by-path` paths over `ttyUSBn`.

### Configuration file

Precedence is defaults, an explicitly selected YAML file, then explicit flags.
There are no environment overrides, automatic file searches or live reloads:

```sh
autopaws-kvm -config ./autopaws.yaml -web-fps 30
```

See [the example configuration](examples/autopaws.yaml). Flag names and YAML keys
match. Explicit false, empty and zero values override the file. Use `=false` for
boolean flags, such as `-autocrop=false`. YAML must be one scalar-valued mapping;
unknown/duplicate keys, nulls, aliases and extra documents are rejected.

| Setting | Default | Meaning |
|---|---|---|
| `api` | `127.0.0.1:9329` | JSON Lines API `host:port`; empty disables it |
| `web` | empty | Viewer `host:port`; empty disables it |
| `token-file` | empty | Shared-secret file for API and web; empty disables authentication |
| `serial-device` | empty | Serial path; empty auto-detects CH340/CH341 |
| `serial-baud` | `9600` | Supported baud rate below |
| `screen-width` | `1920` | Positive target coordinate width |
| `screen-height` | `1080` | Positive target coordinate height |
| `target-layout` | `us104` | Target text layout |
| `layouts-dir` | empty | Custom layouts, searched before built-ins |
| `mouse-button-mode` | `relative` | `relative` or `absolute` button reports |
| `capture-device` | empty | Linux video path or macOS `avfoundation:NAME`; empty auto-selects |
| `capture-width` | `1920` | Requested width, 1–4294967295 |
| `capture-height` | `1080` | Requested height, 1–4294967295 |
| `autocrop` | `true` | Detect and crop black borders |
| `web-fps` | `20` | Viewer frame rate, 1–120 |
| `audio-device` | empty | Linux PCM path or macOS `avfoundation:NAME`; `off` disables audio |
| `log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `debug-keys` | `false` | Log input/packet records regardless of log level |

Supported baud rates: 1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600 and 115200.
Screen dimensions describe target coordinates; capture dimensions describe the
video stream. A one-pixel target axis maps to HID zero. See
[keyboard layouts](CREATING_LAYOUTS.md) for built-in and custom text maps.

Listen addresses accept hostnames, IPv4 and bracketed IPv6. An empty host binds
all interfaces; ports are 0–65535, with zero selecting an ephemeral port.
Logs go to stderr. SIGINT/SIGTERM stop the daemon and attempt input release;
kernel device calls can outlast application cleanup deadlines.

## Capture and browser viewer

Linux capture uses V4L2 MJPG. Automatic video selection prefers non-webcam names;
automatic audio matches the selected video's USB device. An explicit PCM path
stays fixed. macOS uses FFmpeg/AVFoundation and requires explicit audio selection.

Capture starts on demand and stops after ten seconds without video demand.
Snapshots wait up to five seconds for a fresh decodable frame and optionally crop
black borders. Linux timestamps reflect device capture; macOS timing estimates
pipe receipt and cannot account for FFmpeg/OS buffering. A fresh frame does not
prove the target finished processing input. Missing capture hardware leaves input
usable. Device/resolution changes invalidate pending snapshots with `capture changed`.

Click the video for physical keyboard and mouse input, independent of the text
layout. The toolbar offers Ctrl+Alt+Delete, Alt+Tab, view-only mode, cursor,
sizing and fullscreen controls. Focus loss, view-only mode and stream changes
release held input. Connections reconnect automatically without replaying input.
Keyboard Lock depends on browser support; toolbar shortcuts remain available.
The installable viewer caches no assets or media.

### Browser audio

Click Audio to play and mute to stop. Playback requires AudioWorklet and a secure
context; use localhost or an [SSH tunnel](#security). Errors leave video and input
usable. macOS converts audio to 48 kHz stereo; Linux uses the negotiated PCM rate.
Audio choices persist across reconnects. Recovery flushes buffered samples;
configuration changes reopen the selected source. The server/browser application
queues total at most 200 ms, excluding capture, network and output-device latency.
See [ALSA setup](alsa/README.md) and [measured results](docs/audio-measurement.md).

For wire formats and input rules, see the [API reference](api/PROTOCOL.md) and
[browser protocol](web/PROTOCOL.md). Go clients can use `apiclient`.

## MCP server

Configure each MCP client to start its own local stdio process against the daemon:

```sh
autopaws-mcp -api 127.0.0.1:9329
```

| Flag | Default | Meaning |
|---|---|---|
| `-api host:port` | `127.0.0.1:9329` | KVM JSON Lines address, port 1–65535 |
| `-capture-log DIR` | off | Save screenshot JPEGs; no retention limit |
| `-token-file PATH` | off | Read the API secret at startup |
| `-tesseract PATH` | automatic | Optional OCR engine; an invalid explicit path disables OCR |

| Tools | Purpose |
|---|---|
| `type_text`, `send_key`, `send_key_sequence` | Keyboard actions |
| `list_keyboard_layouts`, `set_keyboard_layout` | Inspect or select target text maps |
| `mouse_move`, `mouse_click`, `mouse_drag`, `mouse_scroll` | Pointer actions |
| `capture_screen` | Fresh JPEG and image dimensions |
| `get_screen_text`, `execute_and_read` | English OCR; optionally type a command first |
| `get_device_info`, `list_capture_devices`, `set_capture_device`, `set_capture_resolution` | Device information and capture settings |

Before absolute movement, positioned clicks or drags, call `capture_screen`.
Coordinates are integer pixels in that session's latest screenshot, including
cropping. Relative deltas are unscaled; clicks at the current pointer need no
screenshot. Capture-setting attempts invalidate screenshot coordinates; layout
changes preserve them. Cancelling a tool closes its API call and allows input
release cleanup. Use one operator; sessions do not merge held input.

Select a layout with `set_keyboard_layout({"layout":"se105"})` after setting the
target OS layout. Custom YAML names override built-ins; selecting the same name
reloads its file. Invalid selections keep the working map. Restarting restores
the startup layout. See [layout selection](CREATING_LAYOUTS.md#changing-the-active-layout).

OCR requires Tesseract with English data, discovered at startup through
`-tesseract`, `PATH`, or Windows `ProgramFiles`/`ProgramFiles(x86)` installations.
Restart MCP after installing it. OCR calls time out after 15 seconds and do not
set screenshot coordinates. `execute_and_read` types a raw command plus Enter,
waits `wait_seconds` (default 1, range 0–60), then captures OCR output; the wait
does not establish command completion. Capture logs contain complete screenshots.

### Client configuration

For Claude Code:

```sh
claude mcp add --transport stdio --scope user autopaws -- \
  /absolute/path/to/autopaws-mcp -api 127.0.0.1:9329
```

For Claude Desktop, add this to its MCP configuration:

```json
{
  "mcpServers": {
    "autopaws": {
      "command": "/absolute/path/to/autopaws-mcp",
      "args": ["-api", "127.0.0.1:9329"]
    }
  }
}
```

On Windows, use an absolute path such as `C:\\Tools\\autopaws\\autopaws-mcp.exe` in
JSON. Add `-token-file` and its local path when authentication is enabled.
Restart the client after configuration changes. The API address is a JSON Lines
TCP service; see [local MCP client setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers).

## Security

API and viewer access allow keyboard/mouse control of the target. Bind to loopback
and leave `debug-keys` disabled unless diagnosing input. Browser view-only mode
only affects that viewer.

Generate a private shared-secret file:

```sh
umask 077
openssl rand -hex 32 > ./autopaws.token
```

Pass `-token-file ./autopaws.token` to the daemon and a file containing the same
secret to MCP. Files must be readable, nonempty UTF-8; surrounding whitespace is
trimmed. Both processes read the secret once at startup.

Open `http://localhost:9330/#token=ENCODED_SECRET`, using `encodeURIComponent(secret)`.
The viewer removes the fragment and stores the token in a session cookie for its
WebSocket. API clients send the `token` field or use `apiclient.NewWithToken`.

Tokens do not encrypt traffic. For a remote daemon bound to loopback, forward
both ports:

```sh
ssh -L 9329:127.0.0.1:9329 -L 9330:127.0.0.1:9330 kvmhost
```

Connect MCP to local port 9329 and open `http://localhost:9330` for the viewer.
Localhost provides a secure context for AudioWorklet. Plain HTTP to a LAN address
does not; built-in HTTPS and reverse-proxy deployments are unsupported.

## Tested configurations

The [hardware validation record](docs/hardware-validation.md) lists tested setups,
results and limits. Linux input, capture/audio recovery and Chromium/Firefox
playback passed on the recorded hardware. A limited Windows target smoke covered
capture and safe keyboard/pointer actions. An Apple silicon macOS host passed
input, capture and reconnect checks against Ubuntu; acoustic browser playback
and latency remain unvalidated. Cross-builds cover supported OS/architecture
pairs and do not establish hardware coverage.

## Development

Use Go 1.27.4+, Node.js 26.13.1, the golangci-lint version pinned in
[CI](.github/workflows/ci.yml), actionlint, ShellCheck and a C compiler for race tests.
Run the complete local gate after final edits and before pushing or starting CI:

```sh
actionlint
bash -n .github/scripts/build-release.sh
shellcheck .github/scripts/build-release.sh
node --check web/assets/viewer-helpers.mjs
node --check web/assets/viewer.mjs
node --check web/assets/sw.js
node --check web/assets/audio-playback.mjs
node --check web/assets/audio-worklet.mjs
node --test web/*.test.mjs
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go build -o /dev/null ./cmd/autopaws-kvm
golangci-lint run
CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go tool cover -html=coverage.out -o=coveragereport.html.out
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-mcp
```

On macOS, install FFmpeg and also run the native media fixtures:

```sh
AUTOPAWS_FFMPEG_TEST=1 CGO_ENABLED=1 go test -race ./avfoundation
```

## License

MIT. See [LICENSE](LICENSE).
