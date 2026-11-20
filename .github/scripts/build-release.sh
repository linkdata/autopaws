#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

version=${1:?Usage: build-release.sh VERSION [NEW_OUTPUT_DIRECTORY]}
output=${2:-dist}
if [[ -e "$output" ]]; then
  echo "Output directory must not already exist: $output" >&2
  exit 1
fi
mkdir -p "$output"
output=$(cd "$output" && pwd)

for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${platform%/*}
  arch=${platform#*/}
  commands=(autopaws-mcp)
  if [[ "$os" == linux || "$os" == darwin ]]; then
    commands+=(autopaws-kvm)
  fi
  suffix=
  if [[ "$os" == windows ]]; then
    suffix=.exe
  fi
  for command in "${commands[@]}"; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
      -ldflags="-s -w -X main.version=$version" \
      -o "$output/${command}_${version}_${os}_${arch}${suffix}" "./cmd/$command"
  done
done

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
for platform in linux/amd64 linux/arm64 darwin/arm64; do
  os=${platform%/*}
  arch=${platform#*/}
  for command in autopaws-kvm autopaws-mcp; do
    cp "$output/${command}_${version}_${os}_${arch}" "$staging/$command"
  done
  (
    cd "$staging"
    zip -q -X "$output/autopaws_${version}_${os}_${arch}.zip" autopaws-kvm autopaws-mcp
  )
done

(
  cd "$output"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum autopaws* > SHA256SUMS
  else
    shasum -a 256 autopaws* > SHA256SUMS
  fi
)
