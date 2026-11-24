# Linux device and service setup

These Debian/Ubuntu examples run the daemon without root privileges. Check your
distribution's device ownership and group names. Use example files from the same
revision as your installed binary:

```sh
git clone https://github.com/linkdata/autopaws.git autopaws
cd autopaws
```

For a release, run `git checkout YOUR_INSTALLED_TAG`. Source builds can use their
existing checkout. The README's foreground quick start needs neither service
installation nor custom udev rules.

## Device access

Serial normally uses `dialout`, video uses `video`, and ALSA PCM capture uses
`audio`. Inspect the selected nodes with `ls -l`. Add your interactive user to
the relevant groups, then sign out and back in (or reconnect SSH):

```sh
sudo usermod --append --groups dialout,video,audio "$USER"
```

The example [udev rule](../examples/udev/70-autopaws-serial.rules) assigns the
supported CH340/CH341 serial nodes to `dialout` with mode `0660`. Install it,
reload the rules, and unplug/replug the serial adapter:

```sh
sudo install -m 0644 examples/udev/70-autopaws-serial.rules /etc/udev/rules.d/70-autopaws-serial.rules
sudo udevadm control --reload-rules
```

Keep the system's video/audio rules. Do not replace device permissions with a
world-writable mode. Run `autopaws-kvm --list-devices` as the eventual daemon user
to confirm inventory and permissions. It queries V4L2 capabilities but starts no
stream and opens neither tty nor PCM devices.

Use `/dev/serial/by-id/...` for `serial-device` when available. If an adapter has
no unique ID, use `/dev/serial/by-path/...` to identify its physical USB port.
Replugging can change `ttyUSBn`. Multiple supported serial devices require an
explicit path. An explicit video path can likewise use a stable V4L symlink.

ALSA capture opens the selected PCM exclusively. If it is busy, follow the
canonical [WirePlumber 0.5 per-card setup](../alsa/README.md#exclusive-access-with-wireplumber-05).
Use the instructions for your installed WirePlumber version. There is no
PipeWire/PulseAudio fallback.

## Optional system service

After the foreground quick start works, create the dedicated service account
and install the [example configuration](../examples/autopaws.yaml) and
[unit](../examples/systemd/autopaws-kvm.service):

```sh
sudo useradd --system --user-group --home-dir /var/lib/autopaws --create-home --shell /usr/sbin/nologin autopaws
sudo install -d -o root -g autopaws -m 0750 /etc/autopaws
sudo install -o root -g autopaws -m 0640 examples/autopaws.yaml /etc/autopaws/autopaws.yaml
```

Edit `/etc/autopaws/autopaws.yaml`: set the serial path, target layout and target
screen size. The example's API/web listeners bind to loopback. Confirm `dialout`,
`video` and `audio` exist; the unit grants them as supplementary groups.

Optional authentication uses `token-file` in this YAML file for API, web and MCP.
To enable it for a new installation, create a secret file readable by the
service account:

```sh
sudo install -o root -g autopaws -m 0640 /dev/null /etc/autopaws/token
openssl rand -hex 32 | sudo tee /etc/autopaws/token >/dev/null
```

Set `token-file: /etc/autopaws/token` in `/etc/autopaws/autopaws.yaml`. The daemon
reads and trims the nonempty UTF-8 secret once before creating devices or
listeners; an unreadable or empty file prevents startup. Alternatively, store
the secret directly in YAML as `token`. Nonempty `token` and `token-file`
together are rejected; leaving both empty disables authentication. MCP can reuse
the configuration with `-config /etc/autopaws/autopaws.yaml` when its user can
read both the configuration and token files, for example as a member of the
`autopaws` group. Restart both processes after changing either file.
The viewer bootstraps its session cookie from
the URL fragment; see the canonical [Security instructions](../README.md#security)
for that URL, SSH forwarding and token handling.

```sh
sudo install -m 0644 examples/systemd/autopaws-kvm.service /etc/systemd/system/autopaws-kvm.service
sudo systemctl daemon-reload
sudo systemctl enable --now autopaws-kvm.service
journalctl -u autopaws-kvm.service -f
```

`sudo systemctl restart autopaws-kvm` applies edited configuration; there is no
hot reload. The unit lets retained kernel calls finish during stop, so shutdown
may outlast the application's cleanup deadline. Do not run another KVM daemon
against the same serial or capture devices while the service owns them.

References: [udev rules](https://www.freedesktop.org/software/systemd/man/latest/udev.html),
[systemd services](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html).
