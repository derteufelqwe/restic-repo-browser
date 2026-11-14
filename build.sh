#!/usr/bin/env sh
# Builds restic-browser for every release platform into ./build.
# Usage: ./build.sh [version]      (default version: "dev")
#        TARGETS="linux/amd64" ./build.sh 1.2.3     (build only some platforms)
set -eu
cd "$(dirname "$0")"

version="${1:-dev}"
targets="${TARGETS:-linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64}"
out=build

rm -rf "$out"
mkdir -p "$out"
for target in $targets; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  [ "$os" = windows ] && ext=".exe"
  file="$out/restic-browser-$os-$arch$ext"
  echo "building $file"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "-s -w -X main.releaseVersion=$version" -o "$file" ./cmd/restic-browser
done
echo "done: $out/"
