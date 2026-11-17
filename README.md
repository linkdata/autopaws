# autopaws

autopaws is a software KVM for Linux hosts. It controls a target machine through a CH9329 USB HID emulator and shows the target's screen in a browser through a USB HDMI capture card. AI agents get the same keyboard, mouse and screen access through an MCP server.

It is a Go rewrite of two Python projects:

- [serial-hid-kvm](https://github.com/linkdata/serial-hid-kvm), the KVM server
- [mcp-serial-hid-kvm](https://github.com/linkdata/mcp-serial-hid-kvm), its MCP server

The rewrite is underway. Core input, CLI configuration, device discovery, the TCP API, the stdio MCP server and low-level V4L2 capture are implemented. The shared video pipeline, OCR, audio and browser services are still being ported; API capture calls and web startup currently report unavailable. The [tracking epic](../../issues/23) lists the implementation issues in execution order.

## Components

- **KVM server** (`autopaws-kvm`): a daemon on the Linux host. It owns the serial port and the capture card, and serves a JSON Lines TCP API and the web viewer.
- **MCP server** (`autopaws-mcp`): a separate stdio binary that AI agents start. It is a thin client of the API, and it runs OCR with Tesseract when that is installed. It builds for Linux, macOS and Windows.

## Goals

- Static Linux binaries (amd64, arm64), built with `CGO_ENABLED=0`.
- Standard library first. Third-party modules only when they are widely used and well maintained.
- The browser is the only viewer. There is no native preview window.
- The same hardware as serial-hid-kvm: a CH9329 behind a CH340/CH341 USB-UART, and UVC HDMI capture cards with their USB audio.
- Targets can run any OS. The CH9329 appears to them as a standard USB keyboard and mouse.
- No compatibility with the Python projects' command lines, configuration or wire formats.

## Commands and configuration

The command line is independent of the Python tools. Use a subcommand:

```sh
autopaws-kvm version
autopaws-kvm serve -help
autopaws-kvm list-devices -help
```

`serve` requires an API address, a web address, or both. Each address includes a
numeric port. Empty addresses disable that listener; neither is enabled by
default. For example, the intended API/web configuration is:

```sh
autopaws-kvm serve -api 127.0.0.1:9329 -web 127.0.0.1:9330 -mouse-button-mode relative
```

To run the implemented API on its own:

```sh
autopaws-kvm serve -api 127.0.0.1:9329 -mouse-button-mode relative
```

The [API protocol reference](api/PROTOCOL.md) describes every method, validation
limit, result and error. `apiclient` provides its portable Go client.
Command help and `version` work without attached hardware or a configuration file.
Help exits 0; unknown commands, malformed flags and invalid configuration exit 2.
A runtime startup failure exits 1.

`autopaws-kvm list-devices` lists supported CH340/CH341 serial ports, capture
video nodes with their pixel formats, and capture PCM devices. It queries video
capabilities and formats without starting or changing a stream; tty and PCM
devices are not opened. Metadata-only video nodes and unrelated serial ports
are omitted. The `*` marker shows automatic defaults. Multiple supported serial
ports remain ambiguous and require `-serial-device`. Empty inventories are
successful listings. Listing needs no serve listener or configuration file.

Video selection uses numeric node order, preferring the first name without a
webcam keyword and otherwise the first capture node. Automatic audio matches the
selected video's complete USB device path, so identical cards do not select each
other's audio. With several capture PCMs on that USB device, the lowest numeric
card/device pair wins. No match means audio unavailable; `audio-device: off`
disables it and an explicit PCM path stays independent of video selection. Audio
service startup and switching are wired with the audio work; this command already
shows the discovery result.

### Precedence and files

Built-in defaults are replaced by the explicitly selected YAML file, then by
explicit command-line flags. There are no environment overrides or automatic
configuration-file searches. Select exactly the file you intend to use:

```sh
autopaws-kvm serve -config ./autopaws.yaml -web-fps 30
```

Explicit false, empty and zero values override the file too. For example,
`-autocrop=false` disables a file's enabled setting and `-web=` disables its web
listener. Boolean flag values use `=false` or `=true`; a separate trailing word
such as `-autocrop false` is an error. Flags belong after their subcommand.

The file is one YAML mapping. Unknown or duplicate keys, null values, wrong value
types, an empty file and extra YAML documents are errors. Omitted keys retain
their defaults. Values must be scalars; YAML aliases and merge keys are not
supported. There is no reload or SIGHUP configuration support.

```yaml
api: 127.0.0.1:9329
web: 127.0.0.1:9330
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

### Serve settings

Flag names and YAML keys match. Empty device settings select automatic discovery;
configured paths do not have to exist while parsing configuration.

| Setting | Default | Meaning |
|---|---|---|
| `api` | empty | JSON Lines API `host:port`, or empty to disable |
| `web` | empty | Browser viewer `host:port`, or empty to disable |
| `serial-device` | empty | Serial device path; empty auto-detects CH340/CH341 |
| `serial-baud` | `9600` | Standard rate from the supported list below |
| `screen-width` | `1920` | Positive target coordinate width in pixels |
| `screen-height` | `1080` | Positive target coordinate height in pixels |
| `target-layout` | `us104` | Target text layout name |
| `layouts-dir` | empty | Custom layout directory searched before built-ins |
| `mouse-button-mode` | `absolute` | `absolute` or compatibility mode `relative` |
| `capture-device` | empty | Video device path, such as `/dev/video0`; numeric indexes are unsupported |
| `capture-width` | `1920` | Positive requested capture width, at most 4294967295 |
| `capture-height` | `1080` | Positive requested capture height, at most 4294967295 |
| `autocrop` | `true` | Automatically crop detected black borders |
| `web-fps` | `20` | Viewer frame rate, from 1 through 120 |
| `audio-device` | empty | Empty auto-selects audio for the video USB device; `off` disables it; otherwise a PCM device path |
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
pipeline will normalize JPEGs and provide API snapshots.

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

Each agent process has its own MCP session. Tools also isolate screenshot state
when multiple SDK sessions share one server instance. The server uses the
[official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) and reserves
stdout for MCP messages; logs go to stderr. Help exits 0, invalid flags exit 2 and
an unexpected transport failure exits 1. Normal client exit or signal shutdown
exits 0.

| Tools | Purpose |
|---|---|
| `type_text`, `send_key`, `send_key_sequence` | Complete keyboard actions through the API |
| `mouse_move`, `mouse_click`, `mouse_drag`, `mouse_scroll` | Pointer actions; a drag is one API request |
| `capture_screen` | Return the daemon's JPEG unchanged plus image dimensions |
| `get_device_info`, `list_capture_devices`, `set_capture_device`, `set_capture_resolution` | Read target/device information and select capture settings |

MCP absolute coordinates are integer pixels in that session's latest successful
`capture_screen` image. Capture first before an absolute move, positioned click
or drag. Both screenshot endpoints map to the target endpoints, including cropped
or scaled images; a one-pixel image or target axis maps to zero. Relative deltas
remain unscaled and a click at the current pointer needs no screenshot. A capture
device/resolution change attempt invalidates the old screenshot even when the API
reports failure. Capture support becomes available with the frame pipeline; the
keyboard, relative-movement and current-pointer tools work through the API now.

Cancelling a tool or closing its MCP connection closes the active API call,
allowing the daemon to finish its release-only cleanup. Complete input actions
share the daemon's gate. V1 supports one operator; independent agents do not merge
held keys/buttons.

Capture logging is opt-in and has no retention limit. On POSIX systems its
directory is mode `0700` and files are mode `0600`; platform access controls apply
on Windows. Temporary-file names include timestamps and random suffixes, so
captures cannot overwrite each other. These files contain the complete screenshot.
OCR tools are added with OCR support; they are not registered yet.

## Development

The hardware, software and host setup needed for development are listed in the [tracking epic](../../issues/23).

Use Go 1.27.4 or newer and the golangci-lint version pinned in [the CI workflow](.github/workflows/ci.yml). Install [actionlint](https://github.com/rhysd/actionlint/blob/main/docs/install.md) and [ShellCheck](https://www.shellcheck.net/) locally too, with these tools on `PATH`.

After the final edits and before pushing or starting GitHub Actions, validate the workflow locally:

```sh
actionlint
```

Then, from the repository root, run the same Go gate as CI:

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go build -o /dev/null ./cmd/autopaws-kvm
golangci-lint run
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-mcp
```

The race check needs a C compiler and cgo enabled; release and cross-build checks use `CGO_ENABLED=0`. The root module's `./...` patterns exclude the nested modules in `research/`. CI deliberately skips those throwaway prototypes; they remain reference material until their replacements land.

To build both Linux commands locally:

```sh
go build -o dist/ ./cmd/...
```

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

Buttons are `left`, `right` and `middle`; an empty name means left. A click holds for 10 ms, and a drag holds the shared gate through down, 50 ms, move, 50 ms and up. Clicks release the selected button and preserve other held buttons. The configured button mode defaults to `absolute`; `relative` sends positioned button changes as a button-free absolute move followed by a relative button report. Absolute movement preserves held buttons in both modes. See the [physical input validation record](docs/hardware-validation.md).

## Repository layout

- `cmd/autopaws-kvm/`: Linux KVM daemon entry point (amd64 and arm64).
- `cmd/autopaws-mcp/`: portable MCP stdio entry point.
- `ch9329/`: portable keyboard and mouse frame encoders.
- `serial/`: Linux serial transport, recovery and CH340/CH341 discovery.
- `devices/`: Linux serial, video and audio inventory and USB-path selection.
- `v4l2/`: Linux MJPG streaming capture, timestamps and read-only format probes.
- `hid/`: portable keycodes, immutable layouts and serialized keyboard/mouse actions.
- `api/`: Linux daemon API handlers and TCP connection lifecycle.
- `apiwire/`: portable envelopes, stable error identities and bounded line framing.
- `apiclient/`: portable API client, independent of daemon/Linux packages.
- `internal/config/`: validated command flags, YAML settings and logging setup.
- Further library packages are added with their implementation issues: `capture`, `web` and `alsa`.
- `research/`: throwaway feasibility prototypes for serial, V4L2 capture, ALSA audio and WebSocket. Each is its own Go module. They are reference material for the rewrite and are deleted once the matching package lands.

## License

MIT. See [LICENSE](LICENSE).
