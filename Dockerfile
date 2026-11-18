# syntax=docker/dockerfile:1.7
#
# RHWP Studio 웹 이미지 (Nginx + 정적 빌드 산출물)
#
# 빌드 컨텍스트 = RHWP 소스 체크아웃, 패키징 파일 = 이름 있는 컨텍스트 `pkg`.
# 직접 빌드하지 말고 scripts/build.sh 를 쓴다.
#
#   docker build -f Dockerfile --build-context pkg=. <rhwp-src-dir>
#
# 업스트림 .github/workflows/deploy-pages.yml 과 같은 절차를 따른다:
#   WASM(release) -> rhwp-studio vite build --base=/rhwp/

ARG RUST_VERSION=1.93.1
ARG NODE_VERSION=22
ARG NGINX_VERSION=1.29-alpine

############################
# 1) WASM 빌드
############################
FROM rust:${RUST_VERSION}-bookworm AS wasm
ARG WASM_PACK_VERSION=0.15.0
RUN rustup target add wasm32-unknown-unknown \
 && curl -sSfL --retry 5 \
      "https://github.com/rustwasm/wasm-pack/releases/download/v${WASM_PACK_VERSION}/wasm-pack-v${WASM_PACK_VERSION}-x86_64-unknown-linux-musl.tar.gz" \
    | tar -xz -C /tmp \
 && mv /tmp/wasm-pack-v${WASM_PACK_VERSION}-x86_64-unknown-linux-musl/wasm-pack /usr/local/cargo/bin/ \
 && wasm-pack --version

WORKDIR /src
COPY . .
# target/ 는 캐시 마운트라 이미지 레이어에 남지 않는다. pkg/ 와 studio/public 의 glue 만 남긴다.
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/src/target \
    --mount=type=cache,target=/root/.cache/.wasm-pack \
    sh scripts/wasm-pack-locked.sh --target web --release \
 && ls -l pkg/rhwp_bg.wasm rhwp-studio/public/rhwp_bg.wasm

############################
# 2) Studio 빌드
############################
FROM node:${NODE_VERSION}-bookworm-slim AS studio
ARG RHWP_BASE=/rhwp/
ARG RHWP_WITHOUT_AI=0
ARG RHWP_WITHOUT_HWPCTRL=0
ARG RHWP_DISABLE_EXTERNAL_WEBFONTS=0
# 1 = 업스트림 PWA(서비스 워커 오프라인 캐시) 유지. 0(기본) = 해제.
# 서비스 워커는 캐시된 앱을 서버 확인 없이 띄우므로, 로그아웃·계정 비활성화가 먹히지 않는다.
ARG RHWP_PWA=0
# 1(기본) = [파일] 메뉴에 '서버에서 열기/서버에 저장' 추가 (web/cloud/cloud.js, rhwp-auth 필요)
ARG RHWP_CLOUD=1
WORKDIR /src
COPY --from=wasm /src /src
RUN cp assets/logo/favicon.ico rhwp-studio/public/favicon.ico
WORKDIR /src/rhwp-studio
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY --from=pkg web /pkg/web
RUN RHWP_WITHOUT_AI=${RHWP_WITHOUT_AI} \
    RHWP_WITHOUT_HWPCTRL=${RHWP_WITHOUT_HWPCTRL} \
    RHWP_DISABLE_EXTERNAL_WEBFONTS=${RHWP_DISABLE_EXTERNAL_WEBFONTS} \
    npx vite build --base=${RHWP_BASE} \
 && if [ "${RHWP_PWA}" = "0" ]; then \
      rm -f dist/workbox-*.js && cp /pkg/web/no-sw/sw.js /pkg/web/no-sw/registerSW.js dist/; \
    fi \
 && if [ "${RHWP_CLOUD}" = "1" ]; then \
      mkdir -p dist/cloud && cp /pkg/web/cloud/cloud.js dist/cloud/ \
      # 업스트림 index.html 구조가 바뀌어 주입 자리를 못 찾으면 빌드를 멈춘다
      && grep -q '</head>' dist/index.html \
      && sed -i "s#</head>#  <script src=\"${RHWP_BASE}cloud/cloud.js\" defer></script>\n</head>#" dist/index.html \
      && grep -q "${RHWP_BASE}cloud/cloud.js" dist/index.html; \
    fi \
 && test -f dist/index.html && test -f dist/rhwp_bg.wasm \
 && grep -q "${RHWP_BASE}assets/" dist/index.html \
 # 큰 정적 파일은 미리 압축해 gzip_static 으로 내보낸다 (wasm 26MB -> 수 MB)
 && find dist -type f \( -name '*.wasm' -o -name '*.js' -o -name '*.css' -o -name '*.html' \
      -o -name '*.svg' -o -name '*.json' -o -name '*.webmanifest' -o -name '*.ttf' -o -name '*.otf' \) \
      -size +1k -exec gzip -9 -k -n {} +

############################
# 3) Runtime
############################
FROM nginx:${NGINX_VERSION}
ARG RHWP_VERSION=unknown
ARG RHWP_REVISION=unknown
LABEL org.opencontainers.image.title="rhwpweb" \
      org.opencontainers.image.description="RHWP Studio (HWP/HWPX web editor, github.com/edwardkim/rhwp) served by Nginx at /rhwp/" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/zardkim/rhwpweb" \
      org.opencontainers.image.url="https://github.com/edwardkim/rhwp" \
      org.opencontainers.image.version="${RHWP_VERSION}" \
      org.opencontainers.image.revision="${RHWP_REVISION}"

ENV RHWP_AUTH=basic \
    RHWP_AUTH_REALM="RHWP Login" \
    RHWP_HTPASSWD=/etc/nginx/auth/.htpasswd

RUN rm -f /etc/nginx/conf.d/default.conf && mkdir -p /etc/nginx/rhwp-auth.d
COPY --from=pkg nginx/default.conf /etc/nginx/conf.d/default.conf
COPY --from=pkg --chmod=755 nginx/40-rhwp-auth.sh /docker-entrypoint.d/40-rhwp-auth.sh
COPY --from=studio /src/rhwp-studio/dist /usr/share/nginx/html/rhwp
# 라이선스: 업스트림 RHWP(MIT) 와 이 패키징(MIT)
COPY --from=wasm /src/LICENSE /usr/share/licenses/rhwp/LICENSE
COPY --from=pkg LICENSE /usr/share/licenses/rhwpweb/LICENSE
RUN echo "${RHWP_VERSION} ${RHWP_REVISION}" > /usr/share/nginx/html/rhwp/VERSION

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O /dev/null http://127.0.0.1/healthz || exit 1
