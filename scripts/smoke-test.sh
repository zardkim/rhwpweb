#!/usr/bin/env bash
# rhwpweb 이미지 스모크 테스트: 임시 컨테이너(인증 off)를 띄워 HTTP + 브라우저 검사.
#   scripts/smoke-test.sh <이미지:태그>
# 브라우저 검사는 Playwright 도커 이미지를 쓴다 (SMOKE_BROWSER=0 이면 생략).
set -euo pipefail

IMAGE="$1"
PW_VERSION="${PW_VERSION:-1.63.0}"
PW_IMAGE="mcr.microsoft.com/playwright:v${PW_VERSION}-noble"
E2E_DIR="${E2E_DIR:-$HOME/.cache/rhwp-docker/e2e}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAME="rhwp-smoke-$$"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" -e RHWP_AUTH=off -p 127.0.0.1::80 "$IMAGE" >/dev/null
PORT="$(docker port "$NAME" 80/tcp | head -1 | sed 's/.*://')"
U="http://127.0.0.1:$PORT"

for _ in $(seq 1 30); do
  curl -sf -o /dev/null "$U/healthz" && break
  sleep 1
done

fail() { echo "❌ smoke: $*" >&2; docker logs "$NAME" 2>&1 | tail -20 >&2; exit 1; }
code() { curl -s -o /dev/null -w '%{http_code} %{content_type}' "$U$1"; }

[[ "$(code /healthz)" == 200* ]]            || fail "/healthz"
[[ "$(code /rhwp/)" == "200 text/html"* ]]  || fail "/rhwp/ → $(code /rhwp/)"
[[ "$(code /rhwp/rhwp_bg.wasm)" == "200 application/wasm" ]] || fail "rhwp_bg.wasm → $(code /rhwp/rhwp_bg.wasm)"
for p in $(curl -s "$U/rhwp/" | grep -oE '(src|href)="/rhwp/[^"]+"' | cut -d'"' -f2); do
  [[ "$(code "$p")" == 200* ]] || fail "$p → $(code "$p")"
done
echo "✅ smoke(HTTP): $IMAGE"

if [ "${SMOKE_BROWSER:-1}" = "1" ]; then
  mkdir -p "$E2E_DIR"
  cp "$SCRIPT_DIR/smoke.mjs" "$E2E_DIR/smoke.mjs"
  docker run --rm --network host -u "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$E2E_DIR:/w" -w /w "$PW_IMAGE" sh -c "
      [ -d node_modules/playwright ] || npm i -s --no-audit --no-fund playwright@$PW_VERSION >/dev/null 2>&1
      node smoke.mjs '$U'" || fail "브라우저 검사"
  echo "✅ smoke(browser): $IMAGE"
fi
