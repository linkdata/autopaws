# Windows 11 KVM host setup

Windows 11 builds both `autopaws-kvm.exe` and `autopaws-mcp.exe` without cgo.
Serial I/O uses Windows COM ports. Video and audio use the external
[FFmpeg DirectShow input](https://ffmpeg.org/ffmpeg-devices.html#dshow), sharing
the media pipeline used by macOS. This is a console daemon; Windows service
installation and bundled FFmpeg are not provided.

## Install and connect

Install a Windows FFmpeg build with DirectShow support from the
[FFmpeg download page](https://ffmpeg.org/download.html#build-windows). Put its
`bin` directory on the daemon's `PATH`, then open a new terminal. Install the
CH340/CH341 driver if Windows does not expose the adapter as a COM port. Use
drivers and FFmpeg that work on the host architecture; ARM64 hardware coverage
is separate from the ARM64 cross-build.

Connect the capture card's USB and the CH340/CH341 serial side to the Windows
host. Connect the target's HDMI output to the capture card and its USB input to
the CH9329 HID side. The target can run Linux or another operating system.

Single/mirrored displays use the default identity mapping. A Windows target
with the captured display primary was also tested with its other display still
extended. If the target's absolute HID range spans a larger desktop, configure
the captured display's signed offset, native size and actual input span through
web **Pointer setup → Apply** or API/MCP `set_mouse_mapping`; see
[display mapping](../README.md#display-mapping). The Windows host does not choose
which target displays HID reaches, and geometry cannot reach a display outside
that range. Geometry lives only in the running daemon; reapply it after capture
changes. Optional `-mouse-mode extended` blocks positioned input until configured.

Use the release ZIP containing both executables, or build from PowerShell with
Go 1.27.4 or newer:

```powershell
$env:CGO_ENABLED = '0'
go build -trimpath -o dist/ ./cmd/...
ffmpeg -version
.\dist\autopaws-kvm.exe --version
.\dist\autopaws-kvm.exe --list-devices
```

`--help` and `--version` work without FFmpeg. Device listing and daemon startup
require it on `PATH`. Listing discovers supported USB serial ports and DirectShow
video/audio inputs without starting capture streams. A GUI launcher can have a
different `PATH` or media permissions from the terminal.

## Select devices and start

Use the `COMn` port printed by `--list-devices`, including numbers above COM9.
An empty serial setting selects the only supported CH340/CH341 adapter; multiple
adapters require an explicit port. Windows opens the port exclusively, so close
other serial applications before starting the daemon.
Moving the adapter to another USB port can change its COM number. Repeat
`--list-devices` and update an explicit selection, or restart with automatic
serial selection when exactly one supported adapter is attached.

Copy the exact `dshow:` selectors printed in the inventory. They use DirectShow
alternative names so identical friendly names remain distinguishable. A unique,
exact friendly name is also accepted, but canonical selectors are preferred.
Explicit missing selectors fail instead of opening another device. Repeat
discovery after moving hardware to a different USB port or reinstalling drivers.

In PowerShell, use single quotes around copied selectors to preserve their
backslashes and other characters. Substitute the selected devices and actual
target dimensions here:

```powershell
$video = 'dshow:PASTE_VIDEO_ALTERNATIVE_NAME'
$audio = 'dshow:PASTE_AUDIO_ALTERNATIVE_NAME'
.\dist\autopaws-kvm.exe `
  -serial-device COM3 `
  -capture-device $video -audio-device $audio `
  -capture-width 1920 -capture-height 1080 `
  -screen-width 1920 -screen-height 1080 `
  -target-layout us104 -web 127.0.0.1:9330
```

Open <http://127.0.0.1:9330> and turn off View only to control the target.
The API defaults to `127.0.0.1:9329`, and mouse
buttons default to relative reports. Target keyboard layout and coordinate
dimensions are independent of the Windows host and capture resolution. The
same YAML configuration, MCP tools, browser controls and shared-token settings
apply. In YAML, single-quote copied `dshow:` selectors as well.

Video auto-selection prefers non-webcam names, then falls back to the first
video input. Select the HDMI capture card explicitly when several inputs are
attached. The backend requests MJPEG at 30 fps and the configured dimensions;
unsupported capture modes report unavailable.

Audio requires an explicit `-audio-device 'dshow:...'` because this backend does
not automatically pair video and audio USB interfaces. Use `-audio-device off`
to disable audio. FFmpeg converts the input to 48 kHz stereo S16_LE; the capture
device's native rate may differ. Video and audio use separate processes with no
hardware synchronization guarantee.

## Permissions, recovery and shutdown

Allow camera and microphone access for desktop applications in Windows Settings.
Video uses camera permission; HDMI audio uses microphone permission. If access
was denied, correct the launching application's permissions and restart the
daemon. Device-in-use and permission errors leave diagnostics on stderr.

If Windows reports Configuration Descriptor Request Failed before the card
appears in the inventory, resolve USB enumeration first; FFmpeg cannot open an
input Windows has not enumerated. In one recorded check, moving USB connections
restored enumeration after a power cycle did not. The cause was not established;
this observation does not identify a defective USB port.

Press Ctrl+C to stop the console daemon and attempt release of held input.
Cancellation and disconnect recovery do not replay interrupted input. After a
serial reconnect, release-all precedes fresh input. A forcefully terminated
process cannot perform normal cleanup.

Capture starts on demand and stops after ten seconds without video demand.
Failed media opens/reads retry while demand remains; a stalled FFmpeg process is
stopped and joined before recovery. Explicit selectors stay fixed across retries.
After USB re-enumeration changes a selector, list devices and reselect it.

FFmpeg video/audio timestamps estimate pipe receipt on the host monotonic clock.
They are marked estimated and exclude upstream FFmpeg/driver buffering. A fresh
snapshot does not prove that the target processed preceding input. Application
queues remain bounded; no Windows end-to-end latency guarantee is made.

## Validation

CI uses mocks and generated media without physical devices. Hardware acceptance
runs locally with the Windows host and connected Linux target; see
[hardware validation](hardware-validation.md). Windows ARM64 is cross-built;
ARM64 hardware testing is not required for this support addition.
