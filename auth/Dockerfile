# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go vet ./... \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/rhwp-auth . \
 && mkdir -p /out/data /out/files

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="rhwpweb-auth" \
      org.opencontainers.image.source="https://github.com/zardkim/rhwpweb" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Lightweight login & user management for RHWP (nginx auth_request)" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/rhwp-auth /usr/local/bin/rhwp-auth
# /data  = db 폴더 (users.db, secret.key)   /files = 사용자별 문서 폴더
# 예전 compose(user: 65532, 새 Docker 볼륨)에서도 쓸 수 있게 기본 소유자는 nonroot(65532)
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/files /files
# root 로 시작해 두 폴더의 소유자를 PUID:PGID 로 맞춘 뒤 그 사용자로 내려가 실행한다 (privdrop.go).
# compose 에서 user: 로 비root 를 지정하면 이 과정을 건너뛴다.
ENV RHWP_AUTH_DATA=/data RHWP_AUTH_FILES=/files PUID=1000 PGID=1000
EXPOSE 8080
USER root
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD ["/usr/local/bin/rhwp-auth", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/rhwp-auth"]
