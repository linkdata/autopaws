# Agent instructions

The target machine's access credentials are stored in `~/autopaws.txt` on the development host.

The target machine is dedicated to testing and contains no sensitive data or credentials. You may reboot or reinstall it whenever needed for development or testing.

The existing Python tools, `serial-hid-kvm` and `mcp-serial-hid-kvm`, are available on `PATH`. You may use them during development to test functionality while porting the projects to Go.

The `claude` CLI is available on `PATH`. You may use Claude as an adversarial reviewer during development.

Unless otherwise specified, changes require two consecutive clean reviews before merging. A clean review reports no defects or suggested simplifications. Address any findings and restart the count. Security defects are out of scope for reviews during the current porting stage.
