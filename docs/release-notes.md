# v0.1.1

Swedish text entry supports 154 printable characters, Tab and Enter, including
`£€§½¤µ`, literal accents and composed accented letters.

The built-in `se105` map follows Linux/XKB. For Swedish Windows targets, use
`se105-windows.yaml` with `-target-layout se105-windows` and `-layouts-dir`
pointing to its directory. Literal `´` and `¨` use different sequences on Linux
and Windows. See the
[layout instructions](https://github.com/linkdata/autopaws/blob/v0.1.1/CREATING_LAYOUTS.md#target-os-differences).

The complete Windows map passed physical MCP/HID testing. Linux mappings were
checked against libxkbcommon and its compose table. See the
[Swedish keyboard record](https://github.com/linkdata/autopaws/blob/v0.1.1/docs/validation/swedish-keyboard-2026-11-18.json).
