# hid-kvm

hid-kvm is a software KVM for Linux hosts. It controls a target machine through a CH9329 USB HID emulator and shows the target's screen in a browser through a USB HDMI capture card.

It is a Go port of [serial-hid-kvm](https://github.com/linkdata/serial-hid-kvm). The port is in the planning stage. See the [planning issues](../../issues) and the tracking epic.

## Goals

- One static Linux binary (amd64, arm64), built with `CGO_ENABLED=0`.
- Standard library first. Third-party modules only when they are widely used and well maintained.
- The browser is the only viewer. There is no native preview window.
- Targets can run any OS. The CH9329 appears to them as a standard USB keyboard and mouse.

## Repository layout

- `research/`: throwaway feasibility prototypes for serial, V4L2 capture, ALSA audio and WebSocket. Each is its own Go module. They are reference material for the port and are deleted once the matching package lands.

## License

MIT. See [LICENSE](LICENSE).
