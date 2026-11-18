# v0.1.1

Swedish text entry now covers 154 printable characters, up from 101, plus Tab
and Enter. The added mappings include `£€§½¤µ`, literal accents, and letters
formed with acute, diaeresis, grave, circumflex and tilde accents. Existing
character mappings are preserved.

The built-in `se105` map follows Linux/XKB. For Swedish Windows targets, use the
complete custom `se105-windows.yaml` example with `-target-layout se105-windows`
and `-layouts-dir` pointing to its directory. Literal `´` and `¨` require different
sequences on the two systems. See the
[layout instructions](https://github.com/linkdata/autopaws/blob/v0.1.1/CREATING_LAYOUTS.md#target-os-differences).

The candidate's complete Windows character set was tested through
`autopaws-mcp` and `autopaws-kvm` into an unsaved Notepad buffer. Controlled
inputs and pass/fail are in the
[Swedish keyboard record](https://github.com/linkdata/autopaws/blob/v0.1.1/docs/validation/swedish-keyboard-2026-11-18.json).
No target files were saved, and no screenshots or OCR were retained. Linux
mappings were checked against libxkbcommon and its compose table.

This patch changes layout data, tests and documentation. The
[v0.1.0 validation baseline](https://github.com/linkdata/autopaws/blob/v0.1.0/docs/hardware-validation.md#release-validation-draft-2026-11-17)
covers the earlier media, input and recovery checks; those physical checks were
not repeated for this patch. Linux amd64 commands receive the runtime checks;
the remaining supported artifacts are cross-built, without claiming additional
physical hosts or target operating systems.

The repository remains private. Downloads require an authenticated account with
access. See the
[release procedure](https://github.com/linkdata/autopaws/blob/v0.1.1/docs/releasing.md)
for packaging and checksums.
