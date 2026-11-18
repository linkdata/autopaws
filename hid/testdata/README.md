# Layout fixture provenance

`python_layouts.json` records every `build_char_map` entry for US104 and JP106
(97 each). `python_keycodes.json` records the modifier and special-key aliases,
plus reference hex-lookup behavior. They were exported from
[serial-hid-kvm revision a66c6e8b2d936717fcd543319ecd339ca43ec235](https://github.com/linkdata/serial-hid-kvm/tree/a66c6e8b2d936717fcd543319ecd339ca43ec235),
using its `src/serial_hid_kvm/hid_keycodes.py` and built-in layout files.
Go intentionally rejects Python's permissive hexadecimal underscore and trailing
whitespace forms; the local tests cover that stricter contract explicitly.

`python_tokenizer.json` records 34 tokenizer examples and 15 tag-validity calls
from that revision's `hid_keyboard.py` and `tests/test_hid_keyboard.py`, including
the docstring examples and added Unicode, brace and control-character cases.
Python represents a tag with a U+0001 prefix; Go uses a separate token kind so a
literal U+0001 remains literal.

`keyboard_actions.json` records ten successful keyboard operations from
the same revision. A recording device expanded each keyboard send into its real
encoder's press/release frames; sleeps were recorded rather than performed.
Tests compare frames and emission times under `testing/synctest`, including
delays after the final character or sequence step. Python's partial typing on
invalid input is deliberately superseded by whole-request validation.

`mouse_actions.json` records 44 Python mouse cases from that revision, including
frames, delays and held-button state. Where coordinates matter, the 4096×4096
reference screen makes the Python and Go formulas identical. Go rejects unknown
buttons and out-of-range relative motion, uses one optional position rather than
partial coordinates, and keeps Click's wire and stored button state consistent.
Python-only cases exhibiting those superseded behaviors are excluded.

`mouse_coordinates.json` records 21 boundary comparisons for Go's deliberate
coordinate correction: round each valid screen axis onto HID 0…4095 using the
last valid pixel as the denominator; a one-pixel axis maps to zero. The fixture
also records Python's different results for context. Tests check the Go results
and rejection decisions, with additional exact-rounding and extreme-int cases.

`xkb_layouts.json` contains factual character-to-stroke mappings evaluated against
installed xkeyboard-config (`xkb-data` 2.46-2) using libxkbcommon 1.13.1-1, with
`evdev` rules, `pc105` model, `basic` variants and no options. The source definitions
are `/usr/share/X11/xkb/symbols/{gb,de,fr,se,latin}`; their SHA-256 hashes are recorded
in the fixture. Every chosen sequence was checked through libxkbcommon, including
the `C.UTF-8` compose table for dead key followed by Space. No target input was
generated while exporting these fixtures.

The XKB fixtures cover all 95 printable ASCII characters for UK, German and French.
Swedish covers 154 printable characters: ASCII, `åäöÅÄÖ`, eight further literal
symbols and 45 additional composed letters. This is the character inventory of
Microsoft's [standard Swedish layout](https://learn.microsoft.com/en-us/globalization/keyboards/kbdsw.html),
with every selected sequence independently evaluated against Linux/XKB. Tab and
LF inherit the US104 base.
Physical XKB key names and HID usages are recorded as generation inputs. Where
XKB offers several ways to type a character, the selected mapping prefers fewer
strokes, then no modifier, Shift, Right Alt and Shift+Right Alt. Equivalent choices
can be valid; changing a fixture requires checking the replacement against XKB.

`windows_swedish_layout.json` independently records the same 154-character
inventory from Microsoft's normal, Shift, AltGr and dead-key tables. It checks
the complete custom file in `examples/layouts/se105-windows.yaml`, including all
ASCII overrides; a user file starts from US104 rather than inheriting `se105`.
Its original 101 ASCII/native-letter mappings match the Linux map. Of the 53
additions, only literal acute (`´`) and diaeresis (`¨`) need different sequences:

| Character | Linux/XKB built-in | Windows custom file |
|---|---|---|
| `´` | AltGr + HID `0x31` | HID `0x2E`, then Space |
| `¨` | HID `0x30` twice | HID `0x30`, then Space |

On the tested Linux compose table, the Windows dead-key/Space sequences instead
produce ASCII apostrophe and double quote. The fixture records the Microsoft
reference URL and its source digest; physical target checks are separate from
this public-reference fixture generation.

These files are committed test data. Go tests do not need Python, XKB or
libxkbcommon installed.

`browser_codes.json` records all 104 physical `KeyboardEvent.code` mappings and
eight modifier bits from the same pinned revision's `_web_viewer.py`. It covers
the complete mapping tested by `tests/test_web_viewer_keycodes.py`; no display or
Python import is needed by the Go tests. Browser input regressions also preserve
the web-specific packet sequences from `tests/test_viewer_input.py`, with two
deliberate corrections: movement carries held button bits in both modes to match
the physically verified Go Mouse behavior, and scroll preserves held buttons in
absolute mode too. Deterministic six-key press order, whole shortcut actions and
release-only disconnect cleanup are new Go behavior covered by direct tests.
