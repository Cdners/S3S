#!/usr/bin/env bash
# Run the actual binary in the cloud build image. All user state lives in tmpfs.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
install -m 0555 "${1:?Supply the built Linux S3S executable}" "$stage/S3S"
install -m 0444 "$root/tests/ui/native.py" "$stage/native.py"
install -m 0444 "$root"/frontend/src/locales/{en-US,zh-CN}.json "$stage/"
chmod 0755 "$stage"
# This host blocks nested WebKit user namespaces. Keep the test renderer in
# a disposable, unprivileged container with no network or writable host mount.
docker run --init --rm --network=none --read-only --cap-drop=ALL \
  --security-opt=no-new-privileges --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --user nobody:nogroup -e WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 \
  -e XDG_CACHE_HOME=/tmp/cache -v "$stage:/checks:ro" s3s-m0-linux:local \
  xvfb-run -a -s '-screen 0 1440x900x24' dbus-run-session -- \
  python3 /checks/native.py /checks/S3S /checks
