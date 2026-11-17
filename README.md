# autopaws

autopaws is a software KVM for Linux hosts. It controls a target machine through a CH9329 USB HID emulator and shows the target's screen in a browser through a USB HDMI capture card. AI agents get the same keyboard, mouse and screen access through an MCP server.

It is a Go rewrite of two Python projects:

- [serial-hid-kvm](https://github.com/linkdata/serial-hid-kvm), the KVM server
- [mcp-serial-hid-kvm](https://github.com/linkdata/mcp-serial-hid-kvm), its MCP server

The rewrite is underway. The repository currently provides build and CI scaffolding; the command binaries are stubs and do not control hardware yet. The [tracking epic](../../issues/23) lists the implementation issues in execution order.

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

## Development

The hardware, software and host setup needed for development are listed in the [tracking epic](../../issues/23).

Use Go 1.27.4 or newer and the golangci-lint version pinned in [the CI workflow](.github/workflows/ci.yml). From the repository root, run the same gate as CI:

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go build -o /dev/null ./cmd/autopaws-kvm
golangci-lint run
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/autopaws-kvm
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/autopaws-mcp-darwin ./cmd/autopaws-mcp
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/autopaws-mcp-windows.exe ./cmd/autopaws-mcp
```

The race check needs a C compiler and cgo enabled; release and cross-build checks use `CGO_ENABLED=0`. The root module's `./...` patterns exclude the nested modules in `research/`. CI deliberately skips those throwaway prototypes; they remain reference material until their replacements land.

To build both Linux commands locally:

```sh
go build -o dist/ ./cmd/...
```

## Repository layout

- `cmd/autopaws-kvm/`: Linux KVM daemon entry point (amd64 and arm64).
- `cmd/autopaws-mcp/`: portable MCP stdio entry point.
- Library packages are added with their implementation issues: `ch9329`, `serial`, `hid`, `api`, `apiclient`, `devices`, `v4l2`, `frame`, `web` and `alsa`. `apiclient` stays portable and does not import the daemon or Linux-only packages.
- `research/`: throwaway feasibility prototypes for serial, V4L2 capture, ALSA audio and WebSocket. Each is its own Go module. They are reference material for the rewrite and are deleted once the matching package lands.

## License

MIT. See [LICENSE](LICENSE).
