#!/usr/bin/env bash
# 업스트림 RHWP 새 버전 자동 반영 (개발서버 cron 에서 실행)
#
#   ./update.sh            새 릴리스가 있으면 빌드 → 스모크 테스트 → 레지스트리 푸시(latest 이동)
#   ./update.sh --check    확인만 (빌드 안 함). 새 버전이 있으면 종료코드 10
#   ./update.sh --force    이미 빌드한 버전이어도 다시 진행
#
# 환경변수:
#   RHWP_TRACK   release(기본) = 최신 정식 태그 vX.Y.Z / main = main 브랜치 최신 커밋
#   RHWP_GIT_URL 업스트림 (기본 GitHub)
#
# 상태 파일 ~/.cache/rhwp-docker/last-built 에 "<ref> <commit> r<PKG_REVISION>" 을 기록한다.
# 같은 값이면 아무것도 하지 않는다. PKG_REVISION 을 올리면 같은 업스트림이라도 다시 빌드한다.
set -euo pipefail
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RHWP_TRACK="${RHWP_TRACK:-release}"
RHWP_GIT_URL="${RHWP_GIT_URL:-https://github.com/edwardkim/rhwp.git}"
STATE_DIR="$HOME/.cache/rhwp-docker"
STATE_FILE="$STATE_DIR/last-built"
mkdir -p "$STATE_DIR"

MODE=run
FORCE=false
for arg in "$@"; do
  case "$arg" in
    --check) MODE=check ;;
    --force) FORCE=true ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "알 수 없는 인자: $arg" >&2; exit 2 ;;
  esac
done

# 동시 실행 방지 (cron 이 겹치거나 수동 실행과 겹칠 때)
exec 9>"$STATE_DIR/update.lock"
flock -n 9 || { echo "$(date '+%F %T') 다른 update.sh 가 실행 중 — 종료"; exit 0; }

log() { echo "$(date '+%F %T') $*"; }

# ---- 1. 업스트림 최신 버전 확인 -------------------------------------------
case "$RHWP_TRACK" in
  release)
    # 정식 버전 태그만 (vX.Y.Z). -rc 같은 사전 릴리스는 제외.
    REF="$(git ls-remote --tags --refs "$RHWP_GIT_URL" 'v*' \
      | awk '{print $2}' | sed 's#refs/tags/##' \
      | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
    [ -n "$REF" ] || { log "❌ 업스트림 태그를 찾지 못함"; exit 1; }
    COMMIT="$(git ls-remote "$RHWP_GIT_URL" "refs/tags/$REF^{}" | awk '{print $1}')"
    [ -n "$COMMIT" ] || COMMIT="$(git ls-remote "$RHWP_GIT_URL" "refs/tags/$REF" | awk '{print $1}')"
    ;;
  main)
    REF=main
    COMMIT="$(git ls-remote "$RHWP_GIT_URL" refs/heads/main | awk '{print $1}')"
    ;;
  *) log "❌ RHWP_TRACK 은 release 또는 main"; exit 2 ;;
esac

PKG_REVISION="$(tr -d '[:space:]' < PKG_REVISION)"
WANT="$REF ${COMMIT:0:7} r$PKG_REVISION"
HAVE="$(cat "$STATE_FILE" 2>/dev/null || echo none)"

if [ "$WANT" = "$HAVE" ] && ! $FORCE; then
  log "최신 상태: $HAVE"
  exit 0
fi
log "새 버전: $WANT  (이전: $HAVE)"
[ "$MODE" = check ] && exit 10

# ---- 2. 빌드 → 스모크 테스트 → 푸시 -----------------------------------------
RESULT="$(mktemp)"
trap 'rm -f "$RESULT"' EXIT
if RHWP_REF="$REF" RHWP_GIT_URL="$RHWP_GIT_URL" \
   BUILD_RESULT_FILE="$RESULT" BUILD_PREPUSH_HOOK="$SCRIPT_DIR/scripts/smoke-test.sh" \
   ./build.sh --web-only --push; then
  echo "$WANT" > "$STATE_FILE"
  log "✅ 반영 완료: $(cat "$RESULT") → latest"
else
  log "❌ 실패: $WANT — 레지스트리의 latest 는 바뀌지 않았다. 위 로그 확인"
  exit 1
fi
