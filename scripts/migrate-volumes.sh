#!/bin/sh
# 예전 compose(볼륨 rhwp-auth-data / rhwp-files)의 데이터를 .env 의 db/·files/ 폴더로 옮긴다.
#
#   cd /volume1/docker/rhwp            # 새 docker-compose.yml 과 .env 가 있는 설치 폴더
#   sudo sh scripts/migrate-volumes.sh [compose프로젝트이름]
#
# - 프로젝트 이름을 생략하면 *_rhwp-auth-data 볼륨을 찾아 하나뿐이면 그것을 쓴다.
# - 옮기기 전에 rhwp / rhwp-auth 컨테이너를 멈추고 지운다(볼륨은 지우지 않음 → 되돌릴 수 있다).
# - db 폴더에 이미 users.db 가 있으면 덮어쓰지 않고 멈춘다.
set -eu

dir=$(pwd)
[ -f "$dir/docker-compose.yml" ] || { echo "❌ 설치 폴더(docker-compose.yml 이 있는 곳)에서 실행하세요"; exit 1; }
grep -q 'RHWP_DB_DIR' "$dir/docker-compose.yml" || { echo "❌ docker-compose.yml 이 예전 것입니다. 새 파일로 먼저 바꾸세요"; exit 1; }

envget() { # .env 에서 값 읽기 (없으면 기본값)
  v=$( [ -f "$dir/.env" ] && sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$dir/.env" | tail -1 | sed 's/[[:space:]]*$//; s/^"\(.*\)"$/\1/; s/^'"'"'\(.*\)'"'"'$/\1/' )
  [ -n "${v:-}" ] && echo "$v" || echo "$2"
}
abspath() { case "$1" in /*) echo "$1" ;; *) echo "$dir/${1#./}" ;; esac; }

DB=$(abspath "$(envget RHWP_DB_DIR ./db)")
FILES=$(abspath "$(envget RHWP_FILES_DIR ./files)")
PUID=$(envget PUID 1000)
PGID=$(envget PGID 1000)

proj=${1:-}
if [ -z "$proj" ]; then
  cands=$(docker volume ls -q | sed -n 's/_rhwp-auth-data$//p')
  n=$(printf '%s\n' "$cands" | grep -c . || true)
  if [ "$n" -ne 1 ]; then
    echo "예전 볼륨 후보:"; docker volume ls -q | grep -E '_rhwp-(auth-data|files)$' || echo "  (없음)"
    echo "❌ 프로젝트 이름을 지정하세요: sh $0 <이름>   (볼륨 <이름>_rhwp-auth-data 의 <이름>)"; exit 1
  fi
  proj=$cands
fi
VDB="${proj}_rhwp-auth-data"
VFILES="${proj}_rhwp-files"
docker volume inspect "$VDB" >/dev/null 2>&1 || { echo "❌ 볼륨 $VDB 가 없습니다"; exit 1; }

echo "옮기기:"
echo "  $VDB  →  $DB"
docker volume inspect "$VFILES" >/dev/null 2>&1 && echo "  $VFILES  →  $FILES" || { echo "  ($VFILES 없음 — 문서는 건너뜀)"; VFILES=; }
echo "  소유자 → $PUID:$PGID"
if [ -e "$DB/users.db" ] || [ -e "$DB/users.json" ]; then
  echo "❌ $DB 에 이미 사용자 DB 가 있습니다. 덮어쓰지 않습니다 (필요하면 직접 치운 뒤 다시 실행)"; exit 1
fi
printf '계속할까요? [y/N] '; read -r a; [ "$a" = y ] || [ "$a" = Y ] || exit 1

for c in rhwp rhwp-auth; do docker stop -t 15 "$c" >/dev/null 2>&1 && docker rm "$c" >/dev/null && echo "  컨테이너 $c 정리" || true; done
mkdir -p "$DB" "$FILES"
docker run --rm -v "$VDB:/from:ro" -v "$DB:/to" alpine sh -c "cp -a /from/. /to/ && chown -R $PUID:$PGID /to"
[ -n "$VFILES" ] && docker run --rm -v "$VFILES:/from:ro" -v "$FILES:/to" alpine sh -c "cp -a /from/. /to/ && chown -R $PUID:$PGID /to"

echo "✅ 복사 완료. 이제:  docker compose up -d"
echo "   확인 후 옛 볼륨 삭제:  docker volume rm $VDB${VFILES:+ $VFILES}"
echo "   (예전 프로젝트 '$proj' 를 Container Manager/Portainer 에서 만들었다면 그 프로젝트는 삭제하거나 새 compose 로 바꿔 두세요)"
