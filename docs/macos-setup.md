# macOS KVM host setup

## Install and start

Install the [release binaries](../README.md#install), or follow the
[source build instructions](../README.md#build-and-develop). Intel and Apple
silicon Macs are supported. Install FFmpeg separately and put it on the daemon's
`PATH`:

```sh
brew install ffmpeg
```

[Connect the hardware](../README.md#hardware), then start the daemon with the viewer:

```sh
autopaws-kvm -web
```

Open [the viewer](http://127.0.0.1:9330) and turn off **View only** to control the
target. Serial and video devices are selected automatically; audio requires an
explicit selection below. Configure the controlled computer using
[target setup](target-setup.md), independently of the Mac host.

macOS may request Camera access for video and Microphone access for audio.
Grant access to the application launching the daemon, then restart if needed.
Check System Settings → Privacy & Security if access was denied. External HDMI
capture does not use host screen recording. GUI launchers and services can have
a different `PATH` and privacy identity from your terminal.

## Device selection and troubleshooting

If automatic selection fails or picks the wrong video device, or to choose an
audio input, inspect the inventory:

```sh
autopaws-kvm --list-devices
```

Discovery uses `ioreg` for CH340/CH341 devices and FFmpeg's
[AVFoundation input](https://ffmpeg.org/ffmpeg-devices.html#avfoundation) for
video/audio. Host display-capture entries are excluded. Listing starts no
streams but can be subject to privacy restrictions. Daemon startup and listing
require FFmpeg, including API-only use; `--help` and `--version` do not.

With multiple serial adapters, use `-serial-device /dev/cu.YOUR_ADAPTER` from the
inventory. Discovery recognizes USB VID/PID `1a86:7523` and `1a86:7522`.
Copy exact `avfoundation:NAME` selectors, quoting spaces. For example, to override
video selection and enable capture audio:

```sh
autopaws-kvm -web \
  -capture-device 'avfoundation:REPLACE_WITH_YOUR_VIDEO_NAME' \
  -audio-device 'avfoundation:REPLACE_WITH_YOUR_AUDIO_NAME'
```

Video selection prefers non-webcam names, then the first camera. Duplicate names,
names with colons, numeric/reserved prefixes and ambiguous prefixes are rejected.
Disconnect duplicate devices or rename them through supported vendor tooling.
Repeat discovery after reconnecting hardware; AVFoundation indices can change.

The backend requests 30 fps at the configured size and encodes MJPEG. Unsupported
modes report unavailable. The inventory does not enumerate pixel formats.
For dimensions and other overrides, see [configuration](configuration.md).

## Audio and recovery

Use `-audio-device 'avfoundation:NAME'` for audio or `-audio-device off` to disable
it. Empty selection reports audio unavailable because automatic video/audio
pairing is unsupported. FFmpeg converts audio to 48 kHz stereo S16_LE with a
100 ms per-subscriber application queue and expiry. Video and audio use separate
processes without a hardware synchronization guarantee.

Media retries failed opens/reads after one second while demand remains. FFmpeg
restarts after five seconds without output. Capture stops after ten seconds
without video demand. Reselecting video also reopens media. See
[mapping after capture changes](target-setup.md#display-mapping).

See [capture timing limits](browser.md) and [hardware validation](hardware-validation.md).
