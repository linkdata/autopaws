# Capture and browser viewer

Linux capture uses V4L2 MJPG. Automatic video selection prefers non-webcam names;
automatic audio matches the selected video's USB device. An explicit PCM path
stays fixed. macOS uses FFmpeg/AVFoundation; Windows uses FFmpeg/DirectShow.
Both require explicit audio selection and use separate FFmpeg video/audio processes.

Cropping is disabled by default, preserving the full capture frame.

Optional `-autocrop` uses `screen-width` and `screen-height` to find the centered
target area, then verifies the surrounding bars are black. When opting in,
these dimensions must match the target's output aspect. Dark content within
that area does not change the crop. Keep cropping disabled for captures that
do not use centered, aspect-preserving scaling. Bars too narrow to verify remain
visible.

Capture starts on demand and stops after ten seconds without video demand.
Snapshots wait up to five seconds for a fresh decodable frame and optionally crop
validated black bars. Linux timestamps reflect device capture; macOS/Windows
timing estimates pipe receipt and cannot account for FFmpeg/OS buffering. A fresh frame does not
prove the target finished processing input. Missing capture hardware leaves input
usable. Device/resolution changes invalidate pending snapshots with `capture changed`.

For display geometry, see [target setup](target-setup.md#display-mapping).

The viewer opens in View only mode. Turn it off and click the video to focus
physical keyboard and mouse input, independent of the text layout.
The toolbar offers Ctrl+Alt+Delete, Alt+Tab, view-only mode, cursor,
sizing and fullscreen controls. Focus loss, view-only mode and stream changes
release held input. Connections reconnect automatically without replaying input.
Keyboard Lock depends on browser support; toolbar shortcuts remain available.
The installable viewer caches no assets or media.

## Browser audio

Click Audio to play and mute to stop. Playback requires AudioWorklet and a secure
context; use localhost or an [SSH tunnel](security.md). Errors leave video and input
usable. macOS and Windows convert audio to 48 kHz stereo; Linux uses the negotiated PCM rate.
Audio choices persist across reconnects. Recovery flushes buffered samples;
configuration changes reopen the selected source. The server/browser application
queues total at most 200 ms, excluding capture, network and output-device latency.
See [ALSA setup](../alsa/README.md) and [measured results](audio-measurement.md).

For wire formats and input rules, see the [API reference](../api/PROTOCOL.md) and
[browser protocol](../web/PROTOCOL.md). Go clients can use `apiclient`.
