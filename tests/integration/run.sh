#!/usr/bin/env bash
# Isolated AMD64 Linux integration check. Run after preparing the cloud toolchain.
set -euo pipefail
cd "$(dirname "$0")/../.."
tools_dir=${S3S_TOOLS_DIR:-/workspace/.s3s-tools}
moto='docker.io/motoserver/moto@sha256:b459cb555b5809c1839cf7532e5b3ce49b55a4ab04077f398aa97b02b8dd58e9' # 5.2.6, linux/amd64
stage=$(mktemp -d)
service=
cleanup() {
  if [[ -n $service ]]; then docker rm -f "$service" >/dev/null; fi
  rm -rf "$stage"
}
trap cleanup EXIT
mkdir -m 0755 "$stage/server" "$stage/fixture"
aws=s3.us-east-1.amazonaws.com
r2=00000000000000000000000000000000.r2.cloudflarestorage.com
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=s3s-integration' \
  -addext "subjectAltName=DNS:$aws,DNS:$r2" -keyout "$stage/server/server.key" \
  -out "$stage/fixture/server.crt" 2>/dev/null
python3 - "$stage/fixture/credentials.json" <<'PY'
import json, secrets, sys
from pathlib import Path
Path(sys.argv[1]).write_text(json.dumps({'accessKeyId': secrets.token_hex(10).upper(),
                                         'secretAccessKey': secrets.token_hex(32)}))
PY
chmod 0444 "$stage/fixture/"*
# Both provider hostnames point at loopback only inside this offline namespace.
service=$(docker run --init -d --network none --read-only --tmpfs /tmp \
  --add-host "$aws:127.0.0.1" --add-host "$r2:127.0.0.1" \
  --security-opt=no-new-privileges -e S3_IGNORE_SUBDOMAIN_BUCKETNAME=true -e NO_PROXY='*' \
  -v "$stage/server:/cert:ro" -v "$stage/fixture:/fixture:ro" \
  "$moto" -p 443 -c /fixture/server.crt -k /cert/server.key)
docker exec -i "$service" python - < tests/integration/seed.py > "$stage/fixture/object.json"
chmod 0444 "$stage/fixture/object.json"
# Build separately; the process running the test binary has no registry or cloud access.
if [[ -n ${S3S_INTEGRATION_BINARY:-} ]]; then
  install -m 0555 "$S3S_INTEGRATION_BINARY" "$stage/fixture/integration"
else
  docker run --rm --user "$(id -u):$(id -g)" -w /src -v "$PWD:/src:ro" \
    -v "$tools_dir:/tools" -v "$stage/fixture:/out" -e CGO_ENABLED=1 -e GOTOOLCHAIN=local \
    -e GOCACHE=/tools/gocache -e GOPATH=/tools/gopath -e PATH=/tools/go/bin:/usr/local/bin:/usr/bin:/bin \
    s3s-m0-linux:local go test -c -mod=readonly -tags=s3integration -o /out/integration ./tests/integration
fi
chmod 0555 "$stage/fixture/integration"
sandbox=(--init --rm --network "container:$service" --read-only --cap-drop=ALL --security-opt=no-new-privileges
         --user nobody:nogroup --tmpfs /tmp:rw,nosuid,nodev,size=256m -e SSL_CERT_FILE=/fixture/server.crt
         -e NO_PROXY='*' -v "$stage/fixture:/fixture:ro")
# The test executable needs glibc and no desktop dependencies.
base='debian:trixie-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0'
docker run "${sandbox[@]}" "$base" /fixture/integration -test.v -test.timeout 90s
if [[ $# -gt 0 ]]; then
  install -m 0555 "$1" "$stage/fixture/S3S"
  install -m 0444 tests/ui/native.py tests/integration/desktop.py frontend/src/locales/{en-US,zh-CN}.json "$stage/fixture/"
  docker build -t s3s-keyring-integration:local -f tests/integration/Dockerfile.desktop tests/integration
  docker run "${sandbox[@]}" -e WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1 \
    -e GSETTINGS_BACKEND=memory -e XDG_CACHE_HOME=/tmp/cache s3s-keyring-integration:local \
    xvfb-run -a -s '-noreset -screen 0 1440x900x24' dbus-run-session -- \
    sh -ec 'xdotool getdisplaygeometry >/dev/null; exec python3 /fixture/desktop.py'
fi
