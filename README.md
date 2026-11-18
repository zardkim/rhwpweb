# rhwp-docker

**웹 브라우저에서 한글(HWP/HWPX) 문서를 보고 편집하는 RHWP 를, 로그인과 서버 저장까지 붙여
NAS·서버에 Docker 로 바로 설치할 수 있게 묶은 것입니다.**

[RHWP Studio](https://github.com/edwardkim/rhwp)(HWP/HWPX 웹 뷰어·에디터, MIT)를 바탕으로 합니다.

> A self-hosted package of [RHWP](https://github.com/edwardkim/rhwp) (HWP/HWPX web editor) with login,
> user management and per-user server storage. Images: [`zardkim/rhwpweb`](https://hub.docker.com/r/zardkim/rhwpweb),
> [`zardkim/rhwpweb-auth`](https://hub.docker.com/r/zardkim/rhwpweb-auth).

## 주요 기능

- **브라우저에서 HWP/HWPX 편집** — 프로그램 설치 없이 PC·태블릿·휴대폰 브라우저로 연다.
  문서 처리는 브라우저 안(WASM)에서 한다.
- **로그인과 사용자 관리** — 로그인한 사람만 쓸 수 있다. 관리자는 웹 화면에서 사용자를
  추가·비활성화하고 비밀번호를 재설정한다. 사용자 정보는 `db/users.db`(SQLite)에 저장된다.
- **서버 저장/불러오기** — [파일] 메뉴에서 로컬(PC·휴대폰)과 서버 중 골라 저장한다.
  서버에 저장한 문서는 NAS 의 `files/<사용자>/` 에 실제 파일로 남아 File Station·SMB 에서도 보인다.
- **공유 폴더** — NAS 의 다른 폴더(부서 공용 문서, 양식 모음 등)를 붙여 모든 사용자가 함께 쓴다.
- **계정 메뉴** — 편집기 메뉴바 오른쪽 👤 버튼에서 내 계정·비밀번호 변경, 사용자 관리(관리자), 로그아웃.

| 이미지 (Docker Hub) | 역할 |
|---|---|
| [`zardkim/rhwpweb`](https://hub.docker.com/r/zardkim/rhwpweb) | 편집기 화면 (Nginx + RHWP Studio) |
| [`zardkim/rhwpweb-auth`](https://hub.docker.com/r/zardkim/rhwpweb-auth) | 로그인·사용자 관리·서버 저장 |

`latest` 는 RHWP 정식 릴리스를 따라간다. 버전별 태그는 [릴리스](https://github.com/zardkim/rhwpweb/releases) 와 Docker Hub 에서 확인한다.

## 설치

### 1. 설치 폴더 만들기

설치 폴더에 `docker-compose.yml`, `.env.example` (공유 폴더를 쓸 거면 `docker-compose.override.yml.example` 도)
을 받는다. `db/`, `files/` 는 처음 실행할 때 자동으로 만들어진다.

```bash
mkdir -p /volume1/docker/rhwp && cd /volume1/docker/rhwp
base=https://raw.githubusercontent.com/zardkim/rhwpweb/main
curl -fsSLO $base/docker-compose.yml
curl -fsSLO $base/.env.example
curl -fsSLO $base/docker-compose.override.yml.example     # 공유 폴더를 쓸 때
```

(또는 [릴리스](https://github.com/zardkim/rhwpweb/releases) 의 소스 압축 파일을 받아 풀어도 된다.)

```text
/volume1/docker/rhwp/            ← 설치 폴더 (예)
├── docker-compose.yml
├── .env                         ← .env.example 을 복사해 고친 것
├── docker-compose.override.yml  ← (선택) 공유 폴더 마운트
├── db/                          ← 사용자 DB (users.db, secret.key)       [자동 생성]
└── files/                       ← 서버에 저장한 문서                       [자동 생성]
    ├── admin/                   ←   사용자별 '내 문서'
    ├── kim/
    └── mount/                   ←   공유 폴더를 붙이는 자리
```

### 2. `.env` 설정

```bash
cd /volume1/docker/rhwp
cp .env.example .env
```

최소한 **`PUID`/`PGID`** 와 **첫 관리자 비밀번호**를 정한다 → [`.env` 설정](#env-설정)

### 3. 실행

```bash
docker compose up -d
docker compose ps            # rhwp, rhwp-auth 둘 다 (healthy)
```

`RHWP_AUTH_ADMIN_PASSWORD` 를 비워 두었다면 자동으로 만든 관리자 비밀번호가 로그에 한 번 나온다:

```bash
docker logs rhwp-auth 2>&1 | grep '첫 관리자'
```

### 4. 접속

| 주소 | 내용 |
|---|---|
| `http://<NAS주소>:17780/rhwp/` | 편집기 (로그인 화면이 먼저 뜬다) |
| `http://<NAS주소>:17780/rhwp-auth/` | 내 계정 · 비밀번호 변경 · 로그아웃 |
| `http://<NAS주소>:17780/rhwp-auth/admin` | 사용자 관리 · 저장 위치 점검 (관리자만) |

관리자 화면 맨 위 **'저장 위치 점검'** 이 모두 `정상` 이면 설치가 끝난 것이다.
`확인 필요` 가 뜨면 거기 적힌 대로 고친다.

### Container Manager · Portainer 로 설치할 때

- **Synology Container Manager**: 프로젝트 → 생성 → 경로에 설치 폴더를 고르고
  "기존 docker-compose.yml 사용". `.env` 는 같은 폴더에 있어야 한다.
  compose·`.env` 를 바꾼 뒤에는 재시작이 아니라 **프로젝트 중지 → 빌드**로 컨테이너를 다시 만든다.
- **Portainer 스택**: 상대 경로(`./db`, `./files`)가 Portainer 내부 폴더를 가리키므로,
  스택 환경변수에 `RHWP_DB_DIR`, `RHWP_FILES_DIR` 를 **절대 경로**(`/volume1/docker/rhwp/db` 등)로 넣는다.

### 업데이트

```bash
cd /volume1/docker/rhwp
docker compose pull && docker compose up -d
```

사용자·문서는 `db/`, `files/` 에 있어 그대로 유지된다. 백업은 이 두 폴더를 평소 방식(Hyper Backup 등)으로 한다.

## 공유 폴더 마운트 (files/mount)

NAS 의 다른 폴더를 `files/mount/<이름>` 에 마운트하면, 편집기의 **☁ 서버에서 열기 / 서버에 저장** 창
맨 위에 `🏠 내 문서` 와 함께 **`🗂 <이름> (공유)`** 로 나타난다.

**1)** 설치 폴더에서 예시 파일을 복사한다:

```bash
cp docker-compose.override.yml.example docker-compose.override.yml
```

**2)** 붙일 폴더를 적는다. 왼쪽은 NAS 의 실제 폴더, 오른쪽은 `/files/mount/<보여줄 이름>`:

```yaml
services:
  rhwp-auth:
    volumes:
      - /volume1/문서/공용:/files/mount/공용          # 읽고 쓰기
      - /volume1/문서/양식:/files/mount/양식:ro       # 읽기 전용 (끝에 :ro)
```

**3)** 다시 만든다:

```bash
docker compose up -d
docker logs rhwp-auth 2>&1 | grep '공유 폴더'      # 공유 폴더 2개 [공용 양식]
```

알아둘 점:

| | |
|---|---|
| 누가 쓰나 | **로그인한 모든 사용자**가 보고 열고 저장할 수 있다. 공유 폴더 안의 파일 **삭제는 관리자만** |
| 읽기 전용 | `:ro` 로 붙인 폴더는 저장 버튼이 꺼지고 "여기에는 저장할 수 없음" 이 표시된다 |
| 하위 폴더 | 그대로 들어가서 열고 저장할 수 있다 (새 폴더는 File Station 에서 만든다) |
| 권한 | 그 NAS 폴더에 `.env` 의 PUID 사용자가 읽기(저장하려면 쓰기) 권한이 있어야 한다. DSM 공유폴더 권한에서 준다. 공유 폴더 안 파일의 소유자는 바꾸지 않는다 |
| 파일이 남는 이유 | override 파일은 `docker compose` 가 자동으로 함께 읽으므로, `docker-compose.yml` 을 새 버전으로 바꿔도 설정이 남는다 |
| Portainer | override 파일을 읽지 않는다 → 스택 편집기에서 rhwp-auth 의 `volumes:` 에 같은 줄을 직접 넣는다 |
| Container Manager | 빌드한 뒤 '저장 위치 점검'에 공유 폴더가 나오는지 확인한다. 안 나오면 override 파일이 읽히지 않은 것이므로 `docker-compose.yml` 의 rhwp-auth `volumes:` 에 직접 넣는다 |

- 호스트의 `files/mount/` 아래에 같은 이름의 빈 폴더(마운트 자리)가 생긴다. 그대로 두면 된다.
- `mount` 는 공유 폴더 자리로 예약된 이름이라 사용자 이름으로 쓸 수 없다.

## `.env` 설정

`.env.example` 을 `.env` 로 복사해 고친다. 값을 비우거나 줄을 지우면 기본값이 쓰인다.
고친 뒤에는 `docker compose up -d` 로 다시 만들어야 적용된다.

| 변수 | 기본값 | 설명 |
|---|---|---|
| `RHWP_PORT` | `17780` | 접속 포트 → `http://<NAS주소>:17780/rhwp/` |
| `RHWP_DB_DIR` | `./db` | 사용자 DB 폴더. **NAS 로컬 디스크**에 둘 것 (NFS·SMB 로 마운트한 폴더는 DB 가 깨질 수 있다) |
| `RHWP_FILES_DIR` | `./files` | 서버 저장 문서 폴더. NAS 공유폴더를 직접 가리켜도 된다 (예: `/volume1/문서/rhwp`) |
| `PUID` / `PGID` | `1000` / `1000` | `db/`·`files/` 와 저장되는 문서의 소유자. File Station·SMB 로 파일을 열고 고칠 NAS 사용자 (아래) |
| `RHWP_AUTH_ADMIN_USER` | `admin` | 첫 관리자 이름 — **사용자가 한 명도 없을 때 첫 실행에서만** 쓰인다 |
| `RHWP_AUTH_ADMIN_PASSWORD` | (자동 생성) | 첫 관리자 비밀번호 (8자 이상). 비우면 만들어서 로그에 한 번 출력 |
| `RHWP_AUTH_TITLE` | `RHWP` | 로그인 화면 제목 |
| `RHWP_AUTH_SESSION_HOURS` | `168` | 로그인 유지 시간(시간). 168 = 7일 |
| `RHWP_AUTH_COOKIE_SECURE` | `auto` | `auto`: 접속 방식에 맞춤 / `true`: HTTPS 로만 접속할 때 (리버스 프록시로 외부 HTTPS 를 쓸 때 권장. 대신 `http://IP:포트` 로는 로그인이 유지되지 않음) / `false`: HTTP 로만 쓸 때 |
| `RHWP_FILES_MAX_MB` | `100` | 서버에 저장할 문서 하나의 최대 크기(MB). 앞단 프록시의 업로드 제한도 이보다 커야 한다 |
| `RHWP_TAG` / `RHWP_AUTH_TAG` | `latest` | 이미지 버전. 특정 버전에 고정하려면 태그를 적는다 (예: `0.9.0-c9095c5-r6`, `0.4.1`) |

**PUID / PGID 확인** — NAS 에 SSH 로 접속해서:

```bash
id kim          # uid=1026(kim) gid=100(users)  →  PUID=1026, PGID=100
```

`0`(root)은 쓸 수 없다. rhwp-auth 는 시작할 때 `db/`·`files/` 와 사용자 폴더의 소유자를 이 값으로 맞추고
이 사용자로 실행된다.

**예시** (`/volume1/docker/rhwp` 에 설치, 문서는 `문서` 공유폴더에 저장):

```ini
RHWP_PORT=17780
RHWP_DB_DIR=/volume1/docker/rhwp/db
RHWP_FILES_DIR=/volume1/문서/rhwp
PUID=1026
PGID=100
RHWP_AUTH_ADMIN_USER=admin
RHWP_AUTH_ADMIN_PASSWORD=처음-쓸-비밀번호
RHWP_AUTH_COOKIE_SECURE=auto
```

---

사용자 관리, 외부 HTTPS(리버스 프록시) 연결, 문제 해결, 이전 버전에서 옮기기, 빌드·개발 내용은
**[docs/](docs/README.md)** 에 있다.

## 라이선스

이 저장소(패키징·rhwpweb-auth)는 [MIT](LICENSE). 이미지에 들어 있는 [RHWP](https://github.com/edwardkim/rhwp) 는
원저작자의 MIT 라이선스를 따르며, 이미지 안 `/usr/share/licenses/rhwp/LICENSE` 에 사본이 있다.
이 프로젝트는 RHWP 원저작자와 관계없는 비공식 패키지다.
