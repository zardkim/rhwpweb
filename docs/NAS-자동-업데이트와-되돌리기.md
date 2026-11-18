# NAS 자동 업데이트와 되돌리기

빌드 서버가 매일 04:00 에 업스트림 새 릴리스를 빌드해 Docker Hub `latest` 로 올린다 ([빌드와 배포](개발-빌드와-배포.md)).
NAS 는 05:00 에 받아서 반영한다.

## 작업 스케줄러

DSM **제어판 → 작업 스케줄러 → 생성 → 예약된 작업 → 사용자 정의 스크립트**

| 항목 | 값 |
|---|---|
| 사용자 | `root` (docker 실행 권한) |
| 일정 | 매일 05:00 (빌드 이후) |

```bash
cd /volume1/docker/rhwp            # docker-compose.yml 이 있는 폴더
docker compose pull -q && docker compose up -d && docker image prune -f
```

- `latest` 가 바뀌지 않았으면 `up -d` 는 아무것도 다시 만들지 않는다 (사용자·문서는 db/·files/ 에 있어 유지).
- DSM 에서 `docker compose` 가 없다고 나오면 `docker-compose` 로 바꾼다.
- **Portainer 스택**이면 스택 화면의 *Update the stack* → *Re-pull image* (자동화는 스택 Webhook 을 `curl -X POST <webhook URL>` 로 호출).

## 문제가 생긴 버전에서 되돌리기

`.env` 에 직전 태그를 한 줄 **추가**해 고정하고 다시 올린다
(`>` 로 덮어쓰면 다른 설정이 지워지니 `>>` 또는 편집기로). `.env` 에 두어야 05:00 작업도 같은 값을 읽는다.
고정해 두는 동안에는 자동 반영이 멈추고, 지우면 다시 latest 를 따라간다.

```bash
echo 'RHWP_TAG=0.9.0-c9095c5-r6' >> /volume1/docker/rhwp/.env   # Docker Hub 의 이전 태그 중 하나
cd /volume1/docker/rhwp && docker compose up -d
```

rhwp-auth 는 `RHWP_AUTH_TAG` 로 같은 방법을 쓴다.
