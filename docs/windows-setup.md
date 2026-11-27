# Windows 11 KVM host setup

## Install and start

Install the [release binaries](../README.md#install), or follow the
[source build instructions](../README.md#build-and-develop). Install FFmpeg with
[DirectShow support](https://ffmpeg.org/download.html#build-windows) and put its
`bin` directory on the daemon's `PATH`, then open a new terminal. Install the
CH340/CH341 driver if the adapter does not appear as a COM port. Use drivers and
FFmpeg for the host architecture.

[Connect the hardware](../README.md#hardware), then start the daemon with the viewer:

```powershell
autopaws-kvm -web
```

Open [the viewer](http://127.0.0.1:9330) and turn off **View only** to control the
target. Serial and video devices are selected automatically; audio requires an
explicit selection below. Configure the controlled computer using
[target setup](target-setup.md), independently of the Windows host.
This is a console daemon; a Windows service installer and bundled FFmpeg are
not provided.

Allow camera and microphone access for desktop applications in Windows Settings.
Video uses camera permission; HDMI audio uses microphone permission. Correct the
launching application's permissions and restart if access was denied. GUI
launchers can have a different `PATH` or permissions from the terminal.

## Device selection and troubleshooting

If automatic selection fails or chooses the wrong video device, or to choose an
audio input, inspect the inventory:

```powershell
autopaws-kvm --list-devices
```

Listing uses FFmpeg's [DirectShow input](https://ffmpeg.org/ffmpeg-devices.html#dshow)
without starting streams. Listing and daemon startup require FFmpeg on `PATH`;
`--help` and `--version` do not.

With multiple serial adapters, set `-serial-device COMn` to the selected port;
numbers above COM9 work. Ports open exclusively, so close other serial programs.
Moving USB connections can change COM numbers. Update an explicit port or
restart with automatic selection when exactly one supported adapter is attached.

Copy exact `dshow:` selectors from the inventory. Alternative names distinguish
devices with identical friendly names; a unique exact friendly name also works.
Explicit missing selectors fail rather than selecting another device. Repeat
discovery after moving USB connections or reinstalling drivers.

Use single quotes in PowerShell and YAML to preserve selector backslashes. For
example, to override video selection and enable capture audio:

```powershell
$video = 'dshow:PASTE_VIDEO_ALTERNATIVE_NAME'
$audio = 'dshow:PASTE_AUDIO_ALTERNATIVE_NAME'
autopaws-kvm -web -capture-device $video -audio-device $audio
```

Video selection prefers non-webcam names, then the first video input. The backend
requests MJPEG at 30 fps and the configured dimensions; unsupported modes report
unavailable. See [configuration](configuration.md) for other overrides.

If Windows reports Configuration Descriptor Request Failed, resolve USB
enumeration before trying capture. Audio open errors are logged to stderr;
check viewer/API status for video and serial errors. See [recorded Windows-host limits](hardware-validation.md#windows-host).

## Audio, recovery and shutdown

Use `-audio-device 'dshow:...'` for audio or `-audio-device off` to disable it.
Automatic video/audio pairing is unsupported. FFmpeg converts audio to 48 kHz
stereo S16_LE; the native device rate may differ. Video and audio use separate
processes without a hardware synchronization guarantee.

Capture starts on demand and stops after ten seconds without video demand.
Failed opens/reads retry while demand remains; stalled FFmpeg processes stop and
join before recovery. Explicit selectors stay fixed. If re-enumeration changes
a selector, list devices and reselect it. See [mapping after capture changes](target-setup.md#display-mapping).

Press Ctrl+C to stop and attempt release of held input. Cancellation and
recovery do not replay interrupted input. Serial reconnect releases input before
fresh actions. A forcefully terminated process cannot perform normal cleanup.

Timestamps estimate FFmpeg pipe receipt on the host monotonic clock and exclude
upstream buffering. A fresh frame does not prove preceding input reached the
target. See [hardware validation](hardware-validation.md) for tested behavior;
Windows ARM64 is cross-built but has not been hardware-tested.
