#!/usr/bin/env bash
# Debian amd64 cloud workspace with Docker, curl, tar, and the pinned Node runtime.
set -euo pipefail
cd "$(dirname "$0")/.."
tools_dir="${S3S_TOOLS_DIR:-/workspace/.s3s-tools}"
case "${1:-}" in
  setup)
    mkdir -p "$tools_dir/gocache" "$tools_dir/gopath"
    if [[ ! -x "$tools_dir/go/bin/go" ]] || [[ "$("$tools_dir/go/bin/go" version)" != "go version go1.27.4 linux/amd64" ]]; then
      temp=$(mktemp -d)
      trap 'rm -rf "$temp"' EXIT
      curl --fail --location --silent --show-error https://dl.google.com/go/go1.27.4.linux-amd64.tar.gz -o "$temp/go.tgz"
      echo "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840  $temp/go.tgz" | sha256sum --check
      if [[ -e "$tools_dir/go" ]]; then
        echo "Select a fresh S3S_TOOLS_DIR or move the conflicting Go installation." >&2
        exit 1
      fi
      tar -xzf "$temp/go.tgz" -C "$tools_dir"
    fi
    "$tools_dir/go/bin/go" version
    GOPATH="$tools_dir/gopath" GOCACHE="$tools_dir/gocache" GOTOOLCHAIN=local \
      GOPROXY="https://proxy.golang.org|direct" "$tools_dir/go/bin/go" mod download
    docker build -t s3s-m0-linux:local -f scripts/Dockerfile.linux scripts
    ;;
  run)
    shift
    [[ $# -gt 0 ]] || { echo "Supply a command to run." >&2; exit 2; }
    exec docker run --rm --user "$(id -u):$(id -g)" \
      -v "$PWD:/src" -w /src -v "$tools_dir:/tools" \
      -v /etc/ssl/certs/ca-certificates.crt:/etc/ssl/certs/ca-certificates.crt:ro \
      -e GOTOOLCHAIN=local -e CGO_ENABLED=1 -e GOCACHE=/tools/gocache \
      -e GOPATH=/tools/gopath -e PATH=/tools/go/bin:/usr/local/bin:/usr/bin:/bin \
      s3s-m0-linux:local "$@"
    ;;
  *) echo "Usage: scripts/cloud-linux.sh setup | run COMMAND [ARGS...]" >&2; exit 2 ;;
esac
