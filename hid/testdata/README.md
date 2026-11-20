# HID test fixtures

| File | Coverage |
|---|---|
| `python_layouts.json` | US104 and JP106 character maps, 97 entries each |
| `python_keycodes.json` | Modifier/special-key aliases and hexadecimal lookup |
| `python_tokenizer.json` | 34 tokenizer examples and 15 tag-validity cases |
| `keyboard_actions.json` | Ten keyboard operations with frames and emission times |
| `mouse_actions.json` | 44 mouse cases with frames, delays and held-button state |
| `mouse_coordinates.json` | 21 coordinate boundary cases |
| `browser_codes.json` | 104 physical `KeyboardEvent.code` mappings and eight modifier bits |
| `xkb_layouts.json` | UK, German, French and Swedish character-to-stroke mappings |
| `windows_swedish_layout.json` | Swedish Windows character-to-stroke mappings |

Tests use the committed files without external layout tools. Keyboard timing
checks use `testing/synctest`.

## XKB layouts

Mappings were evaluated with xkeyboard-config (`xkb-data` 2.46-2), libxkbcommon
1.13.1-1, `evdev` rules, `pc105` model, `basic` variants and no options. The
fixture records hashes for `/usr/share/X11/xkb/symbols/{gb,de,fr,se,latin}`,
physical key names and HID usages. Dead-key/Space sequences use the `C.UTF-8`
compose table. Generation sends no target input.

UK, German and French cover all 95 printable ASCII characters. Swedish covers
154 characters: ASCII, `åäöÅÄÖ`, eight further symbols and 45 composed letters.
Tab and LF inherit US104. Mapping choices prefer fewer strokes, then no modifier,
Shift, Right Alt and Shift+Right Alt.

## Windows Swedish layout

`windows_swedish_layout.json` records the same 154-character inventory from
Microsoft's [standard Swedish layout](https://learn.microsoft.com/en-us/globalization/keyboards/kbdsw.html).
It includes the reference URL and source digest and checks the complete custom
file in `examples/layouts/se105-windows.yaml`. Custom files start from US104 and
must include all ASCII overrides.

These characters use different sequences on Linux and Windows:

| Character | Linux/XKB built-in | Windows custom file |
|---|---|---|
| `´` | AltGr + HID `0x31` | HID `0x2E`, then Space |
| `¨` | HID `0x30` twice | HID `0x30`, then Space |

On the tested Linux compose table, the Windows sequences produce ASCII apostrophe
and double quote. These are public-reference fixtures; physical target results
are recorded separately.
