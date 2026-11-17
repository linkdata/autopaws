# Synthetic OCR corpus

These are controlled, generated text images, not screenshots of a user's or a
test target's desktop. The four visual styles are Linux console, dark terminal,
light terminal, and PowerShell-style blue. The PowerShell style does **not** claim
Windows execution or Windows OCR validation. All text is fixed in generate.py;
there are no credentials, target data, downloaded screenshots, or embedded fonts.

`generate.py` renders Liberation Mono Regular at 24 px using Pillow, saves 720×140
quality 95 JPEGs, then imports the original Python `TerminalOCR` implementation.
It records both the original Python OCR output and a lossless PGM of its exact
preprocessed pixels. The PGM baselines let Go tests run the same installed
Tesseract against both preprocessing paths without requiring Python, Pillow or
pytesseract in CI. `unsupported-cmyk.jpg` is a separate synthetic blank 8×8 CMYK
JPEG for the supported-color-model error path, generated with Pillow 12.1.1.

`provenance.json` records font/source hashes, Pillow version and Tesseract version
for reproducibility. The font is Liberation Mono Regular, a system-installed
Liberation font under the SIL Open Font License; only rendered glyph images are
included. No font binary is copied into the repository.

Regenerate the four corpus images/baselines with a Python environment containing
Pillow and pytesseract:

```sh
python generate.py --reference /path/to/mcp-serial-hid-kvm/src/mcp_serial_hid_kvm/ocr.py --tesseract /path/to/tesseract
```

The default font path is a Linux installation path; use `--font` elsewhere. The
reference source is used directly, rather than reimplementing its preprocessing
in the generator. Its postprocessor is the exact port covered by Go table tests.

From the repository root, remeasure with:

```sh
go test ./cmd/autopaws-mcp -run '^TestOCRCorpus$' -v -ocr-report testdata/ocr/accuracy.json
```

The test skips when Tesseract cannot be found. It computes case-sensitive Unicode
Levenshtein character edits against the fixed expected text, including spaces and
newlines, and requires Go to have no more edits than the original Python
preprocessing on each image. It runs both paths with the currently installed
Tesseract, so an engine upgrade does not compare one engine against an older one.
The committed original-Python outputs were also identical to the PGM-baseline
outputs during the recorded run.

Recorded with Tesseract 5.5.0 / Leptonica 1.86.0:

| Fixture | Expected characters | Go edits | Python edits |
|---|---:|---:|---:|
| Linux console | 66 | 0 | 0 |
| Dark terminal | 67 | 1 | 1 |
| Light terminal | 45 | 0 | 0 |
| PowerShell-style | 63 | 0 | 0 |
| Total | 241 | 1 | 1 |

Both paths recognized 240/241 characters (99.6% by this metric). The shared error
is `agent@Linux` for expected `agent@linux`. This small, clear synthetic corpus
supports the minimal preprocessing choice; it does not measure actual target
screens, arbitrary fonts, every terminal theme, other languages or Windows/macOS
runtime behavior. Extended field screenshots can be added when available.
