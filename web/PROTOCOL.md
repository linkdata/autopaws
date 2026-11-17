# Browser WebSocket protocol, version 1

The viewer uses HTTP for embedded assets and one WebSocket at `/ws`. Assets have
`Cache-Control: no-cache`. Unknown page routes return the index page. The default
`coder/websocket` origin check permits the request's own host; cross-origin
browser upgrades are rejected. There is no application WebSocket compression.

Each connection has one ordered input reader and one application-data writer.
Input shares the daemon's complete-action coordinator with the API. Concurrent
control by different operators is unsupported; there are no leases or merged
independent held states.

## Configuration and media

The first server application message is JSON text:

```json
{"type":"config","audio":{"status":"unavailable","generation":"0"}}
```

Audio is implemented in issue #41. Its status becomes `off`, `unavailable`, or
`available`; an available configuration includes negotiated `sampleRate` and
`channels`. Every audio-generation change is sent as configuration before any
samples belonging to that generation. JSON uint64 generations are decimal strings
to preserve precision in JavaScript. Binary uint64 values are exact: clients use
`DataView.getBigUint64(..., true)`, not Number coercion.

Each binary WebSocket message is one 64-byte header followed by its payload.
All multibyte integers are **unsigned little-endian**. Version 1 defines:

| Offset | Type | Meaning |
|---:|---|---|
|0|uint8|Kind: `1` video; `2` reserved audio |
|1|uint8|Protocol version: `1` |
|2|uint16|Header length: `64` |
|4|uint32|Common timestamp flags, below |
|8|uint64|Video settings generation, or audio configuration generation |
|16|uint64|Video connection delivery sequence, or reserved audio source-block sequence |
|24|uint64|Capture timestamp in nanoseconds on the server's `CLOCK_MONOTONIC`, or zero if unavailable |
|32|uint32|Video source JPEG width; zero for audio |
|36|uint32|Video source JPEG height; zero for audio |
|40|uint32|Video crop x; zero for audio |
|44|uint32|Video crop y; zero for audio |
|48|uint32|Video crop width; zero for audio |
|52|uint32|Video crop height; zero for audio |
|56|uint32|Reserved audio sample-frame count; zero for video |
|60|uint32|Reserved audio block duration in nanoseconds; zero for video |

Timestamp flags have the **same meanings for both media kinds**:

* Bit 0 (`1`): the timestamp is available and belongs to server CLOCK_MONOTONIC.
  Without it, offset 24 is zero and must not be used for timing.
* Bit 1 (`2`): video timestamp denotes start of exposure. With bit 1 clear, a valid
  video timestamp denotes end of frame. Audio leaves this bit clear and its
  timestamp denotes the first sample frame of the block.
* Bit 2 (`4`): the monotonic timestamp is an estimate rather than a device timestamp.
  Video currently leaves this bit clear. Issue #41 may use it for audio whose first
  sample time is estimated from a monotonic observation and buffered frame count.
* Bits 3–31 are reserved and zero. Estimated timestamps require bit 0 as well.

The browser's performance clock has a different epoch. Do not compare its clock
directly to server monotonic timestamps. Use sequences, relative timestamps and
durations for gaps/backlog. An audio sample frame contains one sample per channel;
the payload in issue #41 is interleaved signed16-bit little-endian PCM, with exactly
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
frame queue or prevent other clients from receiving frames. Issue #41 gives queued
audio priority over a waiting video frame and sends configuration before new
audio generations; it does not interrupt a JPEG already being written.

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
