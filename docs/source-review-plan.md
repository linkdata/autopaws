# Source review plan

Status: all 180 source files reviewed; findings fixed; Linux validation and two
consecutive clean reviews passed, ending with Claude's final critique. The owner
accepted the shared changes without native macOS retesting; macOS cross-builds passed.

Snapshot: `7f2957467be5`, 2026-11-20. The working tree was clean when inventoried.
Scope: **180 source files**, including **97 test files**, totaling 32,398 lines:
166 Go, seven MJS, one JS, one HTML, one CSS, one Python, two C and one shell.
Supporting layouts, manifests, fixtures, workflows and configuration are checked
with their consumers. Security defect review is outside this porting-stage scope,
as specified in [AGENTS.md](../AGENTS.md).

## Approach

1. At execution time, record the commit and working-tree changes, then refresh
   the tracked and untracked source inventory. Add any new source files to the
   checklist; do not let ignored build output inflate the scope. Check installed
   tool versions against the README and CI before running validation.
2. Work through the batches below in dependency order. Read every implementation,
   platform alternative and test file; review the tests themselves for meaningful
   assertions, cancellation/cleanup coverage and misleading mocks or skips.
   Check fixtures against their generating source and documented contracts.
3. For each file, trace callers, consumers, exported behavior and affected fixtures.
   Check success, invalid input, boundary values, errors, cancellation, recovery
   and resource ownership where relevant. Across files, check units, framing,
   state transitions, generations, lock ordering and platform assumptions.
4. Apply the ponytail filter: look first for deletion, redundant state/branches,
   duplicate validation, unnecessary wrappers/conversions and avoidable custom
   code. Recommend only concrete simplifications that preserve behavior and make
   the affected flow easier to read. Keep boundary validation, error handling,
   hardware calibration/pacing and existing package boundaries. Do not add
   dependencies, speculative abstractions or unrelated features.
5. Record findings with `path:line`, a concrete failure or needless complexity,
   the affected caller/contract, the smallest correction and useful verification.
   A checklist box means the file was read and its outcome recorded; it does not
   imply that its findings are resolved. Keep findings in a short ledger with
   `ID | file:line | defect/simplification | evidence | proposed fix | status`.
   Record a clean batch explicitly when there are no findings.
6. When fixing findings, fix the shared cause and follow it through
   callers, tests, fixtures and documentation. Give non-trivial logic one focused
   regression check; avoid tests that merely repeat the implementation and avoid
   adding tests for trivial edits. Run the relevant existing checks after each
   coherent fix.

## Review batches

Each file has exactly one checklist entry. Review companion tests and contracts
alongside the implementation even when the alphabetical checklist differs from
reading order. Independent batches can be delegated once their shared contracts
are understood; keep fixes to a shared file under one owner's control.

### 1. Foundations (9 files)

Frame bytes and checksums, wire limits and error identities, clock domains/units and fallback behavior, token loading and matching semantics.

- [x] `apiwire/wire.go`
- [x] `apiwire/wire_test.go`
- [x] `ch9329/frame.go`
- [x] `ch9329/frame_test.go`
- [x] `internal/monotime/clock_other.go`
- [x] `internal/monotime/clock_unix.go`
- [x] `internal/monotime/clock_unix_test.go`
- [x] `internal/token/token.go`
- [x] `internal/token/token_test.go`

### 2. Serial transport (14 files)

Port discovery, Linux/Darwin termios and ioctl behavior, partial writes, pacing, cancellation, preparation generations, reconnect and descriptor ownership through close.

- [x] `serial/discover.go`
- [x] `serial/discover_darwin.go`
- [x] `serial/discover_darwin_test.go`
- [x] `serial/discover_linux.go`
- [x] `serial/discover_linux_test.go`
- [x] `serial/hid_test.go`
- [x] `serial/port.go`
- [x] `serial/port_discover_linux_test.go`
- [x] `serial/port_test.go`
- [x] `serial/tty.go`
- [x] `serial/tty_darwin.go`
- [x] `serial/tty_darwin_test.go`
- [x] `serial/tty_linux.go`
- [x] `serial/tty_linux_test.go`

### 3. HID actions and layouts (15 files)

Keycode/browser mappings, tokenizer escapes, layout parsing/selection, whole-request validation, serialized actions, held-input cleanup, mouse modes and coordinate endpoints. Preserve hardware delays.

- [x] `hid/browser.go`
- [x] `hid/browser_codes.go`
- [x] `hid/browser_test.go`
- [x] `hid/controller.go`
- [x] `hid/controller_test.go`
- [x] `hid/keyboard.go`
- [x] `hid/keyboard_test.go`
- [x] `hid/keycodes.go`
- [x] `hid/layout.go`
- [x] `hid/layout_selection_test.go`
- [x] `hid/layout_test.go`
- [x] `hid/mouse.go`
- [x] `hid/mouse_test.go`
- [x] `hid/tokenizer.go`
- [x] `hid/tokenizer_test.go`

### 4. Linux video (11 files)

C/Go ABI agreement, read-only probes, format negotiation, mmap/buffer lifetime, timestamps, activation/cancellation, dequeue/requeue failures and close ownership.

- [x] `v4l2/abi_linux.go`
- [x] `v4l2/abi_linux_test.go`
- [x] `v4l2/activation_linux_test.go`
- [x] `v4l2/device_linux.go`
- [x] `v4l2/device_linux_test.go`
- [x] `v4l2/probe_linux.go`
- [x] `v4l2/probe_linux_test.go`
- [x] `v4l2/sys_linux.go`
- [x] `v4l2/sys_linux_test.go`
- [x] `v4l2/testdata/sz.c`
- [x] `v4l2/types.go`

### 5. Linux audio (17 files)

C/Go ABI agreement, PCM negotiation and arithmetic, kernel-call ownership, DROP/PREPARE sequencing, recovery progress, generations, timestamp math, bounded/expiring queues and subscriber shutdown.

- [x] `alsa/abi_assert_linux.go`
- [x] `alsa/abi_linux.go`
- [x] `alsa/abi_linux_test.go`
- [x] `alsa/capture_linux.go`
- [x] `alsa/capture_linux_test.go`
- [x] `alsa/expiry_linux_test.go`
- [x] `alsa/hardware_linux_test.go`
- [x] `alsa/kernel_linux.go`
- [x] `alsa/kernel_linux_test.go`
- [x] `alsa/lifecycle_linux_test.go`
- [x] `alsa/params_linux.go`
- [x] `alsa/params_linux_test.go`
- [x] `alsa/queue_linux_test.go`
- [x] `alsa/ready_linux_test.go`
- [x] `alsa/recovery_progress_linux_test.go`
- [x] `alsa/testdata/sz.c`
- [x] `alsa/types.go`

### 6. macOS media (8 files)

FFmpeg device parsing, command arguments, startup/stall handling, pipe framing, process kill/reap, negotiated media, queue age and monotonic timestamps. Compare shared Linux-facing types without forcing common wrappers.

- [x] `avfoundation/audio.go`
- [x] `avfoundation/audio_test.go`
- [x] `avfoundation/devices.go`
- [x] `avfoundation/devices_test.go`
- [x] `avfoundation/process.go`
- [x] `avfoundation/process_test.go`
- [x] `avfoundation/video.go`
- [x] `avfoundation/video_test.go`

### 7. Discovery and shared capture (18 files)

Stable selectors and USB audio/video pairing, read-only enumeration, platform source selection, manager ownership, fresh snapshots, crop/scale metadata, subscriptions, generations and reconnect behavior.

- [x] `capture/crop.go`
- [x] `capture/crop_test.go`
- [x] `capture/events_test.go`
- [x] `capture/frame.go`
- [x] `capture/frame_test.go`
- [x] `capture/manager.go`
- [x] `capture/manager_lifecycle_test.go`
- [x] `capture/manager_test.go`
- [x] `capture/mjpeg.go`
- [x] `capture/mjpeg_test.go`
- [x] `capture/recovery_test.go`
- [x] `capture/source_darwin.go`
- [x] `capture/source_linux.go`
- [x] `devices/discover_darwin.go`
- [x] `devices/discover_linux.go`
- [x] `devices/discover_linux_test.go`
- [x] `devices/list.go`
- [x] `devices/types.go`

### 8. Configuration (5 files)

CLI/YAML/default precedence, unknown or invalid settings, explicit zero/empty values, action validation, early help/version/list behavior and logging setup. Follow settings through both commands.

- [x] `internal/config/actions_test.go`
- [x] `internal/config/config.go`
- [x] `internal/config/config_test.go`
- [x] `internal/config/parse.go`
- [x] `internal/config/token_test.go`

### 9. Daemon API and portable client (13 files)

Protocol/method agreement, framing, numeric validation and error propagation, request cancellation/disconnect, concurrent calls, capture/layout state changes and client cleanup.

- [x] `api/auth_test.go`
- [x] `api/capture.go`
- [x] `api/capture_integration_linux_test.go`
- [x] `api/errors.go`
- [x] `api/integration_test.go`
- [x] `api/layout_test.go`
- [x] `api/methods.go`
- [x] `api/methods_test.go`
- [x] `api/server.go`
- [x] `api/server_test.go`
- [x] `apiclient/client.go`
- [x] `apiclient/client_test.go`
- [x] `apiclient/token_test.go`

### 10. Web server and media transport (19 files)

HTTP/WebSocket lifecycle, input ownership/release, one ordered media writer, deadlines/backpressure, audio activation/retry, format/generation changes and shutdown. Match protocol bytes to browser consumers.

- [x] `web/audio.go`
- [x] `web/audio_darwin.go`
- [x] `web/audio_fixture_test.go`
- [x] `web/audio_lifecycle_test.go`
- [x] `web/audio_linux.go`
- [x] `web/audio_logging_test.go`
- [x] `web/audio_resolve_linux_test.go`
- [x] `web/audio_retry_darwin_test.go`
- [x] `web/audio_retry_test.go`
- [x] `web/audio_server_test.go`
- [x] `web/audio_stream_test.go`
- [x] `web/auth_test.go`
- [x] `web/fixtures_test.go`
- [x] `web/input.go`
- [x] `web/protocol.go`
- [x] `web/protocol_test.go`
- [x] `web/server.go`
- [x] `web/server_test.go`
- [x] `web/stream.go`

### 11. Browser viewer (10 files)

HTML/CSS behavior and accessibility, actual page-module behavior, keyboard/pointer focus and release, image-coordinate mapping, reconnect/stale-frame races, service-worker caching, AudioWorklet queues and clock/generation changes. Check that test harnesses represent relevant browser behavior.

- [x] `web/assets/audio-playback.mjs`
- [x] `web/assets/audio-worklet.mjs`
- [x] `web/assets/index.html`
- [x] `web/assets/sw.js`
- [x] `web/assets/viewer-helpers.mjs`
- [x] `web/assets/viewer.css`
- [x] `web/assets/viewer.mjs`
- [x] `web/audio-helpers.test.mjs`
- [x] `web/viewer-helpers.test.mjs`
- [x] `web/viewer-integration.test.mjs`

### 12. MCP command and OCR tooling (26 files)

Tool schemas/goldens, API forwarding and cancellation, screenshot coordinate establishment/invalidation, capture-log cleanup, OCR resolution/preprocessing/process lifetime and corpus provenance. Read the Python generator against its retained fixtures.

- [x] `cmd/autopaws-mcp/api_integration_linux_test.go`
- [x] `cmd/autopaws-mcp/capture.go`
- [x] `cmd/autopaws-mcp/capture_test.go`
- [x] `cmd/autopaws-mcp/capturelog.go`
- [x] `cmd/autopaws-mcp/capturelog_linux_test.go`
- [x] `cmd/autopaws-mcp/capturelog_test.go`
- [x] `cmd/autopaws-mcp/layout_integration_linux_test.go`
- [x] `cmd/autopaws-mcp/main.go`
- [x] `cmd/autopaws-mcp/mcp_integration_test.go`
- [x] `cmd/autopaws-mcp/ocr.go`
- [x] `cmd/autopaws-mcp/ocr_corpus_test.go`
- [x] `cmd/autopaws-mcp/ocr_integration_test.go`
- [x] `cmd/autopaws-mcp/ocr_path.go`
- [x] `cmd/autopaws-mcp/ocr_path_test.go`
- [x] `cmd/autopaws-mcp/ocr_test.go`
- [x] `cmd/autopaws-mcp/ocr_tools.go`
- [x] `cmd/autopaws-mcp/ocr_tools_test.go`
- [x] `cmd/autopaws-mcp/run.go`
- [x] `cmd/autopaws-mcp/run_test.go`
- [x] `cmd/autopaws-mcp/screen.go`
- [x] `cmd/autopaws-mcp/screen_test.go`
- [x] `cmd/autopaws-mcp/testdata/ocr/generate.py`
- [x] `cmd/autopaws-mcp/token_integration_linux_test.go`
- [x] `cmd/autopaws-mcp/token_test.go`
- [x] `cmd/autopaws-mcp/tools.go`
- [x] `cmd/autopaws-mcp/tools_test.go`

### 13. KVM command assembly (14 files)

Flag exit codes/defaults, help/version/list without hardware, component wiring and startup rollback, serial/capture/audio selection, layout changes and ordered shutdown.

- [x] `cmd/autopaws-kvm/ffmpeg_darwin_test.go`
- [x] `cmd/autopaws-kvm/list.go`
- [x] `cmd/autopaws-kvm/list_linux_test.go`
- [x] `cmd/autopaws-kvm/main.go`
- [x] `cmd/autopaws-kvm/run.go`
- [x] `cmd/autopaws-kvm/run_test.go`
- [x] `cmd/autopaws-kvm/serve.go`
- [x] `cmd/autopaws-kvm/serve_audio_test.go`
- [x] `cmd/autopaws-kvm/serve_capture_test.go`
- [x] `cmd/autopaws-kvm/serve_layout_test.go`
- [x] `cmd/autopaws-kvm/serve_test.go`
- [x] `cmd/autopaws-kvm/serve_web_shutdown_test.go`
- [x] `cmd/autopaws-kvm/serve_web_test.go`
- [x] `cmd/autopaws-kvm/token_test.go`

### 14. Release tooling (1 file)

All ten platform artifacts, static Linux builds, version injection, output/checksum correctness, failure propagation and rerun behavior. Check workflow and README agreement without publishing anything.

- [x] `.github/scripts/build-release.sh`

## Companion contracts and data

These are supporting inputs, rather than additional source-file counts. Review
all relevant entries with the source above; preserve provenance when updating a
baseline and explain every changed golden rather than accepting it blindly.

- [x] `go.mod`, `go.sum`: module/platform constraints and actual dependency use.
- [x] `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.golangci.yml`:
  local-gate agreement, pinned tools, platform matrix and release prerequisites.
- [x] `hid/layouts/*.yaml`, `examples/layouts/*.yaml`, `hid/testdata/*`,
  `ch9329/testdata/*`: all built-in/custom map behavior, key tables, action and
  coordinate fixtures, and retained Python/XKB reference evidence.
- [x] `alsa/testdata/abi-linux-amd64.txt`, `v4l2/testdata/abi-linux-amd64.txt`
  and their READMEs: ABI probe output and architecture limits.
- [x] `devices/testdata/*`: discovery output and selection expectations.
- [x] `cmd/autopaws-mcp/testdata/*.json`, `cmd/autopaws-mcp/testdata/ocr/*`:
  schemas/goldens, synthetic image/PGM baselines, expected text, accuracy and provenance.
- [x] `web/assets/manifest.json`, `web/assets/icon.svg`: asset references, browser
  installation and service-worker cache agreement.
- [x] `examples/autopaws.yaml`, `examples/systemd/autopaws-kvm.service`,
  `examples/udev/70-autopaws-serial.rules`: installed paths, settings and startup behavior.
- [x] `README.md`, `CREATING_LAYOUTS.md`, `api/PROTOCOL.md`, `web/PROTOCOL.md`,
  `alsa/README.md`, `docs/*.md`, `docs/validation/*.json`: behavior/limits promised
  by the source and the evidence behind platform and hardware claims.

## Verification after any resulting source changes

Use the [README development gate](../README.md#development) as the executable
source of truth. Do not substitute a smaller selection for its final gate.

- Install the documented local tooling, including the pinned golangci-lint,
  actionlint and ShellCheck. Run `actionlint`,
  `bash -n .github/scripts/build-release.sh` and
  `shellcheck .github/scripts/build-release.sh` after final edits.
- Run all five browser syntax checks and `node --test web/*.test.mjs`.
- Run native builds, golangci-lint, cgo-enabled race tests with coverage, and both
  coverage report commands. Run the documented native gate on Linux and macOS;
  the macOS check also needs FFmpeg fixtures with `AUTOPAWS_FFMPEG_TEST=1`.
  Record an unavailable native host as a validation gap and leave that required
  check incomplete; a cross-build cannot establish native runtime behavior.
- Preserve all supported cross-builds: Linux and Darwin amd64/arm64 for KVM and
  MCP, plus Windows amd64/arm64 for MCP. Keep release builds free of cgo.
- Confirm optional test bodies actually ran: report skipped hardware, Tesseract
  and FFmpeg checks separately. Exercise the OCR corpus when OCR changes;
  retain its original-Python comparison and record the installed engine version.
- Use the available Linux setup for physical KVM validation of affected input,
  capture/audio, browser, cancellation/shutdown and USB recovery behavior.
  Record build identity, host/target/capture metadata, actions and actual results
  in the existing hardware-validation format. Select the intended ALSA test
  device explicitly when exercising its opt-in hardware tests. Unit tests do
  not prove driver timing or physical key/button release.
- Retain the already-passed limited Windows laptop smoke as existing evidence.
  Broader Windows/macOS target tests and ARM64 hardware tests are not required.
  If a Windows smoke is repeated, record only actions, outcomes and host/capture
  metadata: no screen contents, labels, identifiers, screenshots/OCR, target
  scripts or target file/settings changes. The owner handles target setup.

## Completion criteria

- [x] Refreshed inventory is complete; all 180 current source entries and any
  newly added files have a recorded outcome, including tests and platform variants.
- [x] Findings are resolved and rechecked, or clearly reported as unresolved;
  an unresolved correctness defect or proposed simplification prevents a clean review.
- [x] Any resulting fixes include their affected callers, tests, fixtures and docs.
- [x] After the final fixes, obtain **two consecutive clean reviews** of the
  changes and affected contracts: zero defects and zero suggested simplifications.
  Address any finding and restart the clean-review count. This final check follows
  the complete file-by-file pass; it does not replace it. Self-review throughout;
  reserve optional Claude use for the final round, in streaming mode. If Claude
  credits run out, continue self-reviewing.
- [x] After the final edits, the complete local gate and required physical checks
  pass, with optional skips explicitly reported. Re-run
  checks only when later changes or unresolved failures justify it.
- [x] Report reviewed commit, file coverage, findings/fixes, both clean-review
  outcomes, verification evidence and remaining limits before any later push or merge.

## Execution record, 2026-11-20

The complete tracked inventory at `7f2957467be5` was reviewed, including every
test, C probe, Python generator, browser asset and platform alternative. No new
source files were added. All companion groups above were checked. Security
defects remained outside this review's scope.

| Finding | Original location | Correction and verification |
|---|---|---|
| MCP capture overlapping a settings request could restore stale screenshot coordinates | `cmd/autopaws-mcp/tools.go:186` | Invalidate before and on return from both capture setters. Eight real SDK/API cases cover both methods, success/failure and capture completion before/across setter return; all eight failed before the fix. |
| Controller duplicated keyboard state that production code never read | `hid/controller.go:42` | Remove the fields, assignments and per-stroke key slice; preserve Browser's actual held state and emitted release reports. Focused HID/core race tests and physical typing/cancellation passed. |
| Audio recovery test assumed its sole new block could not race a previous dequeue | `web/audio_stream_test.go:51` | Use the existing synctest pattern and wait for the previous dequeue before recovery. Before: 9/100 failures with race/coverage; after: 1,000/1,000 passed. Production drop behavior is unchanged. |
| README counted twelve non-OCR tools after layout tools increased the count to fourteen | `README.md:547` | Remove the unnecessary numeric count. |

Batches 1–3 and 8–9 were read together with their callers/tests; the HID finding
above was their only change. Batches 4–6 were clean, including both strict C ABI
probes and byte-for-byte fixture comparisons. Batch 7 and batch 13 were clean.
Batch 10–11 found only the subsequently reproduced test scheduling defect.
Batch 12 found only the MCP overlap defect. Batch 14 and the companion contract
review found only the README count mismatch. All findings were fixed and rechecked.

Validation status:

- Focused race suites passed across all packages. The first complete Linux gate
  exposed the audio test scheduling failure above. After its fix, all 27 commands
  in the final Linux README/workflow gate passed, including actionlint, ShellCheck,
  browser checks, native builds, lint, race/coverage and every supported cross-build.
- Browser syntax/tests, module verification and `go mod tidy -diff` passed.
- Tesseract 5.5.0 corpus actually ran: Go and retained Python baselines matched,
  with 240/241 combined characters correct. Generator syntax and JSON fixtures passed.
- Physical Linux input/capture/MCP checks passed; actions, build hashes, observed
  shutdown and limits are in [the validation record](validation/source-review-2026-11-20.json).
- Native macOS checks were not run: the owner confirmed that host is unavailable
  and accepted the shared changes without native Mac retesting. No macOS-specific
  source changed; both macOS cross-builds passed. This does not claim native runtime validation.
- Broader Windows/macOS target tests and ARM64 hardware tests were not required;
  the previous limited Windows smoke remains the recorded evidence.
- Claude's first final critique found no correctness defects and one redundant
  error wrapper left in the HID test after deleting duplicate state. The callback
  now returns the write error directly. The clean-review count was restarted.
  Fresh round 1 reviewed the final source and evidence with zero defects and zero
  suggested simplifications. Claude then inspected the final diff, affected
  callers/tests and both review documents and reported: **CLEAN: no defects or
  suggested simplifications.** These are the two consecutive clean reviews
  recorded before PR creation.

