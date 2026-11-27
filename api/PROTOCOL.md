# JSON Lines TCP API

The API uses one fresh TCP connection for each call. Send one JSON object followed
by LF, keep the write side open, read one JSON response followed by LF, then close.
The server closes after that response. Persistent connections and pipelining are
unsupported. CRLF is accepted as JSON trailing whitespace plus LF.

Request lines may contain at most 1 MiB (1048576 bytes), **including LF**. An
oversized line gets `request too large` and the connection closes. An unterminated
fragment is not executed. The Go client accepts response lines up to 64 MiB
(67108864 bytes), including LF, to accommodate base64 JPEG replies.

```json
{"id":1,"method":"send_key","params":{"key":"enter"}}
```

```json
{"id":1,"ok":true,"result":{"sent":true}}
```

Failures use a stable `error` code and optional human-readable `detail`:

```json
{"id":1,"ok":false,"error":"invalid params","detail":"x and y must be supplied together"}
```

`id` may be any JSON value. Its value and numeric precision/spelling are retained;
insignificant JSON whitespace may be compacted. Missing IDs and invalid JSON get
`id:null`. A request must be an object with a nonempty string `method`. Missing
`params` means `{}`; an explicit null or non-object parameter value is rejected.
Unknown envelope/parameter fields are rejected. JSON duplicate keys follow Go's
standard decoder behavior: the last matching value is used.

Required parameters reject both missing and null values. Optional nulls mean
omitted. Integers must be JSON integers: `1.0`, strings and overflow are rejected.
All input fields, including later text characters or sequence steps, are validated
before hardware preparation or input emission.

## Optional shared token

With a nonempty `token-file` or `token` in the shared YAML file (or via the
daemon's `-token-file PATH` flag), each request must include the matching
secret as an envelope string field, for example `"token":"your-secret"`. It is
checked before method dispatch or HID preparation. Missing, null, empty, or wrong tokens
receive `{"id":1,"ok":false,"error":"unauthorized"}` (with the request's ID).
The token is never logged. Without a configured token, the token field is ignored.

The configuration and optional token file are read once at daemon startup.
Token values must be nonempty UTF-8; surrounding whitespace is trimmed.
Restart to load a changed secret. Nonempty `token` and `token-file` together
are rejected. The portable Go client uses `apiclient.NewWithToken(address, secret)`;
`New(address)` omits it. MCP reads the same secret from the shared YAML file
or its own `-token-file PATH` flag.
Use loopback addresses and the SSH tunnel in the README: the token does not encrypt
plain TCP traffic.

## Connections and cancellation

Connections have ten seconds to send and authenticate their request, including
any error response before authentication. At most 64 connections may await
authentication; a new connection closes the oldest pending one. These limits
apply before the request's token check, even when no token is configured. Once
the request passes that check, it runs without that deadline or pending limit.
Temporary listener accept errors are retried with backoff from 5 ms to one second.

A read EOF cancels the request, including `shutdown(SHUT_WR)`, `CloseWrite` and
`nc -N`. A half-close is **not** a request terminator; keep both directions open
while waiting for the reply. A fully disconnected client may not receive the
error reply. Extra input after the one request is rejected when already buffered;
extra input observed during execution cancels the action. An already completed
action may have produced its result before later input is observed. No second
request executes.

Every complete text, key, sequence, click or drag action uses one shared HID gate
across API and browser callers, including holds, delays and cleanup. Use one
operator at a time; separate connections do not merge independent held state.

Cancellation after input may have reached the target attempts release before the
next action. Cleanup has a one-second application budget; executing kernel device
calls can outlast it. Recovery releases all input and clears held state; interrupted
input is never replayed. Cleanup failures appear in the error detail. Shutdown
attempts keyboard and mouse release before closing serial transport.

## Input methods

In this table `?` means optional. Button names are `left`, `right` and `middle`;
missing or empty means left. `button_mode` is `absolute` or `relative`; omitted,
null or empty uses the configured default.

| Method | Parameters | Successful result |
|---|---|---|
| `ping` | none | `{"pong":true}` |
| `type_text` | `text` string; `char_delay_ms?` integer; `raw?` boolean | `{"chars_typed":N}` |
| `send_key` | `key` string; `modifiers?` array of strings | `{"sent":true}` |
| `send_key_sequence` | `steps` array of `{key, modifiers?, delay_ms?}`; `default_delay_ms?` | `{"steps_sent":N}` |
| `mouse_move` | integer `x`, `y`; `relative?` boolean | `{"position":{"x":X,"y":Y,"relative":false}}` (echoes inputs) |
| `mouse_click` | `button?`, integer `x?`, `y?`, `button_mode?` | `{"clicked":true}` |
| `mouse_down` | same button parameters | `{"pressed":true,"button":"left"}` (effective button) |
| `mouse_up` | same button parameters | `{"released":true,"button":"left"}` (effective button) |
| `mouse_scroll` | integer `amount` | `{"scrolled":true}` |
| `mouse_drag` | integer `start_x`, `start_y`, `end_x`, `end_y`; `button?`, `button_mode?` | `{"dragged":true}` |

Text supports recognized tags such as `{enter}`, `{ctrl+c}` and `{shift+0x87}`.
Unknown tags remain literal; `{{` and `}}` escape braces. Raw mode types braces and
backslashes literally and converts actual CRLF, CR or LF into Enter. Layouts and
supported characters are described in [Creating keyboard layouts](../CREATING_LAYOUTS.md).
`chars_typed` counts input Unicode runes, including tag syntax. Empty text is valid.

Every key stroke is held for 10 ms, then released. Character delay defaults to
20 ms and sequence delay to 100 ms, with per-step overrides. Delays include the
last character/tag/step and must be from 0 through 60000 ms. Explicit zero disables
that delay. Multi-stroke layout characters have no character delay between their
strokes. Sequences may contain at most 4096 steps; an empty array is valid.

Absolute coordinates are **configured target-screen pixels**:
`0 <= x < screen_width`, `0 <= y < screen_height`. Both position fields must be
supplied together, including when both are zero. Their first and last pixels map
exactly onto HID 0 and 4095 in legacy mode; a one-pixel axis maps to zero. Extended
mode applies the configured shared mapping after normalization. Screenshot pixels may
have different dimensions: MCP performs that conversion using its latest screenshot.

Relative `mouse_move` uses unscaled signed deltas from −127 through 127 per axis;
larger deltas are rejected. Scroll is clamped to −127…127 and preserves held buttons.
Movement preserves held buttons in both button modes. Clicks preserve other held
buttons and finish with the selected button released. Each held button stays on
the absolute or relative report that pressed it. Movement and scrolling preserve
that ownership; changing `button_mode` does not move an already held button to
another report. Positioned relative presses send the absolute position before
the relative button report.

Releasing an absolute-report button without coordinates, including during
cleanup, uses its last absolute position. That position can be stale after raw
relative or physical mouse movement; supply release coordinates to avoid a jump.
The default relative button mode supports current-position release.

`mouse_drag` is one server-side HID action: down at start, 50 ms, move to end,
50 ms, up at end. Both endpoints, button and mode are checked before input. Clients
must call this method rather than composing down/move/up requests when they need
one complete drag action.

Positioned requests also accept optional integer `mapping_revision`, copied
automatically from `capture_frame` by MCP. Stale revisions fail before input.
Relative moves and position-free button actions do not use this freshness token.

## Shared mouse mapping

| Method | Parameters | Successful result |
|---|---|---|
| `get_mouse_mapping` | none | Current mapping state |
| `set_mouse_mapping` | `mode?`, `geometry?` | Updated mapping state |
| `reset_mouse_mapping` | none | Updated mapping state |

The daemon owns one mapping shared by API, MCP and web connections. Regular
positioned coordinates remain local to the captured display; callers do not add
desktop offsets to individual mouse actions. State includes `mode` (`legacy` or
`extended`), `revision`, `ready`, capture binding, optional `geometry` and an
effective transform. An unset display offset differs from a configured zero.

Legacy retains the original identity mapping. Setting complete geometry enters
extended mode and activates the mapping immediately after structural bounds
validation. The caller supplies the target geometry; readiness does not establish
that absolute HID input can reach that display. Extended mode without configured
geometry blocks positioned actions. Reset clears geometry while retaining the
current mode, so extended becomes not ready. Explicitly set `mode: "legacy"`
to restore identity. Runtime state survives reconnects; restart clears geometry
and follows the startup mode.

Complete geometry uses this shape:

```json
{"geometry":{"offset":{"x":1920,"y":0},"width":1920,"height":1080,"span":{"x":0,"y":0,"width":3840,"height":1200}}}
```

`offset` is the captured display's signed desktop origin on either axis. `span`
is the range actually addressed by absolute HID, which can differ from the
virtual desktop on Windows or macOS. Dimensions and offsets must use the same
desktop units; do not mix logical and physical coordinates.
Widths/heights must each be at least two and the display must fit in the span.
Configured screen dimensions remain the API's local coordinate grid,
independently of these desktop bounds.

An offset cannot make an unreachable display accessible to absolute HID. Use a
captured primary where supported, or single/mirrored output, in that case. Raw
relative movements and current-position clicks remain available for screenshot
feedback. No target software or automatic cursor tracker is required.

Mapping changes release held input. Complete finite actions prepare all points
against one revision before input, including both drag endpoints. Capture
identity, generation, dimensions and crop changes invalidate extended readiness;
reapply the geometry after checking it. Ordinary frames and viewer resizing
preserve readiness. A target layout/DPI change may leave HDMI dimensions
unchanged, so inspect mismatches and update/reset the mapping explicitly.

## Keyboard layout selection

| Method | Parameters | Successful result |
|---|---|---|
| `list_keyboard_layouts` | none | Active `keyboard_layout` and sorted `layouts` catalog, described below |
| `set_keyboard_layout` | nonempty string `layout` name | `{"keyboard_layout":"se105"}` (selected name) |

The read-only listing reports each available name, its effective source and any
load error:

```json
{
  "keyboard_layout":"se105",
  "layouts":[
    {"name":"de105","source":"builtin"},
    {"name":"fr105","source":"builtin"},
    {"name":"jp106","source":"builtin"},
    {"name":"my-layout","source":"custom","error":"/path/to/my-layout.yaml: invalid keyboard layout: expected only an overrides mapping"},
    {"name":"se105","source":"builtin"},
    {"name":"uk105","source":"builtin"},
    {"name":"us104","source":"builtin"}
  ]
}
```

The `layouts` array is sorted by name. Each custom `*.yaml` filename replaces the
built-in entry with the same name, including when invalid. An optional `error`
means the map cannot be loaded. An empty or missing configured layout directory
yields built-ins only; other directory-read failures return `unavailable`.

Select one of `us104`, `uk105`, `de105`, `fr105`, `jp106`, `se105`, or a custom
filename stem from the daemon's configured `layouts-dir`. Request-supplied
directory paths are invalid. Selecting the same name reloads its file.

The replacement is loaded and validated before activation. Missing, empty,
unknown or invalid names and malformed layout files return `invalid params`;
filesystem read failures return `unavailable`. Failures and cancellation before
activation leave the previous map and active name unchanged.

The daemon uses the selection for subsequent text, tags, single-character keys
and character-based sequences. Each keyboard request resolves its entire plan
against one layout; a concurrent switch does not alter that plan.
Use sequential control requests from one active MCP connection. Restarting restores
the CLI/YAML startup choice without rewriting configuration. Select the matching
input source on the target OS separately.

Listing and selection work without hardware and emit no input. They preserve
capture/screenshot state, physical browser keycodes, named special keys,
hexadecimal HID usages and mouse behavior.

## Device information and capture

`get_device_info` takes no parameters and succeeds without opening serial hardware
or finding a capture card. Serial/config fields describe **configured settings**,
not a claim that the device is connected. `config.keyboard_layout` is the active
runtime selection, initially the CLI/YAML startup layout:

```json
{
  "serial":{"device":"","baud":9600,"auto_detect":true},
  "config":{
    "screen_width":1920,"screen_height":1080,"keyboard_layout":"us104",
    "serial_baud":9600,"mouse_button_mode":"relative"
  },
  "capture":{
    "device":"","requested_width":1920,"requested_height":1080,
    "autocrop":false,"running":false,"generation":1,
    "available_devices":[],"error":"unavailable"
  }
}
```

Capture methods share the daemon's frame pipeline:

| Method | Parameters | Successful result |
|---|---|---|
| `capture_frame` | none | `{"jpeg_b64":"...","width":W,"height":H,"target_width":TW,"target_height":TH}` |
| `list_capture_devices` | none | `{"devices":[...]}`, empty when none exist |
| `set_capture_device` | nonempty string `device` path, not a signed/unsigned numeric index | Capture-status object after selecting a device |
| `set_capture_resolution` | integer `width`, `height`, each 1…4294967295 | Capture-status object after updating the requested size |

On Linux, a snapshot requires a successfully decoded frame whose `CLOCK_MONOTONIC` capture
timestamp is after its request arrived. Its five-second acquisition deadline
includes startup and corrupt-frame warmup; an earlier caller cancellation wins.
No valid fresh frame yields `timeout`; device loss, no selected device or an
unsupported timestamp clock yields `unavailable`. There is no stale-frame
fallback. Capture timing does not establish that the target application has
processed earlier input or that HDMI content has stabilized. Executing kernel
operations can outlast the acquisition deadline.

On macOS, device selectors are `avfoundation:<exact name>` from discovery.
Duplicate device names are ambiguous; no numeric indices are accepted.
On Windows, use the `dshow:<alternative name>` selector returned by discovery.
An exact friendly name is accepted only when it identifies one input of the
requested media type. Windows audio requires an explicit audio-device selector.

Freshness comparisons use the server's host monotonic clock: `CLOCK_MONOTONIC`
on Linux/macOS, or Windows `QueryPerformanceCounter` converted to nanoseconds.
FFmpeg video uses estimated pipe read-start timestamps on that clock. The
snapshot still waits for a read started after the request, but
FFmpeg/OS buffering means this does not establish camera capture freshness.

Snapshots are validated JPEGs. Cropping is disabled by default. Opt-in autocrop
uses the configured target aspect ratio to validate a centered, aspect-preserving target
area; matching capture/target aspects are never cropped. Uncropped images
are not re-encoded; cropped images use quality-85 encoding. Width/height describe the returned image, which can differ
from both requested capture size and configured target coordinates. There is no
quality parameter. Capture starts on demand and stops after ten idle seconds.

`target_width` and `target_height` are the configured target coordinate dimensions,
independent of the captured image size and crop. Reading them with a snapshot
does not enumerate devices. Older daemons omit both fields; clients may read
`get_device_info.config.screen_width` and `screen_height` as a compatibility
fallback. A partially present pair or nonpositive/noninteger dimensions is an
invalid snapshot response, rather than a signal to fall back.

`mapping_revision` binds positioned input to the shared mapping for this capture.
`frame_sequence` is a manager-wide frame delivery token retained across idle
source reopen. It is not a target acknowledgement or a
guarantee against capture-card buffering. MCP threads the revision automatically
without exposing it as a mouse-tool option.

Every device or resolution change accepted by a setter increments the settings
generation, flushes queued old frames and resets crop detection. Pending
old-generation snapshots fail with `capture changed`; callers retry explicitly.
Idle opens retain that generation. Native sources on Linux, macOS and Windows
are closed and retried while demand remains after a failure or five seconds
without a successfully processed frame. Retries use a one-second backoff;
a successful automatic recovery advances the generation and resets queued frames
and crop detection. Failed attempts do not
advance it. A device selection is accepted even with no source open, and a
failed open preserves that selection.

Resolution changes store the requested size while idle and reopen an active
source. Failed opens preserve the selection and settings for repair. Without a
selected device, resolution changes return `unavailable`. Missing capture hardware
and discovery failures leave HID input and device selection usable.

Each listed device has this shape; format order is the driver's order:

```json
{
  "device":"/dev/video0","name":"UGREEN",
  "formats":[{"fourcc":1196444237,"description":"Motion-JPEG","flags":1}],
  "usb":{"vid":"2b89","pid":"5389","bus_path":"/sys/devices/example/usb4/4-1"}
}
```

FourCC and format flags are numeric unsigned 32-bit values, retaining big-endian
format bits. Format lists are arrays, including when empty. Non-USB devices use
empty USB strings. Discovery uses numeric node order and node-specific capture
capabilities, omitting metadata nodes. Listing performs read-only queries without
starting a stream. Probe/permission failures are errors rather than an empty list.

With a selected device, capture status has this shape. Successful setters return
this object directly; `get_device_info` places it under `capture`:

```json
{
  "device":"/dev/video0","requested_width":1920,"requested_height":1080,
  "autocrop":true,"running":true,"generation":3,
  "format":{"width":1920,"height":1080,"fourcc":1196444237,"bytes_per_line":0,"size_image":4147200},
  "available_devices":[]
}
```

`format` is omitted until a negotiated format is known. It describes the actual
source format independently of requested settings and cropped snapshot size.
`available_devices` is a separate discovery refresh and remains an array. A
closed/missing/failed source or incomplete discovery adds stable `error` and
optional diagnostic `detail`, while
retaining selected/requested settings. A selected idle device is available while
`running` is false. `get_device_info` remains an overall success when capture is
unavailable. A standalone API server without a capture manager retains the
minimal `{"error":"unavailable"}` capture object.

## Stable errors and logs

| Code | Meaning |
|---|---|
| `unauthorized` | Configured token missing or incorrect; action not dispatched |
| `invalid request` | Malformed envelope/framing or extra input |
| `request too large` | Request exceeds the line limit |
| `unknown method` | No registered method with that name |
| `invalid params` | Missing, wrong-type or out-of-range input; invalid layout name or malformed selected layout |
| `unsupported character` | A text/key character is absent from the target layout |
| `canceled` | Request context was cancelled |
| `timeout` | Context deadline or snapshot acquisition deadline |
| `unavailable` | Service/device unavailable, disconnected or closing; selected layout cannot be read |
| `device busy` | Another cooperating process owns the serial port |
| `capture changed` | Pending snapshot cancelled by a capture selection/resolution change |
| `mouse mapping changed` | Positioned input refers to an obsolete mapping revision |
| `mouse mapping unavailable` | Extended positioned input has no configured absolute mapping |

The optional `detail` contains diagnostics, including the unsupported rune's
position and cleanup errors. Match the stable code, not the detail text.

Each request logs its method and outcome at ordinary debug level. Typed text is
logged only with `-debug-keys`; that option also enables packet logs independently
of ordinary log level. These records can expose passwords and other typed content.

## Go client

`apiclient.New(address).Call(ctx, method, params, result)` is portable and safe for
concurrent calls. Each call owns a new connection, echoes/checks ID `1`, never
half-closes, and closes the connection when its context is cancelled. Callers own
deadlines; there is no arbitrary default RPC timeout. Nil parameters send `{}`;
a nil result discards a validated success result.

Remote failures are `*apiclient.RemoteError` values that unwrap to an
`apiwire.Code`, for example `errors.Is(err, apiwire.ErrUnavailable)`. Local cancelled
calls preserve `context.Canceled` or `context.DeadlineExceeded`. Malformed,
truncated, oversized or mismatched-ID replies match `apiwire.ErrProtocol`.
