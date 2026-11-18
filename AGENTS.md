# Agent instructions

The Linux test target's access credentials are stored in `~/autopaws.txt` on the development host.

The Linux target is dedicated to testing and contains no sensitive data or credentials. You may reboot or reinstall that Linux target whenever needed for development or testing.

Use the available Linux setup for physical KVM validation. The additional owner-required limited read-only Windows laptop smoke passed before v0.1.0; broader Windows/macOS target tests and ARM64 hardware tests are not required. Keep cross-compilation checks for the supported platforms.

For the Windows laptop test, record test actions, pass/fail and host/capture metadata. Do not reference screen contents, labels or identifiers, or retain screenshots/OCR. Place no scripts on the Windows laptop and change no target files or settings; the owner handles target setup.

The existing Python tools, `serial-hid-kvm` and `mcp-serial-hid-kvm`, are available on `PATH`. You may use them during development to test functionality while porting the projects to Go.

The usual Python startup command is `serial-hid-kvm --api --mouse-button-mode relative`. Omitting these arguments from the Python command causes MCP access or mouse clicks to fail. The Go command `autopaws-kvm` starts the daemon with the API at `127.0.0.1:9329` and relative mouse button mode by default; those flags are not required for Go. Use `autopaws-kvm --version` or `autopaws-kvm --list-devices` for those operations; there are no subcommands.

The `claude` CLI is available on `PATH`. You may use Claude as an adversarial reviewer during development.

Use Claude sparingly: critique changes yourself until the final review round. If Claude's usage credits run out, continue self-reviewing until they refill. When using Claude, use streaming mode so you can monitor its progress.

Install CI tooling locally. After the final edits, run `actionlint` and the complete local CI gate documented in the README. They must pass before pushing changes or manually starting GitHub Actions.

Unless otherwise specified, changes require two consecutive clean reviews before merging. A clean review reports no defects or suggested simplifications. Address any findings and restart the count. Security defects are out of scope for reviews during the current porting stage.
