package main

// 저장 위치 점검: 컨테이너의 /data, /files (와 files/mount/*) 가 호스트의 어디에 연결됐는지
// /proc/self/mountinfo 로 알아내 관리자 화면·시작 로그에 보여준다.
// "저장했다는데 NAS 폴더에 안 보인다" 의 흔한 원인(Docker 볼륨, Portainer 스택의 상대 경로,
// 마운트 안 됨)을 바로 짚기 위해서다.
//
// 주의: mountinfo 의 경로는 "그 장치(파일시스템) 안에서의 경로" 다. Synology 에서
// /volume1/docker/rhwp/db 는 보통 /docker/rhwp/db 로 보인다 (장치 = volume1).

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type mountEntry struct {
	Target string // 컨테이너 안 경로
	Root   string // 장치 안 경로
	Source string // 장치
	FSType string
	RO     bool
}

type storageCheck struct {
	Label   string
	Target  string
	Mounted bool
	Where   string // 장치 안 경로 (호스트 쪽)
	Device  string
	RO      bool
	Kind    string // bind | volume | portainer | none
	Warn    string
}

// mountinfo 한 줄: ID PARENT MAJ:MIN ROOT TARGET OPTS [OPTIONAL…] - FSTYPE SOURCE SUPEROPTS
func parseMountinfo(text string) []mountEntry {
	var out []mountEntry
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		sep := -1
		for i, x := range f {
			if x == "-" {
				sep = i
				break
			}
		}
		if len(f) < 6 || sep < 6 || len(f) < sep+3 {
			continue
		}
		e := mountEntry{Root: unescapeMount(f[3]), Target: unescapeMount(f[4]), FSType: f[sep+1], Source: unescapeMount(f[sep+2])}
		for _, o := range strings.Split(f[5], ",") {
			if o == "ro" {
				e.RO = true
			}
		}
		out = append(out, e)
	}
	return out
}

// 공백 등은 \040 처럼 8진수 세 자리로 들어온다
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOct(s[i+1]) && isOct(s[i+2]) && isOct(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOct(c byte) bool { return c >= '0' && c <= '7' }

func classify(label, target string, mounts []mountEntry) storageCheck {
	c := storageCheck{Label: label, Target: target, Kind: "none"}
	for i := len(mounts) - 1; i >= 0; i-- { // 나중 마운트가 위에 덮인다
		m := mounts[i]
		if m.Target != target {
			continue
		}
		c.Mounted, c.Where, c.Device, c.RO = true, m.Root, m.Source, m.RO
		switch {
		case strings.Contains(m.Root, "/compose/"):
			c.Kind = "portainer"
			c.Warn = "Portainer 스택의 상대 경로로 보입니다. NAS 폴더가 아니라 Portainer 내부에 저장되고 있습니다. " +
				"스택 환경변수 RHWP_DB_DIR / RHWP_FILES_DIR 를 절대 경로(/volume1/…)로 지정하세요."
		case strings.Contains(m.Root, "/volumes/") && (strings.HasSuffix(m.Root, "/_data") || strings.Contains(m.Root, "/_data/")):
			c.Kind = "volume"
			c.Warn = volumeWarn(m.Root)
		default:
			c.Kind = "bind"
		}
		return c
	}
	c.Warn = "마운트되어 있지 않습니다. 컨테이너 안에만 저장되어 컨테이너를 다시 만들면 사라집니다. docker-compose.yml 의 volumes 를 확인하세요."
	return c
}

// volumeName: ".../volumes/<이름>/_data" 에서 <이름>
func volumeName(root string) string {
	i := strings.LastIndex(root, "/volumes/")
	if i < 0 {
		return ""
	}
	rest := root[i+len("/volumes/"):]
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// volumeWarn: 예전 compose(0.2.x, 볼륨 rhwp-auth-data / rhwp-files)로 만든 컨테이너인지 짚어 준다.
// 예전 compose 는 RHWP_DB_DIR / RHWP_FILES_DIR 를 읽지 않으므로 .env 를 고쳐도 소용없다.
func volumeWarn(root string) string {
	v := volumeName(root)
	for _, old := range []string{"rhwp-auth-data", "rhwp-files"} {
		if v == old || strings.HasSuffix(v, "_"+old) {
			proj := strings.TrimSuffix(strings.TrimSuffix(v, old), "_")
			w := "Docker 볼륨 '" + v + "' 에 저장되고 있습니다 (NAS 폴더에서 보이지 않음). " +
				"이 컨테이너는 예전 docker-compose.yml(볼륨 방식)로 만들어졌습니다"
			if proj != "" {
				w += " (compose 프로젝트 이름: " + proj + ")"
			}
			return w + ". 예전 compose 는 .env 의 RHWP_DB_DIR / RHWP_FILES_DIR 를 읽지 않습니다. " +
				"그 프로젝트의 compose 내용을 새 docker-compose.yml 로 바꾼 뒤 컨테이너를 다시 만들어야 합니다 " +
				"(재시작만으로는 바뀌지 않음 — Container Manager 는 프로젝트 중지 → 편집 → 빌드). " +
				"기존 데이터는 README '이전 버전(Docker 볼륨)에서 옮기기' 의 scripts/migrate-volumes.sh 로 옮기세요."
		}
	}
	return "Docker 볼륨 '" + v + "' 에 저장되고 있습니다 (NAS 폴더에서 보이지 않음). " +
		"compose 의 volumes 가 폴더 경로(${RHWP_DB_DIR:-./db}, ${RHWP_FILES_DIR:-./files})인지 확인하고 컨테이너를 다시 만드세요."
}

// checkStorage: /data, /files, files/mount/* 의 연결 상태.
func checkStorage() []storageCheck {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	mounts := parseMountinfo(string(b))
	out := []storageCheck{
		classify("사용자 DB (db 폴더)", filepath.Clean(dataDir()), mounts),
		classify("서버 저장 문서 (files 폴더)", filepath.Clean(filesDir()), mounts),
	}
	md := filepath.Join(filepath.Clean(filesDir()), mountDir)
	for _, m := range mounts {
		if filepath.Dir(m.Target) == md {
			c := classify("공유 폴더 "+filepath.Base(m.Target), m.Target, mounts)
			if c.Kind == "portainer" || c.Kind == "volume" {
				c.Warn = "" // 공유 폴더가 볼륨이어도 의도한 것일 수 있다
			}
			out = append(out, c)
		}
	}
	return out
}
