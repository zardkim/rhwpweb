package main

// 컨테이너는 root 로 시작해 db·files 폴더 소유자를 PUID:PGID 로 맞춘 뒤, 그 사용자로 내려가 실행한다.
// (호스트의 ./db, ./files 를 Docker 가 root 소유로 만들어도 동작하고, 저장된 파일은 NAS 사용자
//  소유가 되어 File Station·SMB 에서 그대로 열고 고칠 수 있다.)
//
// root 가 아니게 시작했으면(예: compose 의 user: 지정, 이전 버전 compose) 아무것도 하지 않는다.

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func ids() (int, int, error) {
	uid, err1 := strconv.Atoi(env("PUID", "1000"))
	gid, err2 := strconv.Atoi(env("PGID", "1000"))
	if err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return 0, 0, fmt.Errorf("PUID/PGID 는 숫자여야 합니다 (현재 %q:%q)", os.Getenv("PUID"), os.Getenv("PGID"))
	}
	if uid == 0 {
		return 0, 0, fmt.Errorf("PUID=0(root) 으로는 실행하지 않습니다")
	}
	return uid, gid, nil
}

func chownIfNeeded(path string, uid, gid int) {
	st, err := os.Lstat(path)
	if err != nil {
		return
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok && int(s.Uid) == uid && int(s.Gid) == gid {
		return
	}
	if err := os.Lchown(path, uid, gid); err != nil {
		log.Printf("⚠ %s 소유자를 %d:%d 로 바꾸지 못했습니다: %v", path, uid, gid, err)
	}
}

// fixOwnership: db 폴더는 통째로, files 폴더는 최상위와 사용자 폴더(한 단계)만 맞춘다.
// 사용자 폴더 안의 파일은 건드리지 않는다 — NAS 에서 직접 넣은 파일의 소유자·ACL 을 바꾸지 않기 위해.
func fixOwnership(uid, gid int) {
	data := dataDir()
	_ = filepath.WalkDir(data, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			chownIfNeeded(p, uid, gid)
		}
		return nil
	})
	files := filesDir()
	if files == "" || files == "off" {
		return
	}
	if st, err := os.Stat(files); err != nil || !st.IsDir() {
		return
	}
	chownIfNeeded(files, uid, gid)
	entries, _ := os.ReadDir(files)
	for _, e := range entries {
		if e.IsDir() && validUserDir(e.Name()) {
			chownIfNeeded(filepath.Join(files, e.Name()), uid, gid)
		}
	}
}

// dropPrivileges: root 면 (fix 가 참일 때 소유자를 맞추고) PUID:PGID 로 내려간다.
// 서버뿐 아니라 CLI(docker exec … rhwp-auth user …)도 반드시 거친다 — root 로 DB 를 열면
// WAL 파일이 root 소유로 생겨 서버가 쓰지 못하게 된다.
func dropPrivileges(fix bool) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, err := ids()
	if err != nil {
		log.Fatalf("%v", err)
	}
	if fix {
		fixOwnership(uid, gid)
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		log.Fatalf("setgroups: %v", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		log.Fatalf("setgid(%d): %v", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		log.Fatalf("setuid(%d): %v", uid, err)
	}
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		log.Fatalf("root 권한을 내려놓지 못했습니다")
	}
}
