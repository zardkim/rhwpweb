#!/bin/sh
# nginx 공식 이미지의 /docker-entrypoint.d 훅. 인증 설정 파일을 만든다.
#   RHWP_AUTH=request       rhwp-auth 서비스로 로그인 (로그인 화면·사용자 관리)
#   RHWP_AUTH=basic (기본)  htpasswd 파일 필수, 없으면 기동 실패
#   RHWP_AUTH=off           인증 없음 (앞단 프록시/SSO 가 인증할 때만)
set -eu

out=/etc/nginx/rhwp-auth.d/auth.conf
mkdir -p "$(dirname "$out")"
mode="${RHWP_AUTH:-basic}"
file="${RHWP_HTPASSWD:-/etc/nginx/auth/.htpasswd}"
realm="${RHWP_AUTH_REALM:-RHWP Login}"
upstream="${RHWP_AUTH_UPSTREAM:-http://rhwp-auth:8080}"
upload_mb="${RHWP_UPLOAD_MAX_MB:-100}"   # 서버 저장 최대 크기 (rhwp-auth 의 RHWP_AUTH_FILES_MAX_MB 와 맞출 것)

case "$mode" in
  request)
    case "$upload_mb" in ''|*[!0-9]*) echo "rhwp: RHWP_UPLOAD_MAX_MB 는 숫자여야 한다" >&2; exit 1 ;; esac
    case "$upstream" in
      http://*) ;;
      *) echo "rhwp: RHWP_AUTH_UPSTREAM 은 http://호스트:포트 형식이어야 한다 (현재: $upstream)" >&2; exit 1 ;;
    esac
    # 업스트림 이름을 요청 시점에 해석한다 → rhwp-auth 가 늦게 뜨거나 재시작돼 IP 가 바뀌어도
    # nginx 가 기동 실패하거나 옛 IP 를 붙잡지 않는다. (Docker 내장 DNS)
    ns=$(awk '/^nameserver/{print $2; exit}' /etc/resolv.conf)
    case "$ns" in *:*) ns="[$ns]" ;; esac
    cat > "$out" <<EOF
resolver ${ns:-127.0.0.11} valid=10s ipv6=off;
auth_request /rhwp-auth/verify;
error_page 401 = @rhwp_login;

location = /rhwp-auth/verify {
    internal;
    set \$rhwp_auth_upstream $upstream;
    proxy_pass \$rhwp_auth_upstream;
    proxy_pass_request_body off;
    proxy_set_header Content-Length "";
    proxy_set_header Host \$host;
    proxy_set_header X-Original-URI \$request_uri;
    proxy_connect_timeout 3s;
    proxy_read_timeout 5s;
}

location /rhwp-auth/ {
    auth_request off;
    set \$rhwp_auth_upstream $upstream;
    proxy_pass \$rhwp_auth_upstream;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$rhwp_fwd_proto;
    client_max_body_size 64k;
}

# 서버 저장/불러오기 API(/api/fs, /api/files, /api/me): 문서 본문이 오가므로 크기 제한을 따로 둔다
location ^~ /rhwp-auth/api/ {
    auth_request off;
    set \$rhwp_auth_upstream $upstream;
    proxy_pass \$rhwp_auth_upstream;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$rhwp_fwd_proto;
    client_max_body_size ${upload_mb}m;
    proxy_read_timeout 120s;
    proxy_send_timeout 120s;
}

location @rhwp_login {
    return 302 /rhwp-auth/login?rd=\$request_uri;
}
EOF
    echo "rhwp: 로그인 서비스 사용 ($upstream)"
    ;;
  basic)
    if [ -d "$file" ]; then
      echo "rhwp: $file 가 디렉터리다. 호스트에 htpasswd 파일이 없는 채로 마운트된 것 — README 의 계정 생성 절차를 먼저 실행할 것" >&2
      exit 1
    fi
    if [ ! -s "$file" ]; then
      echo "rhwp: RHWP_AUTH=basic 인데 $file 가 없거나 비어 있다" >&2
      exit 1
    fi
    realm_escaped=$(printf '%s' "$realm" | sed 's/["\\]/\\&/g')
    printf 'auth_basic "%s";\nauth_basic_user_file %s;\n' "$realm_escaped" "$file" > "$out"
    echo "rhwp: Basic 인증 사용 ($(grep -c . "$file") 계정)"
    ;;
  off)
    echo 'auth_basic off;' > "$out"
    echo "rhwp: ⚠ 인증 비활성화 (RHWP_AUTH=off) — 앞단에서 인증하지 않으면 누구나 접근 가능" >&2
    ;;
  *)
    echo "rhwp: RHWP_AUTH 값은 request, basic, off 중 하나여야 한다 (현재: $mode)" >&2
    exit 1
    ;;
esac
