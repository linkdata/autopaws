# MCP server

Each MCP client starts its own local stdio process. With the default KVM API
address, no arguments are needed:

```sh
autopaws-mcp
```

## Client configuration

For Claude Code:

```sh
claude mcp add --transport stdio --scope user autopaws -- \
  /absolute/path/to/autopaws-mcp
```

For Claude Desktop, add this to its MCP configuration:

```json
{
  "mcpServers": {
    "autopaws": {
      "command": "/absolute/path/to/autopaws-mcp"
    }
  }
}
```

On Windows, use absolute paths such as `C:\\Tools\\autopaws\\autopaws-mcp.exe` in
JSON. Add `-config /absolute/path/to/autopaws.yaml` to the client's arguments to
use the [shared configuration](configuration.md#configuration-file). Set
`token-file` or `token` there when authentication is enabled.
Restart the client after configuration changes. The API address is a JSON Lines
TCP service; see [local MCP client setup](https://modelcontextprotocol.io/docs/develop/connect-local-servers).

## Options and tools

MCP flags below use their names without the leading `-` in the
[shared configuration file](configuration.md#configuration-file); `config` is command-line only.
With no `-config`, MCP uses defaults and explicit flags only.

| Flag | Default | Meaning |
|---|---|---|
| `-config PATH` | off | Read the shared KVM/MCP YAML file |
| `-api host:port` | `127.0.0.1:9329` | KVM JSON Lines address, port 1–65535 |
| `-capture-log DIR` | off | Save screenshot JPEGs; no retention limit |
| `-token-file PATH` | off | Read the API secret from a UTF-8 file |
| `-tesseract PATH` | automatic | Optional OCR engine; an invalid explicit path disables OCR |
| `-vision-api URL` | off | OpenAI-compatible base URL or full `chat/completions` endpoint for `wait_for` |
| `-vision-model NAME` | off | Model used to evaluate visual conditions; configure with `-vision-api` |
| `-vision-token-file PATH` | off | UTF-8 bearer-token file for the vision endpoint; separate from the KVM token |
| `-vision-max-width N` | `960` | Maximum width sent to vision, 64–4096; returned screenshots keep their original size |
| `-vision-request-json JSON` | `{"reasoning_effort":"none"}` | Provider options; may override sampling/output settings, but not `model`, `messages` or `stream` |

`vision-max-width` validation and default request options apply only when
vision is enabled.

| Tools | Purpose |
|---|---|
| `setup` | Pre-work pointer/layout/window checklist |
| `type_text`, `send_key`, `send_key_sequence` | Keyboard actions |
| `list_keyboard_layouts`, `set_keyboard_layout` | Inspect or select target text maps |
| `mouse_move`, `mouse_click`, `mouse_drag`, `mouse_scroll` | Pointer actions |
| `get_mouse_mapping`, `set_mouse_mapping`, `reset_mouse_mapping` | Shared display geometry; configure before work when needed |
| `capture_screen` | Fresh JPEG and image dimensions |
| `get_screen_text`, `execute_and_read` | Local Tesseract OCR; optionally type a command first |
| `get_device_info`, `list_capture_devices`, `set_capture_device`, `set_capture_resolution` | Device information and capture settings |

## Coordinates and keyboard layouts

`setup` returns a checklist for target input and display alignment. Call it
before controlling the target and after target, display or layout changes.
For an extended desktop, inspect `get_mouse_mapping` and configure the
[display mapping](target-setup.md#display-mapping) before work when needed.
Before absolute movement, positioned clicks or drags, call `capture_screen`.
Coordinates are integer pixels in that session's latest screenshot, including
cropping. Relative deltas are unscaled; clicks at the current pointer need no
screenshot. Mapping changes and capture-setting attempts invalidate screenshot coordinates; layout
changes preserve them. Cancelling a tool closes its API call and allows input
release cleanup. Use one operator; sessions do not merge held input.

Select a layout with `set_keyboard_layout({"layout":"se105"})` after setting the
target OS layout. Custom YAML names override built-ins; selecting the same name
reloads its file. Invalid selections keep the working map. Restarting restores
the startup layout. See [layout selection](../CREATING_LAYOUTS.md#changing-the-active-layout).

## OCR

OCR requires Tesseract, discovered at startup through
`-tesseract`, `PATH`, or Windows `ProgramFiles`/`ProgramFiles(x86)` installations.
Restart MCP after installing it. OCR calls time out after 15 seconds and do not
set screenshot coordinates. `execute_and_read` types a raw command plus Enter,
waits `wait_seconds` (default 1, range 0–60), then captures OCR output; the wait
does not establish command completion. Capture logs contain complete screenshots.

Both OCR tools accept `language`, defaulting to English (`eng`) when omitted or
empty. Use combinations such as `eng+swe` for English and Swedish. The matching
Tesseract traineddata must be installed on the MCP host; missing models produce
an OCR error and are not installed automatically. This setting is independent of
the HID keyboard layout and target OS input source.

Both OCR tools accept an optional `region: {x, y, width, height}` in original
captured screenshot pixels with a top-left origin. Choose the rectangle from
`capture_screen` and add padding around the text. For example, call
`get_screen_text` with:

```json
{"language": "eng+swe", "region": {"x": 100, "y": 180, "width": 300, "height": 80}}
```

Each call captures a fresh full frame, then crops before the existing 2× OCR
preprocessing. Omit `region` for full-frame OCR. Positions must be nonnegative
and sizes positive. Invalid geometry, malformed language names and empty `+`
components are rejected before `execute_and_read` types. The rectangle must fit
the fresh capture; bounds and missing-model errors can occur after command input.
Results remain plain text and do not set mouse coordinates.

See the [OCR region comparison](validation/ocr-region-comparison-2026-11-24.json)
for measured latency and text accuracy.

## Input with a visual condition

Configure an OpenAI-compatible vision service to enable `wait_for`. For example,
with a local service on port 11434 (use its actual URL and installed model name):

```sh
autopaws-mcp -api 127.0.0.1:9329 \
  -vision-api http://127.0.0.1:11434/v1 \
  -vision-model qwen3-vl-2b-instruct
```

For an authenticated service, add `-vision-token-file /path/to/vision.token`.
The client sends `Authorization: Bearer TOKEN`. When vision is enabled, omitted
`vision-request-json` sends `{"reasoning_effort":"none"}`. An explicit JSON object
replaces that default: use `'{}'` to omit `reasoning_effort` and let the provider
choose. Include `"reasoning_effort":"none"` when adding sampling or output options
to keep reasoning disabled. A service without JSON-schema support can override
`response_format` while MCP still validates the returned boolean object.
The service must return `{"satisfied": boolean}` in normal message content;
reasoning-only output is rejected.

With vision disabled, `wait_for` and `wait_timeout_ms` are absent from tool
schemas and rejected if supplied. When vision is configured, the seven
keyboard/mouse tools advertise these optional arguments:

```json
{
  "key": "enter",
  "wait_for": "A separate floating confirmation dialog with Continue and Cancel is visible over a dimmed page",
  "wait_timeout_ms": 5000
}
```

MCP validates the wait configuration before sending input. After successful
input it captures and checks candidate frames, returning JSON metadata and the
original JPEG whose resized copy the model evaluated. That image establishes
the session's mouse coordinates. The metadata includes the original input
result, observation status, model result/timing, a unique observation ID,
original-image SHA-256 and image age since the capture returned to MCP. It never
attaches a model decision to a newer unchecked screenshot.

The observation budget includes capture and inference, starts after successful
input, defaults to 5000 ms and accepts 1–60000 ms. `wait_timeout_ms` requires a
nonblank `wait_for` prompt of at most 2048 bytes. The first fresh frame is always
checked; later candidates use a small pixel-change gate and recheck unchanged
content once 500 ms have elapsed since the previous completed inference. Capture and
inference stay sequential, with no queued screenshots.

`condition_met` reports the model's decision, not guaranteed task completion.
On `timeout`, `capture_error`, `analysis_error` or `invalid_output`,
`input.sent: true` preserves the successful input outcome: do not replay it
automatically. The last successfully checked image and its boolean result are
returned when available, even if a later check times out. No checked image
means `predicate.satisfied` is null and no image is returned. Capture-setting
changes invalidate the pending image and report `capture_changed`. Cancellation
stops the request without committing new screenshot coordinates.

Input and observation calls serialize within one MCP session. Other sessions,
the browser and direct API calls still require one operator. Screenshots and
conditions go to the configured vision service; `-capture-log` also logs wait
candidate screenshots. With no `wait_for`, existing action results are unchanged
and no capture or vision request is made.

Checks sample individual frames, so a short-lived state between checks can be
missed. Prefer conditions that remain visible until the next action, and verify
the returned image before continuing. See [performance measurements](cycle-time.md)
for capture and visual-wait benchmarks.
