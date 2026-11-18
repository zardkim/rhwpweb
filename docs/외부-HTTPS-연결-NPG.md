# 외부 HTTPS 연결 (NPG)

```text
브라우저 ──HTTPS──▶ NPG (nginx-proxy-guard) ──HTTP──▶ <배포 호스트>:17780 (rhwp)
                                                       └─ /rhwp-auth/* 는 rhwp 가 rhwp-auth 로 중계
```

NPG 는 **rhwp 컨테이너 하나(포트 17780)만** 바라보면 된다. rhwp-auth 는 포트를 열지 않으며 NPG 가 직접 가리키면 안 된다.
다른 리버스 프록시(Nginx Proxy Manager, DSM 역방향 프록시 등)도 같은 원리다.

## 1) Proxy Host 추가

| 항목 | 값 |
|---|---|
| Domain Names | 사용할 도메인 (예: `hwp.example.com`) |
| Scheme | `http` |
| Forward Hostname / IP | 배포 호스트의 **LAN IP** (예: NAS `192.168.0.3`) |
| Forward Port | `17780` (`RHWP_PORT`) |
| Websockets Support | 꺼도 됨 |
| Block Common Exploits | 켜도 됨 |
| Access List / 기본 인증 | **끈다** — 인증은 rhwp-auth 가 한다. 겹치면 로그인 창이 두 번 뜬다 |

- Forward 에 `localhost`·`127.0.0.1` 을 쓰면 안 된다. NPG 도 컨테이너라 자기 자신을 가리킨다.
- **경로를 잘라내지 않는다.** `/rhwp/` 와 `/rhwp-auth/` 가 그대로 전달돼야 한다. 도메인 루트(`/`)로 들어오면 `/rhwp/` 로 보낸다.

## 2) SSL

SSL 탭에서 인증서 발급, **Force SSL** 켬. 그다음 `.env` 에 `RHWP_AUTH_COOKIE_SECURE=true` 를 넣고 `docker compose up -d`.
기본값 `auto` 는 `X-Forwarded-Proto: https` 를 보고 판단하지만, 외부에서 HTTPS 로만 쓸 거면 `true` 가 안전하다.

> ⚠ `true` 로 두면 **`http://<LAN IP>:17780` 으로는 로그인이 유지되지 않는다**. LAN 에서도 HTTP 로 직접 쓸 거면 `auto` 유지.

## 3) 이미 다른 서비스가 쓰는 도메인의 하위 경로로 붙일 때

**Custom Locations 를 두 개** 만든다. 하나라도 빠지면 로그인 화면이 404 가 된다.

| Location | Forward |
|---|---|
| `/rhwp/` | `http://<LAN IP>:17780` |
| `/rhwp-auth/` | `http://<LAN IP>:17780` |

Forward 주소 뒤에 경로를 붙이지 않는다 (`http://IP:17780/rhwp/` ❌).

## 4) 헤더·제한

- 기본 프록시 헤더(`Host`, `X-Forwarded-For`, `X-Forwarded-Proto`)를 그대로 쓴다.
  `X-Forwarded-For` 가 있어야 로그인 실패 제한이 **실제 접속자 IP** 기준으로 걸린다.
- 프록시의 업로드 제한(`client_max_body_size`)이 `RHWP_FILES_MAX_MB`(기본 100MB)보다 작으면 큰 문서 서버 저장이 **413** 으로 실패한다.

## 5) 확인

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:17780/healthz          # 200 (배포 호스트 안)
curl -sI https://<도메인>/rhwp/ | grep -iE '^HTTP|^location'                    # 302, location: /rhwp-auth/login?rd=/rhwp/
curl -s https://<도메인>/rhwp-auth/login | grep -c '로그인'                     # 1 이상
```

| 증상 | 원인 |
|---|---|
| `502 - Nginx Proxy Guard` 페이지 | NPG 가 17780 에 못 닿음 — Forward IP·포트, 컨테이너 `Up` 확인 |
| 로그인 화면이 404 | Custom Location 에 `/rhwp-auth/` 가 빠짐 |
| 로그인해도 계속 로그인 화면 | HTTP 로 접속 중인데 `RHWP_AUTH_COOKIE_SECURE=true` / 브라우저가 쿠키 차단 |
| 로그인 창이 두 번 | NPG 의 Access List·기본 인증이 켜져 있음 |
| `/rhwp/` 대신 `/` 로 잘못 이동 | NPG 가 경로를 잘라냄 |
| 모두가 동시에 "로그인 시도가 너무 많습니다" | NPG 가 `X-Forwarded-For` 를 보내지 않음 |
| 큰 문서만 "파일이 너무 큽니다" / 413 | 프록시 업로드 제한 |
