#!/usr/bin/env bash
# RHWP 이미지 빌드 / 레지스트리(기본 Docker Hub) 푸시
#
# 사용법:
#   ./build.sh                 소스 갱신 + 빌드 (로컬 이미지만)
#   ./build.sh --push          빌드 후 레지스트리에 <버전> 과 latest 로 푸시 (미리 docker login)
#   ./build.sh --no-fetch      소스 갱신 없이 현재 체크아웃으로 빌드
#   ./build.sh --push --force  레지스트리에 같은 태그가 다른 내용으로 있어도 덮어쓴다 (비권장)
#   ./build.sh --web-only      rhwpweb(웹) 만 빌드/푸시 (update.sh 가 사용)
#
# 태그 규칙:
#   rhwpweb      <studio 버전>-<업스트림 커밋 7자리>-r<PKG_REVISION>   예) 0.9.0-c9095c5-r6
#   rhwpweb-auth <auth/VERSION>                                        예) 0.4.1
#   패키징(Dockerfile·nginx·web/)을 바꾸면 PKG_REVISION 을, auth/ 를 바꾸면 auth/VERSION 을
#   올린다. 올리지 않고 푸시하면 "같은 태그·다른 내용" 으로 막힌다 (태그는 덮어쓰지 않는다).
#
# 환경변수:
#   RHWP_GIT_URL   소스 저장소 (기본: GitHub 업스트림)
#   RHWP_REF       브랜치/태그 (기본: main). 예) RHWP_REF=v0.9.0
#   RHWP_SRC_DIR   소스 체크아웃 위치 (기본: ~/.cache/rhwp-docker/src — 프로젝트 폴더 밖)
#   IMAGE_TAG      rhwpweb 태그 강제 지정
#   IMAGE_NS       이미지 이름 앞부분 (기본: docker.io/zardkim → zardkim/rhwpweb, zardkim/rhwpweb-auth)
#                  다른 레지스트리에 올리려면 예) IMAGE_NS=registry.example.com/team
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RHWP_GIT_URL="${RHWP_GIT_URL:-https://github.com/edwardkim/rhwp.git}"
RHWP_REF="${RHWP_REF:-main}"
RHWP_SRC_DIR="${RHWP_SRC_DIR:-$HOME/.cache/rhwp-docker/src}"
IMAGE_NS="${IMAGE_NS:-docker.io/zardkim}"
IMAGE="$IMAGE_NS/rhwpweb"
AUTH_IMAGE="$IMAGE_NS/rhwpweb-auth"
AUTH_VERSION="$(tr -d '[:space:]' < auth/VERSION)"
PKG_REVISION="$(tr -d '[:space:]' < PKG_REVISION)"

DO_PUSH=false
DO_FETCH=true
FORCE=false
WITH_AUTH=true
for arg in "$@"; do
  case "$arg" in
    --push)     DO_PUSH=true ;;
    --no-fetch) DO_FETCH=false ;;
    --force)    FORCE=true ;;
    --web-only) WITH_AUTH=false ;;
    -h|--help)  sed -n '2,23p' "$0"; exit 0 ;;
    *) echo "알 수 없는 인자: $arg" >&2; exit 2 ;;
  esac
done

# ---- 1. 소스 확보 -------------------------------------------------------
# 저장소가 크다(.git 6GB, mydocs 6GB, pdf 1.7GB). blob 지연 로딩 + sparse checkout 으로
# 빌드에 필요 없는 mydocs(manual 제외)·pdf 는 받지 않는다.
if $DO_FETCH; then
  if [ ! -d "$RHWP_SRC_DIR/.git" ]; then
    echo "▶ 소스 clone: $RHWP_GIT_URL ($RHWP_REF) -> $RHWP_SRC_DIR"
    mkdir -p "$(dirname "$RHWP_SRC_DIR")"
    git clone --filter=blob:none --no-checkout --depth 1 --branch "$RHWP_REF" \
      "$RHWP_GIT_URL" "$RHWP_SRC_DIR"
    git -C "$RHWP_SRC_DIR" sparse-checkout set --no-cone \
      '/*' '!/mydocs/' '/mydocs/manual/' '!/pdf/'
    git -C "$RHWP_SRC_DIR" checkout -q "$RHWP_REF"
  else
    echo "▶ 소스 갱신: $RHWP_REF"
    git -C "$RHWP_SRC_DIR" remote set-url origin "$RHWP_GIT_URL"
    git -C "$RHWP_SRC_DIR" fetch -q --filter=blob:none --depth 1 origin "$RHWP_REF"
    git -C "$RHWP_SRC_DIR" checkout -q --force FETCH_HEAD
  fi
fi

REVISION="$(git -C "$RHWP_SRC_DIR" rev-parse HEAD)"
SHORT="${REVISION:0:7}"
STUDIO_VERSION="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["version"])' \
  "$RHWP_SRC_DIR/rhwp-studio/package.json")"
IMAGE_TAG="${IMAGE_TAG:-$STUDIO_VERSION-$SHORT-r$PKG_REVISION}"

echo "▶ RHWP $STUDIO_VERSION @ $SHORT  ->  $IMAGE:$IMAGE_TAG"

# ---- 2. 빌드 ------------------------------------------------------------
# 빌드 컨텍스트 = 소스, 패키징 파일(nginx 설정) = 이름 있는 컨텍스트 pkg.
DOCKER_BUILDKIT=1 docker build \
  -f "$SCRIPT_DIR/Dockerfile" \
  --build-context pkg="$SCRIPT_DIR" \
  --build-arg RHWP_VERSION="$STUDIO_VERSION" \
  --build-arg RHWP_REVISION="$REVISION" \
  -t "$IMAGE:$IMAGE_TAG" \
  -t "$IMAGE:latest" \
  "$RHWP_SRC_DIR"

echo "✅ 빌드 완료: $IMAGE:$IMAGE_TAG (+ latest)"

# 로그인·사용자 관리 서비스 (버전은 auth/VERSION)
if $WITH_AUTH; then
  DOCKER_BUILDKIT=1 docker build \
    --build-arg VERSION="$AUTH_VERSION" \
    -t "$AUTH_IMAGE:$AUTH_VERSION" \
    -t "$AUTH_IMAGE:latest" \
    "$SCRIPT_DIR/auth"
  echo "✅ 빌드 완료: $AUTH_IMAGE:$AUTH_VERSION (+ latest)"
fi

# update.sh 가 결과 태그를 읽는다
if [ -n "${BUILD_RESULT_FILE:-}" ]; then
  printf '%s\n' "$IMAGE:$IMAGE_TAG" > "$BUILD_RESULT_FILE"
fi
if [ -n "${BUILD_PREPUSH_HOOK:-}" ]; then
  # 푸시 전 검사 훅 (update.sh 의 스모크 테스트). 실패하면 푸시하지 않는다.
  "$BUILD_PREPUSH_HOOK" "$IMAGE:$IMAGE_TAG"
fi

# ---- 3. 푸시 ------------------------------------------------------------
if $DO_PUSH; then
  # 자격증명은 docker login 으로 미리 (DOCKER_CONFIG 를 지정하면 그 설정을 쓴다)
  # 버전 태그는 불변으로 다룬다: 원격에 같은 태그가 있으면
  #   같은 내용 -> 건너뜀,  다른 내용 -> 중단 (버전을 올리라고 안내)
  # 비교는 linux/amd64 이미지 manifest digest 로 한다. 최상위 index digest 는
  # 빌드마다 새로 만들어지는 provenance attestation 때문에 내용이 같아도 달라진다.
  platform_digest_remote() {
    docker buildx imagetools inspect --raw "$1" 2>/dev/null | python3 -c '
import sys, json, hashlib
raw = sys.stdin.buffer.read()
if not raw: sys.exit(1)
d = json.loads(raw)
for m in d.get("manifests") or []:
    p = m.get("platform") or {}
    if p.get("os") == "linux" and p.get("architecture") == "amd64":
        print(m["digest"]); sys.exit(0)
print("sha256:" + hashlib.sha256(raw).hexdigest())'
  }
  push_versioned() {
    local repo="$1" tag="$2" bump_hint="$3" local_d remote_d
    local_d="$(docker image inspect --platform linux/amd64 -f '{{.Descriptor.Digest}}' "$repo:$tag")"
    if remote_d="$(platform_digest_remote "$repo:$tag")"; then
      if [ "$remote_d" = "$local_d" ]; then
        echo "⏭  $repo:$tag 는 이미 같은 내용으로 올라가 있다 — 건너뜀"
        return 0
      fi
      if ! $FORCE; then
        echo "❌ $repo:$tag 가 레지스트리에 다른 내용으로 이미 있다 ($remote_d ≠ $local_d)." >&2
        echo "   $bump_hint 를 올리고 다시 빌드할 것. (정말 덮어쓰려면 --force)" >&2
        exit 1
      fi
      echo "⚠  --force: $repo:$tag 를 덮어쓴다"
    fi
    docker push "$repo:$tag"
  }

  push_versioned "$IMAGE" "$IMAGE_TAG" "PKG_REVISION"
  docker push "$IMAGE:latest"
  echo "✅ 푸시 완료: $IMAGE:$IMAGE_TAG (+ latest)"
  if $WITH_AUTH; then
    push_versioned "$AUTH_IMAGE" "$AUTH_VERSION" "auth/VERSION"
    docker push "$AUTH_IMAGE:latest"
    echo "✅ 푸시 완료: $AUTH_IMAGE:$AUTH_VERSION (+ latest)"
  fi
fi
