# v0.5.0

The Tesseract OCR tools `get_screen_text` and `execute_and_read` now accept an
optional `region: {x, y, width, height}` in screenshot pixels. Each call captures
a fresh frame, then crops before OCR preprocessing. Omitting `region` keeps
full-frame recognition.

Both tools also accept `language`, defaulting to English (`eng`). Combinations
such as `eng+swe` use the requested models installed on the MCP host. Missing
models return an error, including when Tesseract otherwise succeeds after
dropping one language from a combination.

OCR results remain plain text and do not establish mouse coordinates. Geometry
and language syntax are checked before command input; bounds and model errors
can occur after `execute_and_read` has typed its command.

Validation includes the complete local CI gate, supported-platform cross-builds,
physical Mac OCR-region checks, and real English/Swedish model tests.

See [MCP usage and configuration](https://github.com/linkdata/autopaws/blob/v0.5.0/README.md#mcp-server).
