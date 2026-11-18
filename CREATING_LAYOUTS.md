# Creating keyboard layouts

Keyboard layouts translate text into the physical keys expected by the target
machine. Select the target's layout, regardless of the development host's layout.
Browser keyboard events use physical key positions and do not use these text maps.

The built-in layouts are `us104`, `uk105`, `de105`, `fr105`, `jp106` and `se105`.
They cover printable ASCII, Tab and Enter. `se105` covers 154 printable
characters, including `åäöÅÄÖ`, `£€§½¤µ`, literal accents and accented letters
formed with the Swedish acute, diaeresis, grave, circumflex and tilde dead keys.
The UK, German, French and Swedish maps follow Linux/XKB's basic layouts.
Windows and macOS variants can differ and require custom files.

## File format

A layout file contains an `overrides` mapping. Its entries replace characters in
a fresh US104 base; characters without an override retain their US104 mapping.
Use the filename stem as the layout name: `my-layout.yaml` becomes `my-layout`.

```yaml
overrides:
  "@": [ralt, 0x14]
  "å": [none, "0x2F"]
  "^": [[none, 0x35], [none, 0x2C]]
```

Each quoted key must contain exactly one Unicode code point. A value is either
one `[modifier, keycode]` pair or a nonempty list of such pairs. In the example,
`^` presses the dead circumflex key, releases it, then presses and releases Space
(`0x2C`). The target combines those strokes into a literal circumflex.

Modifier names are case-insensitive:

| Modifier | Names |
|---|---|
| None | `none`, used alone |
| Left Control | `ctrl`, `lctrl` |
| Right Control | `rctrl` |
| Left Shift | `shift`, `lshift` |
| Right Shift | `rshift` |
| Left Alt | `alt`, `lalt` |
| Right Alt / AltGr | `ralt` |
| Left GUI | `win`, `lwin`, `gui`, `super`, `meta` |
| Right GUI | `rwin` |

Combine modifiers with `+`, for example `shift+ralt`. Spaces around names are
allowed. GUI is the Windows key on a PC and Command on a Mac.

Keycodes are USB HID keyboard usages from `0x00` to `0xFF`. Supply a YAML integer
such as `4` or `0x04`, or a quoted hexadecimal string such as `"0X04"`. Quoted
codes accept only the `0x` prefix and hexadecimal digits: signs, whitespace and
underscores are rejected. A quoted decimal string such as `"4"` is rejected.
Zero is valid and represents an empty key slot.

Unquoted integers follow YAML numeric syntax: leading-zero values such as `044`
are octal (`0x24`), not decimal. Use decimal without padding or explicit `0x` hex.

Unknown fields, duplicate characters, multiple YAML documents, malformed pairs,
unknown modifiers and out-of-range keycodes are errors. Merge keys and alias
references are not supported. Quote character keys, including digits and symbols.

## Loading a custom file

When a user layout directory is configured, its `<name>.yaml` is searched before
the built-in file with the same name. A selected user file's read or parse error
is reported; it does not fall back silently.

A user file replaces the built-in override file, then applies its overrides to
US104. It does not inherit the selected built-in layout's overrides. To modify
German, for example, copy [de105.yaml](hid/layouts/de105.yaml) into the user layout
directory and edit that copy. Other built-in files are in [hid/layouts](hid/layouts).
Each loaded layout is independent and cannot be changed through a lookup result.

## Changing the active layout

Call the API method or read-only MCP tool `list_keyboard_layouts` with no
parameters to discover maps. Its result contains the active `keyboard_layout`
and a `layouts` array sorted by name. Each entry has a `name`, a `source` of
`builtin` or `custom`, and an optional `error` when the map cannot be loaded.
Choose an entry without an error when switching.

Custom `*.yaml` names come from the daemon's configured `-layouts-dir`; a custom
map replaces the same built-in name in this list. An invalid custom override is
reported with its error rather than silently falling back to the built-in map.
An empty or missing configured directory yields the built-in maps only. Other
directory-read failures return `unavailable`.

`-target-layout` selects the daemon's startup map. To select another map while it
is running, call the API method or MCP tool `set_keyboard_layout`:

```json
{"layout":"se105"}
```

The result is `{"keyboard_layout":"se105"}`. Subsequent text, key tags,
single-character `send_key` values and character-based sequences use the selected
map. `get_device_info.config.keyboard_layout` reports its active name.

Use one active MCP connection with sequential requests. The selection belongs to
the running daemon; it does not rewrite configuration. Restarting restores the
CLI/YAML startup layout. Select the matching input source on the target OS
separately. Physical browser key positions, named special keys and hexadecimal
HID usages keep their existing behavior.

The tool accepts any built-in name listed above or a custom filename stem in the
daemon's configured `-layouts-dir`. Custom-file precedence is unchanged, and a
request cannot supply a directory path. Selecting the same name reloads its file.
The complete replacement is loaded and validated before activation; an invalid,
unreadable or malformed selection leaves the previous map active. Listing and
switching perform no hardware input, work with disconnected devices, and
preserve capture and screenshot-coordinate state.

## Target OS differences

The Linux French map can type `~` and grave directly. A Windows French custom
file must use the correct Windows key positions and append Space where its `~`
or grave is a dead key; adding Space to the Linux mapping alone is insufficient.
For macOS German, `@` uses Option+L rather than a PC's AltGr+Q; a custom file can
map it to `[alt, 0x0F]`. Verify mappings against the exact layout enabled on the
target. No built-in OS variants are included.

For standard Swedish Windows targets, use the complete
[se105-windows.yaml](examples/layouts/se105-windows.yaml) custom map:

```sh
autopaws-kvm -target-layout se105-windows -layouts-dir ./examples/layouts
```

Run this from the repository root, or copy the file into your own layout
directory and pass that directory to `-layouts-dir`. This map covers the same
154 printable characters as `se105`, plus Tab and Enter. Windows emits literal
`´` and `¨` with their dead key followed by Space; Linux/XKB's compose table uses
different sequences. The other mappings in these two files agree. The Windows
map was physically checked through `autopaws-mcp` and `autopaws-kvm`; see the
[Swedish validation record](docs/validation/swedish-keyboard-2026-11-18.json).

Further non-ASCII characters, such as German umlauts, `ß` or French `é` in their
respective layouts, can be added through custom files using their own keys or
stroke sequences. Built-ins guarantee only the character sets listed above.

## Finding and checking mappings

For Linux targets, inspect `/usr/share/X11/xkb/symbols/` and its included files.
The relevant basic layouts are `gb`, `de`, `fr` and `se`, with shared definitions
in `latin`. Shift selects level two; Right Alt usually selects level three.
Use the ISO key between Left Shift and Z (`0x64`) for UK `\` and `|`.

Test characters in a harmless text editor on the target. Check both cases,
punctuation and each dead-key sequence. Enable `-debug-keys` to inspect the emitted
CH9329 packet hex when diagnosing a mapping. Those records can expose typed
passwords, so enable them only while debugging.
