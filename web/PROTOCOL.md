# Browser WebSocket protocol, version 1

The viewer uses HTTP for embedded assets and one WebSocket at `/ws`. Assets have
`Cache-Control: no-cache`. Unknown page routes return the index page. The default
`coder/websocket` origin check permits the request's own host; cross-origin
browser upgrades are rejected. There is no application WebSocket compression.

Each connection has one ordered input reader and one application-data writer.
Input shares the daemon's complete-action coordinator with the API. Concurrent
control by different operators is unsupported; there are no leases or merged
independent held states.

## Optional shared token

When `autopaws-kvm -token-file PATH` is configured, `/ws` requires the
`autopaws_token` cookie; missing or wrong values receive HTTP 403 before upgrade
or subscription.
All bootstrap assets remain public. Open
`http://localhost:9330/#token=ENCODED_SECRET`, where ENCODED_SECRET is the result
of JavaScript `encodeURIComponent(secret)`. The page removes the recognized
fragment with `history.replaceState` and writes the URI-encoded secret into a
session cookie before opening its first WebSocket. The cookie uses `Path=/` and
`SameSite=Strict`, plus `Secure` on HTTPS. No Domain or persistent expiry is set.
The server URI-decodes the cookie value without treating `+` as a space. Reconnects
reuse the cookie; tokens never appear in WebSocket URLs or application logs.
The fragment is processed locally and is not sent in HTTP requests.

Without a configured token, assets and WebSocket behavior are unchanged. Cookies
are scoped to a host rather than a TCP port; use one token for this viewer host.
Use localhost through the documented SSH tunnel for remote access. Authentication
does not add TLS or a supported reverse-proxy deployment.

## Configuration and media

The first server application message is JSON text:

```json
{"type":"config","audio":{"status":"unavailable","generation":"0"}}
```

Audio status is `off`, `unavailable`, or `available`. An available configuration
includes the negotiated `sampleRate` and `channels`; for example,
`{"type":"config","audio":{"status":"available","generation":"7","sampleRate":48000,"channels":2}}`.
Audio is captured lazily while web clients are connected. On Linux, auto
selection follows the current selected video USB device; an explicit PCM path
stays fixed. macOS requires an explicit AVFoundation audio selector. Each
accepted video generation reopens audio, including resolution changes, so all
queued media starts fresh. Selection/open/read failures make audio unavailable
without ending video or input. Every audio-generation change is sent as configuration before any
samples belonging to that generation. JSON uint64 generations are decimal strings
to preserve precision in JavaScript. Binary uint64 values are exact: clients use
`DataView.getBigUint64(..., true)`, not Number coercion.

Each binary WebSocket message is one 64-byte header followed by its payload.
All multibyte integers are **unsigned little-endian**. Version 1 defines:

| Offset | Type | Meaning |
|---:|---|---|
|0|uint8|Kind: `1` video; `2` audio |
|1|uint8|Protocol version: `1` |
|2|uint16|Header length: `64` |
|4|uint32|Common timestamp flags, below |
|8|uint64|Video settings generation, or audio configuration generation |
|16|uint64|Video connection delivery sequence, or audio source-block sequence |
|24|uint64|Capture timestamp in nanoseconds on the server's `CLOCK_MONOTONIC`, or zero if unavailable |
|32|uint32|Video source JPEG width; zero for audio |
|36|uint32|Video source JPEG height; zero for audio |
|40|uint32|Video crop x; zero for audio |
|44|uint32|Video crop y; zero for audio |
|48|uint32|Video crop width; zero for audio |
|52|uint32|Video crop height; zero for audio |
|56|uint32|Audio sample-frame count; zero for video |
|60|uint32|Audio block duration in nanoseconds; zero for video |

Timestamp flags have the **same meanings for both media kinds**:

* Bit 0 (`1`): the timestamp is available and belongs to server CLOCK_MONOTONIC.
  Without it, offset 24 is zero and must not be used for timing.
* Bit 1 (`2`): video timestamp denotes start of exposure. With bits 1 and 2 clear, a valid
  video timestamp denotes end of frame. Audio leaves this bit clear and its
  timestamp denotes the first sample frame of the block.
* Bit 2 (`4`): the monotonic timestamp is an estimate rather than a device timestamp.
  Linux video leaves this bit clear. Linux audio sets it because its first sample time is
  estimated from an ALSA monotonic timestamp/delay observation and block frame count.
  macOS video and audio both set this bit: their timestamps estimate FFmpeg pipe
  receipt on the server monotonic clock, not device capture time. FFmpeg/OS
  buffering is unmeasured; video uses read-start time and audio estimates its first
  sample from read completion minus the block duration.
* Bits 3–31 are reserved and zero. Estimated timestamps require bit 0 as well.

The browser's performance clock has a different epoch. Do not compare its clock
directly to server monotonic timestamps. Use sequences, relative timestamps and
durations for gaps/backlog. An audio sample frame contains one sample per channel;
the payload is interleaved signed16-bit little-endian PCM, with exactly
sampleFrames×channels samples. Audio block duration is below the 200 ms application
queue budget, so uint32 nanoseconds fits. Its source-block sequence identifies
dropped blocks; audio configuration generation distinguishes stream restarts.

Video payload is the normalized source JPEG unchanged. Crop metadata gives the
visible target area; a full-frame crop represents no border removal. Source
dimensions describe the JPEG, not the browser canvas or the configured target's
pixel dimensions. The server does not encode video separately for each viewer.

Video delivery sequences start at 1 and increase for each message attempted by
that connection's sole writer. They do not reset when capture stops/reopens or
settings generation changes. V4L2's raw source sequence can reset on reopen and
is not used for connection ordering. Settings changes flush subscriber slots;
the writer also discards a held frame if its generation no longer matches the
manager. An already-started socket write cannot be recalled. The browser must
discard pending image decodes/rendered metadata when a newer generation arrives,
and reject late image-decode results by generation and delivery sequence.

Video is paced to the configured `web-fps` limit after each completed write.
The writer reads a latest-frame slot when ready, so slow readers skip source
frames. Every configuration/media write has a one-second default application
deadline. A stalled write closes that connection; it does not grow a per-client
frame queue or prevent other clients from receiving frames. Queued audio has priority over a waiting video frame. Configuration precedes
new-generation samples; a JPEG already being written is not interrupted.

Audio uses one ALSA subscriber queue per connection, bounded to100ms of sample
frames. Stale expiry measures age since the LAST sample in a block was captured,
separately from acquisition duration. The writer dequeues PCM only immediately
before its send, never across config/JPEG writes, and rejects obsolete video or
audio generations. After a JPEG send takes at least100ms, it closes the old audio
subscription and starts an empty one, discarding the backlog. The browser reserves
50ms for its worklet ring and at most50ms for one acknowledged PCM transfer, with
no additional pending PCM slot. While that transfer awaits acknowledgement,
new arrivals are dropped at admission because posted data cannot be recalled.
Ring overflow and oversized-block trimming discard oldest samples first. The
application-queued total is at most200ms.
Capture/kernel, WebSocket/TCP and output latency remain separate, and issue43
measures the actual end-to-end result. Browser source-age tracking uses timestamp
differences and local elapsed time, never a direct clock-epoch comparison.
An available generation-only change at the same rate/channels resets the running
worklet in MessagePort order. Any old posted PCM still occupies the sole transfer
slot until its matching generation/sequence acknowledgement; a late old ACK
cannot admit an extra transfer or clear a newer one. Rate/status changes rebuild
the graph, preserving the user's audio choice.

## Browser input

Send one JSON object per text WebSocket message. The pinned WebSocket library's
default input-message limit is 32768 bytes. Required fields reject null or
wrong types. Coordinates and masks must be integer JSON values. Unknown object
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

Coordinates range from 0 through 4095 on both axes and go straight to HID reports;
they are not converted through target-screen pixels. The page maps visible crop
pixel endpoints exactly to 0 and 4095; a one-pixel axis maps to 0. Buttons use 1=left,
2=right,4=middle, and any mask 0…7. In relative button mode, a button event sends
a button-free absolute position plus the relative button mask. Movement carries
actual held bits in its absolute report in both modes, matching the shared Mouse
behavior. A mismatched `mousemove.buttons` cannot silently mark a relative button
released without the corresponding relative release report.

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

On disconnect or server shutdown, only connections with held keys/buttons attempt
release, through the shared coordinator with an uncancelled one-second cleanup
context. If an active input action already performed its failure cleanup, that
connection does not start a second cleanup budget. Failed transport sends are
reconciled by serial Prepare's reset on reopen; interrupted input is never replayed.
An in-flight release-only disconnect cleanup is registered with the controller,
so overlapping daemon shutdown reuses its remaining deadline as well.
Application budgets govern admission and waits; an executing Linux driver call
retains ownership until it returns. Server Close cancels every client before Wait
joins their handlers, so retained input ownership does not hold a server registry
lock or delay cancellation of other clients.
