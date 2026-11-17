# Layout fixture provenance

`python_layouts.json` records every `build_char_map` entry for US104 and JP106
(97 each). `python_keycodes.json` records the modifier and special-key aliases,
plus reference hex-lookup behavior. They were exported from
[serial-hid-kvm revision a66c6e8b2d936717fcd543319ecd339ca43ec235](https://github.com/linkdata/serial-hid-kvm/tree/a66c6e8b2d936717fcd543319ecd339ca43ec235),
using its `src/serial_hid_kvm/hid_keycodes.py` and built-in layout files.
Go intentionally rejects Python's permissive hexadecimal underscore and trailing
whitespace forms; the local tests cover that stricter contract explicitly.

`xkb_layouts.json` contains factual character-to-stroke mappings evaluated against
installed xkeyboard-config (`xkb-data` 2.46-2) using libxkbcommon 1.13.1-1, with
`evdev` rules, `pc105` model, `basic` variants and no options. The source definitions
are `/usr/share/X11/xkb/symbols/{gb,de,fr,se,latin}`; their SHA-256 hashes are recorded
in the fixture. Every chosen sequence was checked through libxkbcommon, including
the `C.UTF-8` compose table for dead key followed by Space. No target input was
generated while exporting these fixtures.

The XKB fixtures cover all 95 printable ASCII characters for UK, German and French,
and those characters plus `åäöÅÄÖ` for Swedish. Tab and LF inherit the US104 base.
Physical XKB key names and HID usages are recorded as generation inputs. Where
XKB offers several ways to type a character, the selected mapping prefers fewer
strokes, then no modifier, Shift, Right Alt and Shift+Right Alt. Equivalent choices
can be valid; changing a fixture requires checking the replacement against XKB.

These files are committed test data. Go tests do not need Python, XKB or
libxkbcommon installed.
