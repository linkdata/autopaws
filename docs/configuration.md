# Configuration

The daemon uses defaults unless you supply flags or a YAML file.

```sh
autopaws-kvm --help
autopaws-kvm --version
```

`-web` listens on `127.0.0.1:9330`; use `-web host:port` to choose an address.
Use `-web=` to disable it, including when enabled in a configuration file.
For a viewer without the API, add `-api ''`. At least one listener must be enabled.
Use `--version` and `--list-devices` separately from daemon flags.

## Configuration file

Precedence is defaults, an explicitly selected YAML file, then explicit flags.
There are no environment overrides, automatic file searches or live reloads:

```sh
autopaws-kvm -config ./autopaws.yaml -web-fps 30
autopaws-mcp -config ./autopaws.yaml
```

See [the example configuration](../examples/autopaws.yaml). YAML settings use the
same names as their flags; `token` is YAML-only. `config`, `version` and `list-devices`
are command-line only. Both binaries accept the same file, sharing
`api`, `token` and `token-file`.
Each uses its own remaining settings; see [MCP options](mcp.md#options-and-tools)
for client-specific settings. Relative file paths resolve from the
process's working directory. Explicit false, empty and zero values override the
file. Use `=false` for boolean flags, such as `-autocrop=false`. YAML must be one
scalar-valued mapping; unknown/duplicate keys, nulls, aliases and extra documents
are rejected.

Use a connectable `api` address for MCP. If KVM binds a wildcard address or runs
on another host, override it for MCP, for example
`autopaws-mcp -config ./autopaws.yaml -api kvm-host:9329`.

| Setting | Default | Meaning |
|---|---|---|
| `api` | `127.0.0.1:9329` | JSON Lines API `host:port`; empty disables it |
| `web` | empty | Viewer `host:port`; empty disables it |
| `token` | empty | Inline shared secret for API, web and MCP; alternative to `token-file` |
| `token-file` | empty | Read the shared secret from a UTF-8 file instead |
| `serial-device` | empty | Serial path or Windows `COMn`; empty auto-detects CH340/CH341 |
| `serial-baud` | `9600` | Supported baud rate below |
| `screen-width` | `1920` | Positive target coordinate width |
| `screen-height` | `1080` | Positive target coordinate height |
| `target-layout` | `us104` | Target text layout |
| `layouts-dir` | empty | Custom layouts, searched before built-ins |
| `mouse-button-mode` | `relative` | `relative` or `absolute` button reports |
| `mouse-absolute-refresh` | `false` | Opt in to refreshing absolute positions before input; see [mouse compatibility](target-setup.md#mouse-compatibility) |
| `mouse-mode` | `legacy` | Identity mapping, or `extended` to require display geometry before positioned input |
| `capture-device` | empty | Linux video path, macOS `avfoundation:NAME` or Windows `dshow:NAME`; empty auto-selects |
| `capture-width` | `1920` | Requested width, 1–4294967295 |
| `capture-height` | `1080` | Requested height, 1–4294967295 |
| `autocrop` | `false` | Opt in to cropping verified bars matching the configured target output aspect |
| `web-fps` | `20` | Viewer frame rate, 1–120 |
| `audio-device` | empty | Linux PCM path, macOS `avfoundation:NAME` or Windows `dshow:NAME`; `off` disables audio |
| `log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `debug-keys` | `false` | Log input/packet records regardless of log level |

Supported baud rates: 1200, 1800, 2400, 4800, 9600, 19200, 38400, 57600 and 115200.
Screen dimensions describe target coordinates; capture dimensions describe the
video stream. A one-pixel target axis maps to HID zero. See
[keyboard layouts](../CREATING_LAYOUTS.md) for built-in and custom text maps.

Listen addresses accept hostnames, IPv4 and bracketed IPv6. An empty host binds
all interfaces; ports are 0–65535, with zero selecting an ephemeral port.
Logs go to stderr. SIGINT/SIGTERM stop the daemon and attempt input release;
use Ctrl+C in a Windows console. Kernel device calls can outlast application cleanup deadlines.

## Troubleshooting device selection

Use device discovery when automatic selection fails, chooses the wrong device,
or you need capture formats or an explicit audio selector:

```sh
autopaws-kvm --list-devices
```

`--list-devices` marks automatic defaults with `*`. It probes video formats on
Linux without starting a stream or opening serial/audio devices. Multiple serial
adapters require `-serial-device`. Prefer stable `/dev/serial/by-id` or
`/dev/serial/by-path` paths over `ttyUSBn`.
