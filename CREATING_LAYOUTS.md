# Creating keyboard layouts

Keyboard layouts translate text into the physical keys expected by the target
machine. Select the target's layout, regardless of the development host's layout.
Browser keyboard events use physical key positions and do not use these text maps.

The built-in layouts are `us104`, `uk105`, `de105`, `fr105`, `jp106` and `se105`.
They cover printable ASCII, Tab and Enter. `se105` also covers `åäöÅÄÖ`.
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

## Target OS differences

The Linux French map can type `~` and grave directly. A Windows French custom
file must use the correct Windows key positions and append Space where its `~`
or grave is a dead key; adding Space to the Linux mapping alone is insufficient.
For macOS German, `@` uses Option+L rather than a PC's AltGr+Q; a custom file can
map it to `[alt, 0x0F]`. Verify mappings against the exact layout enabled on the
target. No built-in OS variants are included.

Further non-ASCII characters, such as German umlauts, `ß` or French `é`, can be
added through custom files using their own keys or stroke sequences. Built-ins
currently guarantee only the character sets listed above.

## Finding and checking mappings

For Linux targets, inspect `/usr/share/X11/xkb/symbols/` and its included files.
The relevant basic layouts are `gb`, `de`, `fr` and `se`, with shared definitions
in `latin`. Shift selects level two; Right Alt usually selects level three.
Use the ISO key between Left Shift and Z (`0x64`) for UK `\` and `|`.

Test characters in a harmless text editor on the target. Check both cases,
punctuation and each dead-key sequence. Enable `-debug-keys` to inspect the emitted
CH9329 packet hex when diagnosing a mapping. Those records can expose typed
passwords, so enable them only while debugging.
