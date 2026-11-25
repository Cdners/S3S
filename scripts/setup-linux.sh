#!/usr/bin/env bash
# Native Linux development dependencies for Debian 13 and Ubuntu 24.04.
set -euo pipefail
if (( EUID != 0 )); then
  exec sudo bash "$0" "$@"
fi
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  ca-certificates build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev
pkg-config --modversion gtk4 webkitgtk-6.0 gio-unix-2.0
