# JSON Lines TCP API

The API uses one fresh TCP connection for each call. Send one JSON object followed
by LF, keep the write side open, read one JSON response followed by LF, then close.
The server closes after that response. Persistent connections and pipelining are
unsupported. CRLF is accepted as JSON trailing whitespace plus LF.

Request lines may contain at most 1 MiB (1048576 bytes), **including LF**. An
oversized line gets `request too large` and the connection closes. An unterminated
fragment is not executed. The Go client accepts response lines up to 64 MiB
(67108864 bytes), including LF, to accommodate future base64 JPEG replies.

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

## Connections and cancellation

A read EOF cancels the request, including `shutdown(SHUT_WR)`, `CloseWrite` and
`nc -N`. A half-close is **not** a request terminator; keep both directions open
while waiting for the reply. A fully disconnected client may not receive the
error reply. Extra input after the one request is rejected when already buffered;
extra input observed during execution cancels the action. An already completed
action may have produced its result before later input is observed. No second
request executes.

Every complete text, key, sequence, click or drag action uses one shared HID gate
across API and browser callers, including its holds, delays and cleanup. V1 supports
one operator at a time; separate connections do not merge independent held state.

Cancellation after input may have reached the target attempts release before the
next action enters. Cleanup uses an uncancelled one-second application context and
explicit transport preparation. A recovered generation releases all input and
clears held state; interrupted input is never replayed. Failure is reported in the
error detail. Linux serial kernel calls already executing can outlast that budget.

Shutdown closes listeners/connections and starts shared controller cleanup before
waiting for handlers. Keyboard and mouse are released before permanent transport
close. API-only and later web-only/combined service startup share this ownership;
there is no universal kernel shutdown deadline.

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
exactly onto HID 0 and 4095; a one-pixel axis maps to zero. Screenshot pixels may
have different dimensions: MCP performs that conversion using its latest screenshot.

Relative `mouse_move` uses unscaled signed deltas from −127 through 127 per axis;
larger deltas are rejected. Scroll is clamped to −127…127 and preserves held buttons.
Movement preserves held buttons in both button modes. Clicks preserve other held
buttons and finish with the selected button released. Relative button mode sends
positioned button changes as a button-free absolute move plus a relative report.

`mouse_drag` is one server-side HID action: down at start, 50 ms, move to end,
50 ms, up at end. Both endpoints, button and mode are checked before input. Clients
must call this method rather than composing down/move/up requests when they need
one complete drag action.

## Device information and capture

`get_device_info` takes no parameters and succeeds without opening serial hardware
or finding a capture card. Serial/config fields describe **configured settings**,
not a claim that the device is connected:

```json
{
  "serial":{"device":"","baud":9600,"auto_detect":true},
  "config":{
    "screen_width":1920,"screen_height":1080,"keyboard_layout":"us104",
    "serial_baud":9600,"mouse_button_mode":"absolute"
  },
  "capture":{"error":"unavailable"}
}
```

Capture methods are registered now and return `unavailable` after validating their
parameter shapes. The frame pipeline will supply their operational behavior:

| Method | Parameters | Current result / later contract |
|---|---|---|
| `capture_frame` | none | Currently unavailable; later `{"jpeg_b64":"...","width":W,"height":H}` |
| `list_capture_devices` | none | Currently unavailable; later a `devices` array, empty when none exist |
| `set_capture_device` | nonempty string `device` path, not a signed/unsigned numeric index | Currently unavailable; later selects a device even when none is open |
| `set_capture_resolution` | integer `width`, `height`, each 1…4294967295 | Currently unavailable; later updates the selected device's requested size |

A future snapshot must be captured after its request arrived. Its five-second
server deadline includes startup and corrupt-frame warm-up; an earlier caller
cancellation wins. No valid fresh frame yields `timeout`; device loss or no selected
device yields `unavailable`. Changing the selected device or resolution cancels
pending old-generation snapshots with `capture changed`, without silently moving
them to the new generation.

An idle capture device is not unavailable merely because streaming is stopped.
Resolution changes remain available while idle or after a previous open failed.
Store the requested size for the next start or reopen for active subscribers; a
failed reopen is reported while selection/settings remain repairable. No selected
device means resolution changes return `unavailable`. These operational checks
arrive with the frame pipeline; the current stubs do not simulate device state.

## Stable errors and logs

| Code | Meaning |
|---|---|
| `invalid request` | Malformed envelope/framing or extra input |
| `request too large` | Request exceeds the line limit |
| `unknown method` | No registered method with that name |
| `invalid params` | Missing, wrong-type or out-of-range input |
| `unsupported character` | A text/key character is absent from the target layout |
| `canceled` | Request context was cancelled |
| `timeout` | Context deadline; later snapshot freshness deadline |
| `unavailable` | Service/device unavailable, disconnected or closing |
| `device busy` | Another cooperating process owns the serial port |
| `capture changed` | Reserved for a snapshot cancelled by capture selection/resolution change |

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
