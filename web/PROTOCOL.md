# Browser WebSocket protocol, version 1

The viewer uses HTTP for embedded assets and one WebSocket at `/ws`. Assets have
`Cache-Control: no-cache`. Unknown page routes return the index page. WebSocket
upgrades permit the request's own host and reject cross-origin browser requests.
Without a configured token, the Host must be `localhost` or an IP literal.
Custom hostnames require a token. Responses include `Content-Security-Policy:
frame-ancestors 'none'` and `X-Frame-Options: DENY`. WebSocket compression is disabled.

Input actions are serialized with API actions. Use one operator at a time;
connections do not merge independent held states.

## Optional shared token

When a nonempty `token-file` or `token` is configured in YAML (or via the
daemon's `-token-file PATH` flag), `/ws` requires a `Sec-WebSocket-Protocol` offer of
`autopaws-token.HEX`, where HEX is the hex encoding of the secret's UTF-8 bytes.
Missing or wrong values receive HTTP 403 before upgrade or subscription.
The browser also offers `autopaws`; the server selects that fixed protocol and
never echoes the credential. Cookies do not authorize WebSockets; a received
`autopaws_token` cookie is expired.

All bootstrap assets remain public. Open
`http://127.0.0.1:9330/#token=ENCODED_SECRET`, where ENCODED_SECRET is the result
of JavaScript `encodeURIComponent(secret)`. The page removes the recognized
fragment with `history.replaceState` before opening its first WebSocket and
stores the secret in `sessionStorage` under `autopaws_token`. Storage is scoped
to the page's origin, including its port, and lasts for that tab's session.
Reconnects and reloads reuse the stored token; an empty `#token=` clears it.
When session storage is unavailable, the token remains usable for that page's
lifetime. Tokens never appear in WebSocket URLs or application logs.
The fragment is processed locally and is not sent in HTTP requests.

Without a configured token, no token is required. Use the literal loopback
address through the documented SSH tunnel for remote access. Authentication
does not add TLS or a supported reverse-proxy deployment.

## Configuration and media

The first server application message is JSON text with `type: "config"`, `audio`
and `mapping` objects. The audio portion has this shape:

```json
{"type":"config","audio":{"status":"unavailable","generation":"0"}}
```

Audio status is `off`, `unavailable`, or `available`. An available configuration
includes the negotiated `sampleRate` and `channels`; for example,
`{"type":"config","audio":{"status":"available","generation":"7","sampleRate":48000,"channels":2}}`.
Audio is captured lazily while web clients are connected. On Linux, auto
selection follows the current selected video USB device; an explicit PCM path
stays fixed. macOS and Windows require explicit AVFoundation/DirectShow audio selectors. Each
video generation reopens audio, including resolution changes and successful
automatic capture recovery. Selection/open/read failures make audio unavailable
without ending video or input. FFmpeg audio retries an explicit selector
after one second while viewers remain connected. Every audio-generation change
is sent as configuration before any samples belonging to that generation.
JSON uint64 generations are decimal strings
to preserve precision in JavaScript. Binary uint64 values are exact: clients use
`DataView.getBigUint64(..., true)`, not Number coercion.

Each binary message contains a 64-byte header followed by its payload.
All multibyte integers are **unsigned little-endian**:

| Offset | Type | Meaning |
|---:|---|---|
|0|uint8|Kind: `1` video; `2` audio |
|1|uint8|Protocol version: `1` |
|2|uint16|Header length: `64` |
|4|uint32|Common timestamp flags, below |
|8|uint64|Video settings/recovery generation, or audio configuration generation |
|16|uint64|Shared capture delivery sequence for video, or audio source-block sequence |
|24|uint64|Capture timestamp in nanoseconds on the server's host monotonic clock, or zero if unavailable |
|32|uint32|Video source JPEG width; zero for audio |
|36|uint32|Video source JPEG height; zero for audio |
|40|uint32|Video crop x; zero for audio |
|44|uint32|Video crop y; zero for audio |
|48|uint32|Video crop width; zero for audio |
|52|uint32|Video crop height; zero for audio |
|56|uint32|Audio sample-frame count; zero for video |
|60|uint32|Audio block duration in nanoseconds; zero for video |

The server's host monotonic clock is `CLOCK_MONOTONIC` on Linux/macOS, or Windows
`QueryPerformanceCounter` converted to nanoseconds. It is not wall-clock time;
values must not be compared across hosts or host restarts.

Timestamp flags have the **same meanings for both media kinds**:

* Bit 0 (`1`): the timestamp is available and belongs to the server's host monotonic clock.
  Without it, offset 24 is zero and must not be used for timing.
* Bit 1 (`2`): video timestamp denotes start of exposure. With bits 1 and 2 clear, a valid
  video timestamp denotes end of frame. Audio leaves this bit clear and its
  timestamp denotes the first sample frame of the block.
* Bit 2 (`4`): the monotonic timestamp is an estimate rather than a device timestamp.
  Linux video leaves this bit clear. Linux audio sets it because its first sample time is
  estimated from an ALSA monotonic timestamp/delay observation and block frame count.
  macOS and Windows video and audio set this bit: their timestamps estimate FFmpeg pipe
  receipt on the server monotonic clock, not device capture time. FFmpeg/OS
  buffering is unmeasured; video uses read-start time and audio estimates its first
  sample from read completion minus the block duration.
* Bits 3–31 are reserved and zero. Estimated timestamps require bit 0 as well.

The browser's performance clock has a different epoch. Do not compare its clock
directly to server monotonic timestamps. Use sequences, relative timestamps and
durations for gaps/backlog. An audio sample frame contains one sample per channel;
the payload is interleaved signed 16-bit little-endian PCM, with exactly
sampleFrames×channels samples. Audio block duration is below 200 ms. Its
source-block sequence identifies dropped blocks; audio configuration generation
distinguishes stream restarts.

Video payload is the normalized source JPEG. Crop metadata gives the
visible target area, validated against the configured target aspect ratio;
a full-frame crop represents no border removal. Source
dimensions describe the JPEG, not the browser canvas or the configured target's
pixel dimensions.

Video sequences are the capture manager's delivery sequence, shared by web and
API snapshots. They increase across capture restarts and generation changes;
each connection can observe gaps from pacing or frames delivered before it joined.
Generation changes discard queued frames; an in-progress send may still arrive.
Clients must discard pending image decodes/rendered metadata when a newer
generation arrives and reject late decode results by generation and sequence.

Video is paced to `web-fps` after each completed write; slow readers skip frames.
Configuration/media writes have a one-second default deadline. A stalled write
closes that connection without blocking other clients. Queued audio takes
priority over waiting video. Configuration precedes new-generation samples;
an in-progress JPEG send is not interrupted.

Audio buffering is bounded to 200 ms: 100 ms on the server, 50 ms in the browser
worklet and 50 ms in one acknowledged PCM transfer. Staleness is measured from
the last sample's capture time, separately from acquisition duration. Obsolete
generations are discarded before sending. A JPEG send taking at least 100 ms
resets the server audio backlog. The browser drops new PCM while its transfer
awaits acknowledgement; ring overflow and oversized blocks discard oldest samples.
Capture/kernel, WebSocket/TCP and output latency are additional. Source-age
tracking uses timestamp differences and local elapsed time.

A generation-only change at the same rate/channels resets the running worklet.
An outstanding transfer still occupies its slot until its matching generation/
sequence acknowledgement. Rate/status changes rebuild the audio graph while
preserving the user's audio choice.

## Browser input

Each opened or reloaded viewer page starts in View only mode. Turn off View only
and focus the image to send input; reconnects preserve the page's current choice.

`config.mapping` publishes the same mapping used by the API and MCP: `mode`,
`ready`, optional `geometry` and `transform`, capture context and a status
`message`. Web uint64 revisions and capture sequences are decimal strings.
Mode `legacy` provides identity behavior. Mode `extended` blocks positioned
input until complete geometry is applied. Geometry is activated directly after
bounds validation; the KVM does not observe the target cursor.

The ordered mapping config also contains `frame_generation` and `after_sequence`.
Discard queued pointer movement and unfinished image decodes on a revision or
barrier change. Display only video in that generation with a sequence greater
than the barrier. Every positioned input from the page carries `revision`,
`frame_generation` and `frame_sequence` from its displayed image. Stale mappings,
unconfigured input and mapped coordinates outside the HID range return a nonfatal
`config.mapping.error` status and release held input; they do not reconnect.
Existing clients may omit these fields only in legacy mode. `release_all` always
remains available regardless of mapping readiness.

Send one JSON object per text WebSocket message, up to 32768 bytes. Required
fields reject null or wrong types. Coordinates and masks must be integer JSON values. Unknown object
fields, unsupported event types, malformed JSON and binary input end the
connection, which schedules any held-input cleanup. Standard JSON duplicate-key
semantics apply. Fields belonging to another recognized event type are ignored.

| Type | Required fields | Behavior |
|---|---|---|
|`keydown`, `keyup`|`code` string|Physical `KeyboardEvent.code`, independent of target text layout. Unknown codes are ignored. |
|`mousemove`|`x`, `y`, `buttons` integers|Move to normalized HID coordinates, preserving actual held buttons. The supplied mask is validated but movement does not change held state. |
|`mousedown`, `mouseup`|`x`, `y`, `buttons` integers|Apply the complete authoritative button mask at that position. |
|`scroll`|`deltaY` number|Signed wheel amount, clamped to −127…127 then truncated toward zero. Held buttons are preserved. |
|`release_all`|none|Release this connection's held keyboard and mouse classes; a passive connection sends nothing. |
|`shortcut`|`keys` nonempty array of physical-code strings|One validated chord: press, 10 ms, restore prior held keyboard state. |
|`mapping_set`|`mapping` object|Set `mode` or apply complete `geometry` in the shared KVM mapping. |
|`mapping_reset`|none|Clear geometry and readiness; extended mode stays gated. |

Geometry uses signed `offset: {x,y}`, native captured-display `width,height`,
`span: {x,y,width,height}` for the desktop extent reached by HID 0…4095. Use the
same desktop coordinate system and units for every value; they need not match
capture resolution. Applying or resetting geometry changes the
mapping and releases held input. Capture context changes
invalidate an extended mapping until geometry is reapplied.

Coordinates range from 0 through 4095 on both axes and pass through the shared
mapping before HID encoding; they are not converted through target-screen pixels. The page maps visible crop
pixel endpoints exactly to 0 and 4095; a one-pixel axis maps to 0. Buttons use 1=left,
2=right, 4=middle, and any mask 0…7. In relative button mode, a button event sends
an absolute position plus the relative button mask. Each held button stays on
the report that pressed it: absolute motion carries only absolute-owned bits,
and relative reports carry only relative-owned bits. `mousemove.buttons` does
not change held state. Origin positioning may first send a nearby `(0,1)` report
without new button presses, then the requested `(0,0)` report, for targets that
ignore all-zero absolute motion. Disconnect cleanup sends releases only.

Non-modifier keys stay in press order, at most six. A seventh down is ignored until
a held key is released and another down can be accepted. Modifier codes use their
left/right HID bits. Ctrl+Alt+Delete is one `shortcut` with
`["ControlLeft","AltLeft","Delete"]`; Alt+Tab uses `["AltLeft","Tab"]`.
The complete shortcut is validated before input and owns the coordinator through
its press/delay/restore reports. It must not be composed from separate client
keydown/keyup messages when atomic behavior is required.
The requested chord and its union with current held keys must fit six
non-modifiers. A generation reset is reconciled before checking that held union;
an oversized union sends no partial chord.

On disconnect or server shutdown, connections with held input attempt release
through the shared coordinator with a one-second application budget. An action's
failure cleanup is not repeated on disconnect; overlapping shutdown reuses a
cleanup's remaining deadline. Serial recovery resets held input; interrupted input
is never replayed. Executing kernel device calls can outlast the cleanup budget.
