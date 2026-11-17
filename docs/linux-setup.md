# Linux device and service setup

These are Debian/Ubuntu-style examples. Check your distribution's actual device
ownership and group names before applying them. They do not require a privileged
KVM daemon.

The example files are in the repository. For the optional udev/service setup,
use a checkout of the same revision as the installed binary. With authenticated
private-repository access:

```sh
gh repo clone linkdata/autopaws autopaws
cd autopaws
```

For a released binary, run `git checkout YOUR_INSTALLED_TAG` before using its
examples. A source build can use the examples from its existing checkout. The
foreground quick start in the README needs no service or udev installation.

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
world-writable mode. Run `autopaws-kvm list-devices` as the eventual daemon user
to confirm inventory and permissions. It queries V4L2 capabilities but starts no
stream and opens neither tty nor PCM devices.

Use `/dev/serial/by-id/...` for `serial-device` when available. If an adapter has
no unique ID, use `/dev/serial/by-path/...` to identify its physical USB port.
Replugging can change `ttyUSBn`. Multiple supported serial devices require an
explicit path. An explicit video path can likewise use a stable V4L symlink.

ALSA capture opens the selected PCM exclusively. If it is busy, follow the
canonical [WirePlumber 0.5 per-card setup](../alsa/README.md#exclusive-access-with-wireplumber-05).
That recipe uses the exact `device.name`, includes restart/rollback instructions
and avoids changing unrelated host audio policy. Use the instructions for your
installed WirePlumber version. Record the release setup's actual negotiation and
quirks in the [hardware validation record](hardware-validation.md); this setup
guide does not claim those checks have passed. There is no
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

Optional authentication uses `token-file` in this YAML file for both API and web.
Leave it empty to disable authentication. To enable it for a new installation,
create a token readable by the service account:

```sh
sudo install -o root -g autopaws -m 0640 /dev/null /etc/autopaws/token
openssl rand -hex 32 | sudo tee /etc/autopaws/token >/dev/null
```

Set `token-file: /etc/autopaws/token` in `/etc/autopaws/autopaws.yaml`. The daemon
reads and trims the nonempty UTF-8 secret once before creating devices or
listeners; an unreadable or empty file prevents startup. Restart the daemon
after changing it. Each MCP process needs the same secret in its own readable
file and `-token-file PATH` flag. The viewer bootstraps its session cookie from
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

The unit and rule are examples until validated on the final #43 setup. They do
not assert distribution-wide installation or runtime support.

References: [udev rules](https://www.freedesktop.org/software/systemd/man/latest/udev.html),
[systemd services](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html).
