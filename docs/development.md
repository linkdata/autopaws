# Development

Build requirements: Go 1.27.4+, Node.js 26.13.1, the golangci-lint version
pinned in [CI](../.github/workflows/ci.yml), actionlint, ShellCheck,
FFmpeg, zip/unzip and a C compiler for race tests. Tesseract is optional;
the real OCR corpus test skips when it is absent.

## Local CI gate

Run this gate before submitting changes.
It runs native checks on the development host, cross-builds all supported
platforms and verifies release assets. Use Bash; on Windows, use Git Bash or
MSYS2 with native Go, Node.js and a compatible MinGW-w64 GCC on `PATH`.

```bash
(
  set -euo pipefail
  GOOS=$(go env GOHOSTOS)
  GOARCH=$(go env GOHOSTARCH)
  export GOOS GOARCH
  autopaws_checks=$(mktemp -d)
  trap 'rm -r "$autopaws_checks"' EXIT
  command -v ffmpeg >/dev/null
  export AUTOPAWS_FFMPEG_TEST=1
  actionlint
  bash -n .github/scripts/build-release.sh
  shellcheck .github/scripts/build-release.sh
  node --check web/assets/viewer-helpers.mjs
  node --check web/assets/viewer.mjs
  node --check web/assets/mapping.mjs
  node --check web/assets/sw.js
  node --check web/assets/audio-playback.mjs
  node --check web/assets/audio-worklet.mjs
  node --test web/*.test.mjs
  CGO_ENABLED=0 go build ./...
  golangci-lint run
  CGO_ENABLED=1 go test -race -coverprofile=coverage.out ./...
  go tool cover -func=coverage.out
  go tool cover -html=coverage.out -o=coveragereport.html.out
  for autopaws_platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    CGO_ENABLED=0 GOOS="${autopaws_platform%/*}" GOARCH="${autopaws_platform#*/}" go build ./...
  done
  bash .github/scripts/build-release.sh ci "$autopaws_checks/dist"
  (
    cd "$autopaws_checks/dist"
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum --check SHA256SUMS
    else
      shasum -a 256 --check SHA256SUMS
    fi
  )
  for archive in "$autopaws_checks"/dist/*.zip; do unzip -tq "$archive"; done
)
```

CI runs native Linux, macOS and Windows tests with generated media fixtures.
Require all applicable jobs to pass before merging. Cross-builds check compilation,
not another OS's runtime behavior. Physical KVM checks run locally with hardware;
see [hardware validation](hardware-validation.md) for coverage.
