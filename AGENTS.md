# Agent instructions

The target machine's access credentials are stored in `~/autopaws.txt` on the development host.

The target machine is dedicated to testing and contains no sensitive data or credentials. You may reboot or reinstall it whenever needed for development or testing.

Use the available Linux setup for physical KVM validation. Separate Windows/macOS target tests and ARM64 hardware tests are not required. Keep cross-compilation checks for the supported platforms.

The existing Python tools, `serial-hid-kvm` and `mcp-serial-hid-kvm`, are available on `PATH`. You may use them during development to test functionality while porting the projects to Go.

The usual startup command is `serial-hid-kvm --api --mouse-button-mode relative`. Omitting these arguments causes MCP access or mouse clicks to fail.

The `claude` CLI is available on `PATH`. You may use Claude as an adversarial reviewer during development.

Use Claude sparingly: critique changes yourself until the final review round. If Claude's usage credits run out, continue self-reviewing until they refill. When using Claude, use streaming mode so you can monitor its progress.

Unless otherwise specified, changes require two consecutive clean reviews before merging. A clean review reports no defects or suggested simplifications. Address any findings and restart the count. Security defects are out of scope for reviews during the current porting stage.
