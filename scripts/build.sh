#!/usr/bin/env bash
# Package a production build for the current native host after scripts/check.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
platform=$(go env GOHOSTOS)
arch=$(go env GOHOSTARCH)
revision=${GITHUB_SHA:-$(git rev-parse HEAD)}
[[ "$revision" =~ ^[[:xdigit:]]{40}$ ]] || { echo "Expected a full Git commit SHA" >&2; exit 1; }
[[ "$(go env GOOS)/$(go env GOARCH)" == "$platform/$arch" ]]
[[ "$(go env CGO_ENABLED)" == 1 ]]
test -s frontend/dist/index.html
mkdir -p dist
archive="s3s-$platform-$arch-$revision"
# Local iterations can contain uncommitted source. Keep the clean SHA archive intact.
if [[ -z "${GITHUB_SHA:-}" && -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  archive="$archive-dirty"
fi
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
flags=(-mod=readonly -tags production -trimpath)
verify_binary() {
  local info
  info=$(go version -m "$1")
  grep -Fq "GOOS=$platform" <<< "$info"
  grep -Fq "GOARCH=$arch" <<< "$info"
}
case "$platform" in
  windows)
    go build "${flags[@]}" -ldflags="-H windowsgui" -o "$stage/S3S.exe" .
    verify_binary "$stage/S3S.exe"
    python -m zipfile -c "dist/$archive.zip" "$stage/S3S.exe"
    ;;
  darwin)
    bundle="$stage/S3S.app"
    mkdir -p "$bundle/Contents/MacOS"
    cp scripts/Info.plist "$bundle/Contents/"
    plutil -lint "$bundle/Contents/Info.plist"
    MACOSX_DEPLOYMENT_TARGET=15.0 go build "${flags[@]}" -o "$bundle/Contents/MacOS/S3S" .
    verify_binary "$bundle/Contents/MacOS/S3S"
    codesign --force --sign - "$bundle"
    codesign --verify --strict "$bundle"
    tar -czf "dist/$archive.tar.gz" -C "$stage" S3S.app
    ;;
  linux)
    go build "${flags[@]}" -o "$stage/S3S" .
    verify_binary "$stage/S3S"
    tar -czf "dist/$archive.tar.gz" -C "$stage" S3S
    ;;
  *) echo "Unsupported platform: $platform" >&2; exit 1 ;;
esac
