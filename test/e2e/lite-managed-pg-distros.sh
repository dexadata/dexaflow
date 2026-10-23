#!/usr/bin/env bash
# Cold-start gate for the managed Postgres on bare distribution images (#279).
#
# `dexaflow lite --postgres managed` downloads a relocatable PostgreSQL that
# links a few system libraries it does not bundle (libxml2, Kerberos, OpenSSL 3,
# zstd, lz4). A bare container lacks some of them: ubuntu:22.04 has no libxml2,
# ubuntu:24.04 has neither libxml2 nor Kerberos. The glibc build carries no ICU
# of its own, so nothing here depends on the distribution's ICU release; the
# libicuuc an `ldd` shows is the host libxml2's.
#
# For each image this boots Lite in a fresh container as a non-root user:
#   1. on the bare image, Lite either becomes ready or stops naming the missing
#      libraries and the exact install command; any other failure fails;
#   2. that printed command is run as root, unedited but for sudo;
#   3. Lite then downloads nothing new, initdb's, starts and answers /readyz.
#
# Needs Docker and network (the ~11 MB PostgreSQL download and apt). The images
# default to Ubuntu 22.04 and 24.04; pass others as arguments. Behind a TLS
# proxy, set E2E_CA_BUNDLE to its CA file and HTTPS_PROXY to the proxy:
#   bash test/e2e/lite-managed-pg-distros.sh [image ...]
# The host builds static binaries once (CGO_ENABLED=0) unless DEXAFLOW_E2E_BIN
# names a directory that already holds dexaflow, dexaflow-server, dexaflow-agent.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ "$#" -eq 0 ]; then
  set -- mirror.gcr.io/library/ubuntu:22.04 mirror.gcr.io/library/ubuntu:24.04
fi

BIN="${DEXAFLOW_E2E_BIN:-}"
if [ -z "$BIN" ]; then
  BIN="$TMP/bin"
  echo "==> building static binaries"
  (cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN/" ./cmd/dexaflow ./cmd/dexaflow-server ./cmd/dexaflow-agent)
fi

# The part that runs inside the container, as root.
cat >"$TMP/inner.sh" <<'INNER'
#!/usr/bin/env bash
set -euo pipefail
. /etc/os-release
echo "==> $PRETTY_NAME"
export DEBIAN_FRONTEND=noninteractive
if [ -f /e2e/ca.crt ]; then
  # A TLS-intercepting proxy: apt over https through it, and its CA trusted.
  sed -i 's#http://#https://#g' /etc/apt/sources.list /etc/apt/sources.list.d/*.sources 2>/dev/null || true
  printf 'Acquire::https::Proxy "%s";\nAcquire::https::CAInfo "/e2e/ca.crt";\n' "$HTTPS_PROXY" >/etc/apt/apt.conf.d/99e2e-proxy
fi
apt-get -o Acquire::Retries=3 update -qq >/dev/null
# ca-certificates only, for the download. It pulls in openssl, which a bare
# image already has the library of, and none of the libraries under test.
apt-get -o Acquire::Retries=3 install -y -qq --no-install-recommends ca-certificates >/dev/null
if [ -f /e2e/ca.crt ]; then
  cp /e2e/ca.crt /usr/local/share/ca-certificates/e2e-proxy.crt
  update-ca-certificates >/dev/null 2>&1
fi
useradd -m dx
mkdir -p /home/dx/ws
chown -R dx /home/dx

# readyz probes /readyz with bash's /dev/tcp, so the image needs no curl (whose
# libcurl would pull in Kerberos and hide its absence).
cat >/home/dx/lite.sh <<'LITE'
#!/usr/bin/env bash
# usage: lite.sh <log>   exit 0 ready, 1 the lite process exited, 2 timed out
/opt/dx/dexaflow lite --postgres managed --executor subprocess --port 18099 --host 127.0.0.1 \
  --server-bin /opt/dx/dexaflow-server --agent-bin /opt/dx/dexaflow-agent /home/dx/ws >"$1" 2>&1 &
pid=$!
for _ in $(seq 1 240); do
  if ! kill -0 "$pid" 2>/dev/null; then wait "$pid" || true; exit 1; fi
  if { exec 3<>/dev/tcp/127.0.0.1/18099; } 2>/dev/null; then
    printf 'GET /readyz HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
    status=$(head -1 <&3 || true)
    exec 3>&-
    case "$status" in *" 200"*)
      kill -TERM "$pid"; wait "$pid" 2>/dev/null || true; exit 0 ;;
    esac
  fi
  sleep 1
done
kill -TERM "$pid" 2>/dev/null || true
exit 2
LITE
chmod +x /home/dx/lite.sh
as_dx() { su dx -s /bin/bash -c "$*"; }

rc=0
as_dx "env HTTPS_PROXY='${HTTPS_PROXY:-}' /home/dx/lite.sh /home/dx/first.log" || rc=$?
if [ "$rc" -eq 0 ]; then
  echo "  PASS ready on the bare image (no library was missing)"
  exit 0
fi
if [ "$rc" -ne 1 ]; then
  echo "  FAIL the first boot hung"; tail -40 /home/dx/first.log; exit 1
fi
if ! grep -q "system libraries are missing" /home/dx/first.log; then
  echo "  FAIL the first boot failed without naming missing libraries"; tail -40 /home/dx/first.log; exit 1
fi
grep "system libraries are missing" /home/dx/first.log | sed 's/^/  /'
cmd=$(sed -n 's/^ *install them: //p' /home/dx/first.log | head -1)
case "$cmd" in
  "sudo apt-get install -y "*) ;;
  *) echo "  FAIL no apt-get install command printed (got '$cmd')"; tail -40 /home/dx/first.log; exit 1 ;;
esac
echo "  running the printed command: $cmd"
${cmd#sudo } -qq -o Acquire::Retries=3 >/dev/null

rc=0
as_dx "env HTTPS_PROXY='${HTTPS_PROXY:-}' /home/dx/lite.sh /home/dx/second.log" || rc=$?
if [ "$rc" -ne 0 ]; then
  echo "  FAIL not ready after installing the named packages (rc=$rc)"; tail -60 /home/dx/second.log; exit 1
fi
grep -q "downloading relocatable PostgreSQL" /home/dx/second.log && {
  echo "  FAIL the second boot downloaded PostgreSQL again"; exit 1; }
echo "  PASS ready after installing the packages Lite named"
INNER

ca_args=()
if [ -n "${E2E_CA_BUNDLE:-}" ]; then
  ca_args=(-v "$E2E_CA_BUNDLE:/e2e/ca.crt:ro" -e "HTTPS_PROXY=${HTTPS_PROXY:-}" --network host)
fi
for image in "$@"; do
  docker run --rm "${ca_args[@]}" \
    -v "$BIN/dexaflow:/opt/dx/dexaflow:ro" \
    -v "$BIN/dexaflow-server:/opt/dx/dexaflow-server:ro" \
    -v "$BIN/dexaflow-agent:/opt/dx/dexaflow-agent:ro" \
    -v "$TMP/inner.sh:/e2e/inner.sh:ro" \
    "$image" bash /e2e/inner.sh
done
echo
echo "  managed Postgres cold start verified on: $*"
