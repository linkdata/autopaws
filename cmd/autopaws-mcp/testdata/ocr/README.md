# Synthetic OCR corpus

Four generated 720×140 JPEGs cover Linux console, dark terminal, light terminal
and PowerShell-style blue. They contain fixed text rendered in Liberation Mono
Regular at 24 px. They are not target screenshots; the PowerShell style does not
establish Windows runtime support. No font binary is included.

`provenance.json` records source/font hashes, Pillow and Tesseract versions.
Lossless PGM baselines let tests compare preprocessing with the same installed
Tesseract. `unsupported-cmyk.jpg` is a synthetic blank 8×8 CMYK JPEG for the
unsupported-color-model error path.

From the repository root, measure case-sensitive Unicode Levenshtein edits,
including spaces and newlines:

```sh
go test ./cmd/autopaws-mcp -run '^TestOCRCorpus$' -v -ocr-report testdata/ocr/accuracy.json
```

The test skips without Tesseract and requires no more edits than the PGM baseline
for each image. It needs no image-generation dependencies.

Recorded with Tesseract 5.5.0 / Leptonica 1.86.0:

| Fixture | Expected characters | Edits |
|---|---:|---:|
| Linux console | 66 | 0 |
| Dark terminal | 67 | 1 |
| Light terminal | 45 | 0 |
| PowerShell-style | 63 | 0 |
| Total | 241 | 1 |

Both preprocessing paths recognized 240/241 characters (99.6%). The shared error
is `agent@Linux` for expected `agent@linux`. This small corpus does not measure
actual target screens, arbitrary fonts, other languages or platform runtime
behavior.
