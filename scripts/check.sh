#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

frontend() {
  if [[ -z "${npm_config_cache:-}" && ! -w "$(npm config get cache)" && ! -w "$HOME" ]]; then
    export npm_config_cache="${TMPDIR:-/tmp}/s3s-npm-$(id -u)"
  fi
  npm --prefix frontend ci
  npm --prefix frontend test
  npm --prefix frontend run build
}
backend() {
  # Embed requires frontend/dist from the frontend check.
  go mod verify
  go vet -mod=readonly ./...
  go test -mod=readonly -count=1 -v ./...
}
case "${1:-all}" in
  frontend) frontend ;;
  go) backend ;;
  all) frontend; backend ;;
  *) echo "Usage: scripts/check.sh [all|frontend|go]" >&2; exit 2 ;;
esac
