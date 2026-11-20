[![build](https://github.com/linkdata/autopaws/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/linkdata/autopaws/actions/workflows/ci.yml)
[![coverage](https://github.com/linkdata/autopaws/blob/gitcoverage/main/badge.svg)](https://html-preview.github.io/?url=https://github.com/linkdata/autopaws/blob/gitcoverage/main/report.html)

# autopaws

autopaws is a software KVM for Linux and macOS hosts. It controls a target machine through a CH9329 USB HID emulator and shows the target's screen in a browser through a USB HDMI capture card. AI agents get the same keyboard, mouse and screen access through an MCP server.

It is a Go rewrite of two Python projects:

- [serial-hid-kvm](https://github.com/linkdata/serial-hid-kvm), the KVM server
- [mcp-serial-hid-kvm](https://github.com/linkdata/mcp-serial-hid-kvm), its MCP server

Keyboard/mouse control, device discovery, CLI configuration, API/MCP access, browser video/audio, optional OCR and shared-token access are implemented. Required Linux core validation is recorded; see [tested configurations and limits](#tested-configurations-and-limits). The [tracking epic](../../issues/23) records the port.

## Components

- **KVM server** (`autopaws-kvm`): a daemon on the Linux or macOS host. It owns the serial port and the capture card, and serves a JSON Lines TCP API and the web viewer.
- **MCP server** (`autopaws-mcp`): a separate stdio binary that AI agents start. It is a thin client of the API, and it runs OCR with Tesseract when that is installed. It builds for Linux, macOS and Windows.

## Goals

- Static Linux binaries (amd64, arm64), built with `CGO_ENABLED=0`.
- Standard library first. Third-party modules only when they are widely used and well maintained.
- The browser is the only viewer. There is no native preview window.
- The same hardware as serial-hid-kvm: a CH9329 behind a CH340/CH341 USB-UART, and UVC HDMI capture cards with their USB audio.
- Targets can run any OS. The CH9329 appears to them as a standard USB keyboard and mouse.
- No compatibility with the Python projects' command lines, configuration or wire formats.

## Hardware and wiring

The KVM host runs Linux or macOS on amd64 or arm64. Use a CH9329 HID emulator behind a
supported CH340/CH341 USB-UART and a UVC HDMI capture card with USB audio:

1. Connect the target's HDMI output to the capture card's HDMI input.
2. Connect the capture card's USB side to the KVM host.
3. Connect the CH340/CH341 serial USB side to the KVM host, and the CH9329 HID
   USB side to the target. The target sees a normal keyboard and mouse.

Combined boards expose separate host-serial and target-HID connections. Follow
the board's pinout when its UART components are separate. Configure the target
as a single display or mirrored displays so the capture represents the complete
screen used by absolute input. Set the target's actual keyboard layout and
coordinate dimensions; they need not equal the card's capture resolution.

## Installation

The repository is private. Your GitHub account needs access to clone it or
download release assets. Install the [GitHub CLI](https://cli.github.com/) and
authenticate:

```sh
gh auth login
gh auth setup-git
```

### Build from source now

This path works before the first release. With Go 1.27.4 or newer, clone the
repository and build the commands for your computer:

```sh
gh repo clone linkdata/autopaws
cd autopaws
CGO_ENABLED=0 go build -trimpath -o dist/ ./cmd/...
```

On Windows PowerShell, replace the build line with:

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o dist/ ./cmd/...
```

Linux and macOS builds produce both commands. Windows builds produce only
`autopaws-mcp`. The macOS daemon requires FFmpeg on `PATH`; see
[macOS setup](docs/macos-setup.md). Source builds without release
linker flags report version `devel`. On a Linux or macOS KVM host, install both commands:

```sh
sudo install -m 0755 dist/autopaws-kvm dist/autopaws-mcp /usr/local/bin/
autopaws-kvm --version
```

For MCP on another computer, use the absolute path to the built binary, or
install it at a stable location such as `C:\Tools\autopaws\autopaws-mcp.exe` on
Windows. GUI clients may not inherit your shell's `PATH`.

On macOS or Linux, an alternative MCP-only installation uses the private module:

```sh
GOPRIVATE=github.com/linkdata/autopaws CGO_ENABLED=0 go install \
  github.com/linkdata/autopaws/cmd/autopaws-mcp@main
```

This installs the current `main` source, not a release, into `GOBIN` or
`$(go env GOPATH)/bin` when `GOBIN` is unset. The authenticated Git setup above
is still required; [GOPRIVATE](https://go.dev/ref/mod#private-modules) does not
grant repository access itself.

### Install a published release

Once a release is available, list it through the authenticated CLI:

```sh
gh release list --repo linkdata/autopaws
```

Choose a tag shown in that list. If there are no releases yet, use the source
build above. For a Linux amd64 KVM host, run the following in Bash and enter the
published tag when prompted:

```bash
read -r -p "Published release tag: " autopaws_version
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

Use `linux_arm64` for an arm64 KVM host. Download the matching `autopaws-mcp`
asset for the MCP computer and install it as `autopaws-mcp` or `autopaws-mcp.exe`.

| Command | Release OS/architecture pairs |
|---|---|
| `autopaws-kvm` | `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64` |
| `autopaws-mcp` | `linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`, `windows_amd64`, `windows_arm64` |

Filenames are `COMMAND_TAG_OS_ARCH`, with `.exe` on Windows. `SHA256SUMS` covers
all ten binaries. On macOS, use `shasum -a 256 --check -` for verification.
On Windows, compare the selected checksum with
`Get-FileHash -Algorithm SHA256 .\autopaws-mcp_TAG_windows_ARCH.exe` before
installing it. [Release downloads](https://cli.github.com/manual/gh_release_download)
require repository access; there is no anonymous install URL.

macOS binaries are not notarized. If a browser download carries quarantine and
macOS blocks it, verify the binary first, then remove its quarantine attribute:

```sh
xattr -d com.apple.quarantine /absolute/path/to/autopaws-mcp
```

## Quick start on the KVM host

For macOS, follow [macOS setup](docs/macos-setup.md) for FFmpeg, device selectors
and camera/microphone permissions. The commands below describe Linux.

On Debian/Ubuntu-style systems, grant your login user device access, then sign
out and back in:

```sh
sudo usermod --append --groups dialout,video,audio "$USER"
```

Group names vary by distribution. See [Linux setup](docs/linux-setup.md) for
device permissions, stable paths, the example udev rule and an optional systemd
service. List devices and start the daemon in the foreground. Replace the serial
path, screen dimensions and layout with the target's actual settings:

```sh
autopaws-kvm --list-devices
autopaws-kvm \
  -serial-device /dev/serial/by-id/REPLACE_WITH_YOUR_CH340_OR_CH341 \
  -serial-baud 9600 -screen-width 1920 -screen-height 1080 \
  -target-layout us104 -web 127.0.0.1:9330
```

Prefer `/dev/serial/by-id`; use `/dev/serial/by-path` when the adapter has no
unique ID. Replugging can change `ttyUSBn`. Discovery chooses the default video
and its matching USB audio; specify `-capture-device` when needed. The
default `relative` button mode is used by the tested Linux target. Use one
operator at a time.

Open `http://localhost:9330` on the KVM host and click the image to focus input.
For a remote host, follow [Security](#security) to forward both API and web ports
through SSH and open the forwarded localhost URL. Use the [Audio control](#browser-audio)
to start playback with a user gesture. If the capture PCM is busy, follow the
canonical [WirePlumber setup](alsa/README.md#exclusive-access-with-wireplumber-05).

Authentication is off in this foreground example. To enable it, follow
[Security](#security) and configure `-token-file PATH` or the YAML setting. Each
MCP process needs its own readable file containing the same secret.

## Commands and configuration

The command line is independent of the Python tools. Run `autopaws-kvm` with
daemon flags, or use a flag to show help, print the version or list devices:

```sh
autopaws-kvm --help
autopaws-kvm --version
autopaws-kvm --list-devices
```

With no arguments, `autopaws-kvm` starts the daemon with the API at
`127.0.0.1:9329` and relative mouse button reports. The web viewer is disabled
until configured. Each listen address includes a numeric port; an empty address
disables that listener. At least one listener must remain enabled. To serve both
protocols:

```sh
autopaws-kvm -web 127.0.0.1:9330
```

To run the API on its own:

```sh
autopaws-kvm
```

To serve only the web viewer, explicitly disable the default API:

```sh
autopaws-kvm -api '' -web 127.0.0.1:9330
```

Use `-mouse-button-mode absolute` to select absolute button reports explicitly.

The [API protocol reference](api/PROTOCOL.md) describes every method, validation
limit, result and error. `apiclient` provides its portable Go client.
Command help and `--version` work without attached hardware or a configuration file.
Help exits 0; positional arguments, malformed flags and invalid configuration exit 2.
A runtime startup failure exits 1.

On Linux, `autopaws-kvm --list-devices` lists supported CH340/CH341 serial ports, capture
video nodes with their pixel formats, and capture PCM devices. It queries video
capabilities and formats without starting or changing a stream; tty and PCM
devices are not opened. Metadata-only video nodes and unrelated serial ports
are omitted. The `*` marker shows automatic defaults. Multiple supported serial
ports remain ambiguous and require `-serial-device`. Empty inventories are
successful listings. Listing needs no listener or configuration file.
On macOS, it lists AVFoundation selectors without camera format enumeration or
USB audio pairing; see [macOS setup](docs/macos-setup.md).
Use `--version` or `--list-devices` on its own.

There are no subcommands. The former `serve`, `version` and `list-devices`
spellings are not accepted.

Linux video selection uses numeric node order, preferring the first name without a
webcam keyword and otherwise the first capture node. Automatic audio matches the
selected video's complete USB device path, so identical cards do not select each
other's audio. With several capture PCMs on that USB device, the lowest numeric
card/device pair wins. No match means audio unavailable; `audio-device: off`
disables it and an explicit PCM path stays independent of video selection. Audio
capture starts with the first web viewer and stops when the last viewer leaves;
the listing command only shows the discovery result.

### Precedence and files

Built-in defaults are replaced by the explicitly selected YAML file, then by
explicit command-line flags. There are no environment overrides or automatic
configuration-file searches. Select exactly the file you intend to use:

```sh
autopaws-kvm -config ./autopaws.yaml -web-fps 30
```

Explicit false, empty and zero values override the file too. For example,
`-autocrop=false` disables a file's enabled setting and `-web=` disables its web
listener. Boolean flag values use `=false` or `=true`; a separate trailing word
such as `-autocrop false` is an error.

The file is one YAML mapping. Unknown or duplicate keys, null values, wrong value
types, an empty file and extra YAML documents are errors. Omitted keys retain
their defaults. Values must be scalars; YAML aliases and merge keys are not
supported. There is no reload or SIGHUP configuration support.

```yaml
api: 127.0.0.1:9329
web: 127.0.0.1:9330
token-file: ""
serial-device: /dev/serial/by-id/usb-your-CH340-device
serial-baud: 9600
screen-width: 1280
screen-height: 720
target-layout: us104
mouse-button-mode: relative
capture-width: 1920
capture-height: 1080
autocrop: true
audio-device: ""
log-level: info
debug-keys: false
```

### Daemon settings

Flag names and YAML keys match. Empty device settings select automatic discovery;
configured paths do not have to exist while parsing configuration.

| Setting | Default | Meaning |
|---|---|---|
| `api` | `127.0.0.1:9329` | JSON Lines API `host:port`, or empty to disable |
| `web` | empty | Browser viewer `host:port`, or empty to disable |
| `token-file` | empty | UTF-8 shared-secret file for both API and web; empty disables authentication |
| `serial-device` | empty | Serial device path; empty auto-detects CH340/CH341 |
| `serial-baud` | `9600` | Standard rate from the supported list below |
| `screen-width` | `1920` | Positive target coordinate width in pixels |
| `screen-height` | `1080` | Positive target coordinate height in pixels |
| `target-layout` | `us104` | Target text layout name |
| `layouts-dir` | empty | Custom layout directory searched before built-ins |
| `mouse-button-mode` | `relative` | Relative button reports, or explicit `absolute` |
| `capture-device` | empty | Video device path, such as `/dev/video0`, or macOS `avfoundation:NAME`; numeric indexes are unsupported |
| `capture-width` | `1920` | Positive requested capture width, at most 4294967295 |
| `capture-height` | `1080` | Positive requested capture height, at most 4294967295 |
| `autocrop` | `true` | Automatically crop detected black borders |
| `web-fps` | `20` | Viewer frame rate, from 1 through 120 |
| `audio-device` | empty | On Linux, empty matches the video USB device; on macOS select `avfoundation:NAME` explicitly. `off` disables audio; Linux also accepts a PCM device path |
| `log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `debug-keys` | `false` | Emit input/packet debug records independently of ordinary log level |

Supported baud rates are 1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600 and
115200. Screen dimensions may be one pixel; that axis maps to HID zero. Screen
size describes the target coordinate system; capture size describes the video
stream, which can have a different resolution. See [keyboard layouts](CREATING_LAYOUTS.md)
for target OS differences and custom files.

Addresses support hostnames, IPv4, bracketed IPv6 such as `[::1]:9329`, and an
empty host such as `:9329` for all interfaces. Ports range from 0 through 65535;
zero requests an ephemeral port and is useful for tests. Address validation does
not resolve hostnames or bind sockets. Device support and capture negotiation
are checked when the relevant service opens hardware.

Logs go to stderr. Enabling `debug-keys` keeps key/packet records visible even at
ordinary `warn` or `error` level; unrelated debug logs remain filtered. Input logs
can reveal typed content, so enable them only while diagnosing input.

SIGINT and SIGTERM initiate shutdown. The shared input controller stops normal
actions and attempts release before permanently closing serial transport. The
one-second cleanup context bounds application waits; it cannot interrupt a Linux
driver call already executing, as described in [serial I/O and cancellation](#serial-io-and-cancellation).

## V4L2 capture

The Linux `v4l2` package captures MJPG through memory-mapped streaming. It requests
a size and reports the driver's negotiated size; it leaves frame rate at the
driver's native setting. Frames own copied bytes and retain the driver's raw
timestamp, clock/source flags and sequence number. A monotonic timestamp is not
a calendar time. This layer preserves compressed bytes; the shared frame
pipeline normalizes JPEGs and provides API snapshots.

`Probe` lists node capabilities and capture formats with read-only queries. It
does not negotiate a format or start a stream. `Open` requires single-planar
streaming capture and MJPG; unsupported formats, invalid descriptors and device
loss have matchable errors. Cancellation wakes a parked frame read, and `Close`
waits for active copying before unmapping. Synchronous Linux driver calls can
outlast a context; ownership is retained until they return.

Go poll registration happens after streaming starts: registering earlier can
cache the driver's expected pre-stream `EPOLLERR` and prevent frame reads. ABI
checks and their C-header provenance are in [v4l2/testdata](v4l2/testdata/README.md).
Actual capture observations are recorded in [hardware validation](docs/hardware-validation.md).

On macOS, [the AVFoundation backend](docs/macos-setup.md) uses FFmpeg and
estimated pipe receipt timing, with weaker snapshot freshness guarantees than
Linux device timestamps. Stalled FFmpeg reads time out after five seconds;
native macOS video and explicit audio retry after one second while demand remains.
The healthy ten-second idle timeout is unchanged.

The `capture` package shares one producer between subscriptions and snapshots.
Capture starts on demand and stops after ten seconds with neither subscribers
nor pending snapshots. A snapshot waits for a successfully decoded frame whose
`CLOCK_MONOTONIC` timestamp is later than the request (device capture time on
Linux, estimated pipe read-start time on macOS). Its total
five-second acquisition deadline includes startup and corrupt warmup; an earlier
caller cancellation wins. There is no stale-frame fallback. On Linux this
establishes capture timing; on macOS FFmpeg/OS buffering is unmeasured. Neither
proves that the target application has finished reacting to preceding input or
that the HDMI source has stabilized.

Every frame has JPEG padding removed and missing standard Huffman tables repaired.
With autocrop enabled, the producer detects black borders on the first decodable
frame, every thirtieth frame and a requested snapshot while no crop is known.
The detector uses luma greater than 20, a border of at least four pixels and at
least 64 active rows and columns. Snapshots apply a detected crop with one cached
quality-85 encode; uncropped snapshots return the normalized device JPEG. Decodes
and cropped encodes are shared across requests for that frame.

Selecting a device or resolution starts a new settings generation, clears old
queued frames/crop information and returns `capture changed` to pending old
snapshots. Successful automatic macOS video recovery also advances the generation
and clears old frame/crop state; healthy idle opens retain it. Idle or failed-open
settings remain repairable without restarting the daemon. Missing capture hardware
leaves keyboard and mouse usable. Initial valid black/noisy HDMI acquisition
pictures are possible on the tested card; another
capture can observe the established picture without restarting the warm source.

## Browser viewer

`-web host:port` enables HTTP assets and one WebSocket per viewer at `/ws`.
It works on its own or alongside `-api`. All requested addresses bind before
either service begins accepting; a startup failure closes the bound addresses
and shared hardware owners. Shutdown cancels viewers and starts network, input
and capture cleanup concurrently, then joins every owner and handler.

The [browser protocol](web/PROTOCOL.md) defines the video header, crop metadata,
clock flags, generations and physical input messages. Video uses the shared
pipeline's normalized JPEG without per-viewer encoding. Slow viewers skip frames,
and every socket write has a deadline. A passive viewer disconnect sends no input
release. A controlling disconnect releases its held input through the shared
coordinator, sharing an existing cleanup deadline with overlapping shutdown.

Browser input uses physical `KeyboardEvent.code`, independently of the selected
text layout. Held keys retain press order, up to six. Complete shortcut messages
hold the action gate across press and release; raw key/button events remain
ordered. Browser mouse coordinates are normalized HID values 0 through 4095,
with held-button movement matching the API in both button modes.

The embedded viewer draws each frame's crop rectangle on a canvas and maps the
visible pixel endpoints to the target's full mouse range. Click the video to
send physical key positions and mouse input. The toolbar sends complete
Ctrl+Alt+Delete and Alt+Tab actions, toggles view-only mode and the local cursor,
and switches between fit and original pixel size. Fullscreen has an auto-hiding,
draggable toolbar; moving to the top edge reveals it. The frame counter measures
rendered frames. Lost connections reconnect automatically without replaying input.

Entering view-only mode, losing focus, hiding the page or changing video
streams releases this viewer's held input. A viewer with no held input sends no
global release. Mouse movement retains only the latest pending position at 30 Hz;
button, key and wheel events flush that position first to preserve their order.
The rate leaves room for discrete actions on the measured 9600-baud serial link.

Browser capabilities are detected at runtime. Keyboard Lock is attempted in
fullscreen when available; if it is denied, toolbar shortcuts remain usable.
The installable app uses an online-only service worker and caches no assets or
media. A localhost SSH tunnel provides a secure context for compatible browser
APIs, including AudioWorklet playback.

### Browser audio

Click Audio to start playback. The control is hidden when capture is disabled or
unavailable. Muting closes and flushes the browser audio graph; configuration
changes and reconnects preserve that choice. While audio is loading, Cancel audio
stops it. If playback is interrupted, Retry audio starts a fresh attempt.
Capability or sample-rate failures display an explanation while video and input
remain usable. Recovery at an unchanged format flushes old samples without
restarting the running audio graph.

The context requests the negotiated capture rate and verifies it before playing
interleaved S16_LE PCM. The browser has no resampler; the macOS backend uses
FFmpeg to convert input to 48 kHz stereo. Audio shares each viewer's
WebSocket and takes priority over a waiting video frame. Every accepted video
generation, including successful macOS recovery, reopens the selected audio
source and flushes old samples. On Linux, automatic selection follows the new
video's USB path; an explicit PCM path stays fixed. macOS requires explicit audio
selection. Changed configuration is sent before new-generation samples. An audio
failure reports unavailable without stopping video/input; changing capture
settings or closing and reopening the last viewer retries acquisition. Native
macOS audio with an explicit device also retries after one second while viewers
remain.

The server retains at most 100 ms of PCM per viewer. The browser retains a 50 ms
worklet ring plus at most one acknowledged 50 ms transfer, keeping application
queues within 200 ms total. Oldest/stale audio is discarded and underruns produce
silence. While the one transfer awaits acknowledgement, new arriving blocks are
dropped; ring overflow and oversized-block trimming discard oldest samples. A
video write lasting at least 100 ms discards its accumulated audio queue before
resuming. Capture duration, kernel, TCP/WebSocket and output-device
latency are separate; the queue limit does not establish the 200 ms end-to-end
target. Measurements and tested configurations are recorded in
[the hardware validation document](docs/hardware-validation.md).

See [raw ALSA notes](alsa/README.md) for exclusive device access, timestamp
estimation and WirePlumber setup.

## MCP server

Configure an MCP client to start `autopaws-mcp` over stdio. It connects to a running
KVM API and owns no hardware:

```sh
autopaws-mcp -api 127.0.0.1:9329
```

| Flag | Default | Meaning |
|---|---|---|
| `-api host:port` | `127.0.0.1:9329` | KVM API address with a numeric port from 1 through 65535 |
| `-capture-log DIR` | empty, off | Save unchanged screenshot JPEGs in the selected directory |
| `-token-file PATH` | empty, off | Read the API shared secret once at startup |
| `-tesseract PATH` | search automatically | Optional Tesseract executable; an explicit missing path disables OCR |

Each agent process has its own MCP session. Tools also isolate screenshot state
when multiple SDK sessions share one server instance. The server uses the
[official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) and reserves
stdout for MCP messages; logs go to stderr. Help exits 0, invalid flags exit 2 and
an unexpected transport failure exits 1. Normal client exit or signal shutdown
exits 0.

| Tools | Purpose |
|---|---|
| `type_text`, `send_key`, `send_key_sequence` | Complete keyboard actions through the API |
| `list_keyboard_layouts` | Report the active layout and available built-in/custom maps |
| `set_keyboard_layout` | Select the daemon's active text map without restarting or reconnecting MCP |
| `mouse_move`, `mouse_click`, `mouse_drag`, `mouse_scroll` | Pointer actions; a drag is one API request |
| `capture_screen` | Return the daemon's JPEG unchanged plus image dimensions |
| `get_screen_text`, `execute_and_read` | Optional local English OCR, with command execution through the keyboard |
| `get_device_info`, `list_capture_devices`, `set_capture_device`, `set_capture_resolution` | Read target/device information and select capture settings |

Call `list_keyboard_layouts` with no arguments to inspect the active map and
available names. Entries report their `builtin` or `custom` source; an `error`
marks a map that cannot be loaded. Then call `set_keyboard_layout` with
`{"layout":"se105"}` after separately selecting
Swedish on the target OS. It returns `{"keyboard_layout":"se105"}` and applies
to subsequent keyboard actions; `get_device_info` reports the active name.
Custom names are `*.yaml` stems in the daemon's configured `layouts-dir`; a
custom file overrides a built-in of the same name. Selecting the same name
reloads its file; an invalid selection leaves the working map
unchanged. Runtime selection supports one MCP connection with sequential
requests. Restarting the daemon restores its CLI/YAML startup layout. See
[changing the active layout](CREATING_LAYOUTS.md#changing-the-active-layout).

MCP absolute coordinates are integer pixels in that session's latest successful
`capture_screen` image. Capture first before an absolute move, positioned click
or drag. Both screenshot endpoints map to the target endpoints, including cropped
or scaled images; a one-pixel image or target axis maps to zero. Relative deltas
remain unscaled and a click at the current pointer needs no screenshot. A capture
device/resolution change attempt invalidates the old screenshot even when the API
reports failure. Listing or changing keyboard layouts preserves screenshot coordinates.
Screenshots use the shared pipeline's fresh, optionally cropped
JPEG; MCP returns those bytes unchanged.

Cancelling a tool or closing its MCP connection closes the active API call,
allowing the daemon to finish its release-only cleanup. Complete input actions
share the daemon's gate. V1 supports one operator; independent agents do not merge
held keys/buttons.

Capture logging is opt-in and has no retention limit. On POSIX systems its
directory is mode `0700` and files are mode `0600`; platform access controls apply
on Windows. Temporary-file names include timestamps and random suffixes, so
captures cannot overwrite each other. These files contain the complete screenshot.
When Tesseract with English language data is installed, two additional tools are
offered. `get_screen_text` captures a fresh frame and returns recognized text.
`execute_and_read` types the command plus Enter as one raw keyboard action, waits
`wait_seconds` (default 1, finite 0 through 60), then captures fresh output for
OCR. The wait is cancellable; a fresh capture does not prove command completion.
OCR returns text and never establishes or replaces screenshot coordinates.

Tesseract is resolved once at startup: `-tesseract` wins, otherwise search `PATH`,
then on Windows `Tesseract-OCR/tesseract.exe` under `ProgramFiles` and
`ProgramFiles(x86)`. An invalid explicit override disables OCR with a warning on
stderr and does not fall back. Restart the MCP process after installing the
engine. The other tools remain available without it.

OCR uses JPEG luminance, inverts dark backgrounds, scales 2× with nearest-neighbor
sampling, and sends binary PGM over stdin to
`tesseract stdin stdout --oem 3 --psm 6`. It creates no temporary OCR input/output
files. Each process has a 15-second timeout; cancellation kills and reaps the
owned process, with a one-second bound on inherited-pipe waits. OCR/API/process
failures are reported as MCP tool errors.

The [synthetic corpus](cmd/autopaws-mcp/testdata/ocr/README.md) compares Go against
the original Python preprocessing. With Tesseract 5.5.0, both recognized 240 of
241 combined characters, with identical output on all four fixtures. The
PowerShell-style fixture is synthetic, and does not claim Windows runtime
validation.

### MCP client configuration

`autopaws-mcp` is a local stdio process. Its `-api` address is the KVM JSON Lines
service, not an HTTP MCP endpoint. Each client starts its own process against
the shared daemon.

For Claude Code, register the installed binary with server flags after `--`:

```sh
claude mcp add --transport stdio --scope user autopaws -- \
  /absolute/path/to/autopaws-mcp -api 127.0.0.1:9329
```

For Claude Desktop, open Settings → Developer → Edit Config and add the entry
under `mcpServers`, preserving any other servers:

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

On Windows the JSON command can be `C:\\Tools\\autopaws\\autopaws-mcp.exe`.
Restart the client after changing its configuration. When authentication is
enabled, append `-token-file` and the matching local file path to the command
or `args` array; keep the secret value in that file. For a remote KVM, use the
local API port from the [SSH tunnel](#security).

These examples follow the [Claude Code stdio instructions](https://code.claude.com/docs/en/mcp#option-3-add-a-local-stdio-server)
and [local MCP client setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers).
They do not claim runtime validation of every client and OS combination.

## Security

Both the API and web viewer can type on and click on the connected computer.
Keep them bound to loopback. Browser view-only mode is a local convenience and
does not restrict another client. `-debug-keys` logs typed text and input reports;
leave it disabled unless those records are needed.

Optional authentication uses a shared secret. Generate a 256-bit token as 64 hex
characters and keep the file private:

```sh
umask 077
openssl rand -hex 32 > ./autopaws.token
```

Start the daemon with `autopaws-kvm -token-file ./autopaws.token` (or the
`token-file` YAML setting). Both daemon and MCP trim surrounding whitespace and
read their files once at startup; restart them to change the secret. A configured unreadable,
empty, or invalid UTF-8 file fails startup. Start MCP with
`autopaws-mcp -api 127.0.0.1:9329 -token-file PATH`. Other API clients include the
matching string in the `token` request field or use `apiclient.NewWithToken`.

Open the viewer at `http://localhost:9330/#token=ENCODED_SECRET`, replacing
ENCODED_SECRET with `encodeURIComponent(secret)`. The page removes that fragment
and stores the secret in a session cookie before connecting. The fragment is
never sent to the server; bootstrap assets remain public and the WebSocket requires
the cookie. Tokens are never logged. Without a configured token, startup warns
for each non-loopback listener using its actual bound address.

The token does not encrypt traffic. For remote use, bind the daemon API to
`127.0.0.1:9329` and web server to `127.0.0.1:9330`, then run:

```sh
ssh -L 9329:127.0.0.1:9329 -L 9330:127.0.0.1:9330 kvmhost
```

MCP connects to local port 9329; open `http://localhost:9330` in the browser
(with the token fragment when enabled). Forward both ports. Localhost HTTP is a
trustworthy origin for browser secure-context features, including AudioWorklet.
Direct plain HTTP to a LAN address is not; capability checks explain unavailable
features. Direct LAN browser audio, built-in HTTPS and supported reverse-proxy
setups are outside v1 scope. See the [Web Audio specification](https://webaudio.github.io/web-audio-api/#audioworklet)
and [Secure Contexts specification](https://www.w3.org/TR/secure-contexts/#is-origin-trustworthy).

## Coming from the Python projects

Use `autopaws-kvm` in place of `serial-hid-kvm`, and `autopaws-mcp` in place
of `mcp-serial-hid-kvm`. Python CLI flags, YAML keys, environment variables, wire
formats and MCP tool signatures are not compatible with this redesign. Start
with the Go help and references rather than copying the old configuration.
The browser replaces the native preview.

## Tested configurations and limits

The [hardware validation record](docs/hardware-validation.md) identifies the
tested Linux host, target, hardware and browser configurations and their actual
results. [Release notes](docs/release-notes.md) track the first-release gate.
Required Linux core checks passed on the recorded setup, including physical
serial and capture/audio USB recovery, Chromium/Firefox video/audio, and
screenshot-scaled input. The validation record retains the initial ALSA recovery
defect, its fixed-build physical retest and known browser/shutdown diagnostics.
Final tagged artifacts follow the [release procedure](docs/releasing.md).

A separate [limited read-only Windows laptop smoke](docs/validation/windows-readonly-2026-11-18.json)
passed capture and safe keyboard/pointer checks. Only test actions and outcomes
are recorded; no screen content or images are retained. No scripts were placed on the target,
and the agent changed no target files or settings. This does not establish text-layout,
drag or audio coverage. Broader Windows/macOS target tests, additional ARM64
hardware, Safari and browser checks on other operating systems remain optional
extended coverage. Supported-platform cross-builds remain required; they do not
establish runtime or hardware validation. Extended card/OCR coverage and known
limits are recorded separately from the required Linux results.

The [macOS host record](docs/validation/macos-host-2026-11-20.json) covers an Apple
silicon Mac controlling an Ubuntu target: requested Swedish text, API/MCP pointer
coordinates, video, HDMI left/right speaker content, serial reconnect and clean
shutdown passed. Baseline capture reconnect needed explicit same-device repair;
the updated code resumed video/audio automatically on the existing WebSocket
after a physical USB reconnect. Ubuntu's HDMI output needed selecting again
after its fallback to internal speakers. Acoustic/browser audio playback and
latency remain unvalidated.
See [macOS setup](docs/macos-setup.md).

## Development

The hardware, software and host setup needed for development are listed in the [tracking epic](../../issues/23).

Use Go 1.27.4 or newer, Node.js 26.13.1, and the golangci-lint version pinned in [the CI workflow](.github/workflows/ci.yml). Install [actionlint](https://github.com/rhysd/actionlint/blob/main/docs/install.md) and [ShellCheck](https://www.shellcheck.net/) locally too, with these tools on `PATH`.

After the final edits and before pushing or starting GitHub Actions, validate the workflow locally:

```sh
actionlint
bash -n .github/scripts/build-release.sh
shellcheck .github/scripts/build-release.sh
```

Browser checks use Node's built-in test runner and Web APIs; no npm install is
needed. Run the complete gate below on Linux and macOS; a local Linux VM is sufficient
for the Linux checks when developing on macOS. The macOS CI job runs the native
Go build, lint and race tests; the Linux job also runs browser and cross-build
checks. From the repository root:

```sh
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

On macOS, install FFmpeg (`brew install ffmpeg`) and also run the native CI
media fixtures. They generate test patterns and tones without opening devices:

```sh
AUTOPAWS_FFMPEG_TEST=1 CGO_ENABLED=1 go test -race ./avfoundation
```

The browser interaction tests import the actual page module with a small DOM,
WebSocket, timer and deferred-image harness. Physical and real-browser checks
are recorded in [the hardware validation document](docs/hardware-validation.md).

The race check needs a C compiler and cgo enabled; release and cross-build checks use `CGO_ENABLED=0`.

CI uploads the Go coverage profile and HTML report as a `coverage` artifact.
Successful `main` pushes also publish the coverage badge and report to the
`gitcoverage` branch using [gitcoverage](https://github.com/linkdata/gitcoverage),
as in [jaws](https://github.com/linkdata/jaws). The badge measures Go statement
coverage; browser tests run separately. Its HTML preview link will work once this
repository is public. While it is private, download the `coverage` artifact to
view the HTML locally. No additional secret is needed.

To build both host commands locally:

```sh
go build -o dist/ ./cmd/...
```

### Raw ALSA capture

The Linux `alsa` package captures interleaved S16_LE directly through the kernel
PCM interface, with no runtime audio library or helper process. It prefers
48 kHz stereo, falls back to supported mono/rate settings, and reports the
negotiated format. One worker owns reads and descriptor close; DROP wakes reads
on shutdown, and every executing kernel operation retains ownership until it
finishes. Kernel calls can outlast application deadlines.

Overrun or suspend recovery uses PREPARE, changes the audio generation and
flushes subscriber queues. Each subscriber retains at most 100 ms of samples,
drops oldest blocks on overflow and expires blocks by end-of-capture age before
delivery. Capture duration, kernel buffering and browser/output latency are
separate from this application queue budget. See [the package notes](alsa/README.md)
for timestamp estimation, ownership and the explicitly selected hardware tests.
The web viewer shares this capture source through the single ordered media writer.

### Serial I/O and cancellation

The serial package waits for the host output queue to empty, then applies baud-based pacing. This does not confirm CH9329 acknowledgement or target input processing. Request cancellation and a one-second application budget bound write readiness, queue polling and pacing.

An admitted send failure invalidates the current preparation, including cancellation. Release-only cleanup must explicitly call `Prepare` with an uncancelled cleanup context: a changed generation means the reset already released all input and software held state must be cleared. A still-ready generation can send its matching release. Cleanup never replays the interrupted action, and permanent `Close` runs after the cleanup attempt.

Linux USB-serial open, termios configuration and close can wait inside the driver beyond an application deadline. Go contexts cannot interrupt those kernel calls. The package retains ownership until they finish and checks cancellation between calls; it does not abandon blocked goroutines or reuse a descriptor while close is still running. Hardware tests record actual timing rather than asserting a universal kernel shutdown bound.

### Keyboard layouts

The `hid` package provides the built-in `us104`, `uk105`, `de105`, `fr105`, `jp106` and `se105` layouts. Layouts describe the target keyboard. See [Creating keyboard layouts](CREATING_LAYOUTS.md) for custom files and override precedence.

### Keyboard actions

Construct one `hid.Controller` for the transport and share it with the input devices. Its action gate covers each complete text, key or sequence operation, including delays and release cleanup. Close the controller during shutdown so releases run before the transport is permanently closed.

`Keyboard.TypeText` supports tags such as `{enter}`, `{ctrl+c}` and `{shift+0x87}`; unknown tags remain literal text. `{{` and `}}` type single braces. Raw mode types braces and backslashes literally and converts actual CRLF, CR and LF into Enter. Every request is validated completely before input begins; an unsupported character error includes its zero-based rune position, and a tag error points to its opening brace.

Each key is held for 10 ms before release. Text defaults to a 20 ms delay after every character or tag, and sequences default to 100 ms after every step, including the last. Explicit zero disables that delay; a layout's multiple strokes have no character delay between them. Delays must be between zero and one minute, and sequences contain at most 4096 steps. Cancellation stops application waits and attempts release under a separate one-second cleanup context, subject to the serial kernel-call limits above.

### Mouse actions

`hid.Mouse` shares that controller. Absolute coordinates are integer target-screen pixels, from zero through width/height minus one. The endpoints map exactly to HID 0 and 4095; a one-pixel axis maps to zero. Relative movement must be within −127…127 on each axis, and wheel movement is clamped to that range. Invalid coordinates, buttons and modes are rejected before input begins.

Buttons are `left`, `right` and `middle`; an empty name means left. A click holds for 10 ms, and a drag holds the shared gate through down, 50 ms, move, 50 ms and up. Clicks release the selected button and preserve other held buttons. The configured button mode defaults to `relative`, which sends positioned button changes as a button-free absolute move followed by a relative button report. Set `-mouse-button-mode absolute` to use absolute button reports instead. Absolute movement preserves held buttons in both modes. See the [physical input validation record](docs/hardware-validation.md).

## Repository layout

- `cmd/autopaws-kvm/`: Linux/macOS KVM daemon entry point (amd64 and arm64).
- `cmd/autopaws-mcp/`: portable MCP stdio entry point.
- `ch9329/`: portable keyboard and mouse frame encoders.
- `serial/`: Linux/macOS serial transport, recovery and CH340/CH341 discovery.
- `devices/`: Platform-specific serial, video and audio inventory.
- `v4l2/`: Linux MJPG streaming capture, timestamps and read-only format probes.
- `capture/`: shared video lifecycle, fresh snapshots, JPEG normalization and crop metadata.
- `web/`: HTTP/WebSocket serving, browser input and the media wire protocol.
- `hid/`: portable keycodes, immutable layouts and serialized keyboard/mouse actions.
- `api/`: Shared daemon API handlers and TCP connection lifecycle.
- `apiwire/`: portable envelopes, stable error identities and bounded line framing.
- `apiclient/`: portable API client, independent of daemon packages.
- `internal/config/`: validated command flags, YAML settings and logging setup.
- `avfoundation/`: macOS FFmpeg device discovery and video/audio capture.
- `alsa/`: raw Linux PCM capture, negotiation, recovery and bounded subscriber queues.

## License

MIT. See [LICENSE](LICENSE).
