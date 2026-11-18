// rhwp-auth: RHWP 용 경량 로그인·사용자 관리 서비스.
//
// nginx 의 auth_request 로 /rhwp-auth/verify 를 호출해 세션을 확인하고,
// 로그인 화면·본인 비밀번호 변경·관리자 사용자 관리 화면을 제공한다.
//
//	rhwp-auth                       서버 실행 (기본)
//	rhwp-auth user list
//	rhwp-auth user add <이름> [--admin]   비밀번호는 프롬프트/표준입력
//	rhwp-auth user passwd <이름>
//	rhwp-auth user del <이름>
//	rhwp-auth user enable|disable <이름>
//	rhwp-auth user admin|unadmin <이름>
//	rhwp-auth healthcheck
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dataDir() string  { return env("RHWP_AUTH_DATA", "/data") }
func filesDir() string { return env("RHWP_AUTH_FILES", "/files") }

// openStore: db/users.db 를 연다. 사용자가 없고 이전 버전의 users.json 이 있으면 가져온다.
func openStore() *Store {
	st, err := OpenStore(filepath.Join(dataDir(), "users.db"))
	if err != nil {
		log.Fatalf("사용자 DB 열기 실패: %v", err)
	}
	legacy := filepath.Join(dataDir(), "users.json")
	if _, err := os.Stat(legacy); err == nil && st.Count() == 0 {
		n, err := st.ImportJSON(legacy)
		if err != nil {
			log.Fatalf("%s 가져오기 실패: %v", legacy, err)
		}
		if err := os.Rename(legacy, legacy+".imported"); err != nil {
			log.Printf("⚠ %s 이름 바꾸기 실패: %v", legacy, err)
		}
		log.Printf("이전 사용자 파일 %s 에서 %d명을 users.db 로 옮겼습니다 (원본은 users.json.imported)", legacy, n)
	}
	// 사용자를 만들면 서버 저장 폴더도 바로 만든다 (웹·CLI 공통)
	st.OnAdd = func(name string) { ensureUserDir(filesDir(), name) }
	return st
}

func main() {
	log.SetFlags(log.LstdFlags)
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "serve" {
		dropPrivileges(true)
		serve()
		return
	}
	switch args[0] {
	case "healthcheck":
		resp, err := http.Get("http://127.0.0.1" + listenPort() + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
	case "user":
		dropPrivileges(false)
		os.Exit(userCmd(args[1:]))
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "사용법: rhwp-auth [serve|user ...|healthcheck|version]")
		os.Exit(2)
	}
}

func listenPort() string {
	addr := env("RHWP_AUTH_LISTEN", ":8080")
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":8080"
}

func serve() {
	st := openStore()
	key, err := loadKey(filepath.Join(dataDir(), "secret.key"))
	if err != nil {
		log.Fatalf("세션 키: %v", err)
	}
	hours, err := strconv.Atoi(env("RHWP_AUTH_SESSION_HOURS", "168"))
	if err != nil || hours < 1 {
		log.Fatalf("RHWP_AUTH_SESSION_HOURS 는 1 이상의 정수여야 합니다")
	}
	bootstrap(st)
	for _, u := range st.List() { // 사용자 폴더가 없으면 만든다 (files 폴더를 새로 연결한 경우 등)
		ensureUserDir(filesDir(), u.Name)
	}
	maxMB, err := strconv.Atoi(env("RHWP_AUTH_FILES_MAX_MB", "100"))
	if err != nil || maxMB < 1 {
		log.Fatalf("RHWP_AUTH_FILES_MAX_MB 는 1 이상의 정수여야 합니다")
	}

	app := &App{
		store:        st,
		sess:         &Sessions{key: key, ttl: time.Duration(hours) * time.Hour},
		lim:          newLimiter(15 * time.Minute),
		title:        env("RHWP_AUTH_TITLE", "RHWP"),
		cookieSecure: env("RHWP_AUTH_COOKIE_SECURE", "auto"),
		files:        openFileStore(filesDir(), maxMB),
	}
	addr := env("RHWP_AUTH_LISTEN", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	for _, c := range checkStorage() {
		where := c.Where
		if !c.Mounted {
			where = "(마운트 안 됨)"
		}
		log.Printf("저장 위치: %s %s ← %s [%s] %s", c.Label, c.Target, where, c.Device, c.Warn)
	}
	log.Printf("rhwp-auth %s listening on %s (users=%d, session=%dh)", version, addr, st.Count(), hours)
	log.Fatal(srv.ListenAndServe())
}

// bootstrap: 사용자가 한 명도 없으면 첫 관리자를 만든다.
// RHWP_AUTH_ADMIN_USER / RHWP_AUTH_ADMIN_PASSWORD 가 있으면 그 값으로,
// 비밀번호가 없으면 임의 비밀번호를 만들어 로그에 한 번만 출력한다.
func bootstrap(st *Store) {
	if st.Count() > 0 {
		return
	}
	name := env("RHWP_AUTH_ADMIN_USER", "admin")
	pw := os.Getenv("RHWP_AUTH_ADMIN_PASSWORD")
	generated := pw == ""
	if generated {
		pw = randomPassword()
	}
	if err := st.Add(name, pw, true); err != nil {
		log.Fatalf("첫 관리자 생성 실패: %v", err)
	}
	if generated {
		log.Printf("==============================================================")
		log.Printf(" 첫 관리자 계정을 만들었습니다:  %s / %s", name, pw)
		log.Printf(" 로그인 후 /rhwp-auth/ 에서 비밀번호를 바꾸세요.")
		log.Printf("==============================================================")
	} else {
		log.Printf("첫 관리자 계정을 만들었습니다: %s (RHWP_AUTH_ADMIN_PASSWORD)", name)
	}
}

func readPassword(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "다시 입력: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", fmt.Errorf("비밀번호가 서로 다릅니다")
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("표준입력에서 비밀번호를 읽지 못했습니다")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func userCmd(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, "사용법: rhwp-auth user list | add <이름> [--admin] | passwd <이름> | del <이름> | enable|disable <이름> | admin|unadmin <이름>")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	st := openStore()
	if args[0] == "list" {
		fmt.Printf("%-20s %-6s %-6s %-17s\n", "NAME", "ADMIN", "STATE", "LAST LOGIN")
		for _, u := range st.List() {
			state := "active"
			if u.Disabled {
				state = "off"
			}
			last := "-"
			if u.LastLogin != nil {
				last = u.LastLogin.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%-20s %-6v %-6s %-17s\n", u.Name, u.Admin, state, last)
		}
		return 0
	}
	if len(args) < 2 {
		return usage()
	}
	name := args[1]
	var err error
	switch args[0] {
	case "add":
		var pw string
		if pw, err = readPassword("비밀번호: "); err == nil {
			err = st.Add(name, pw, len(args) > 2 && args[2] == "--admin")
		}
	case "passwd":
		var pw string
		if pw, err = readPassword("새 비밀번호: "); err == nil {
			err = st.SetPassword(name, pw)
		}
	case "del":
		err = st.Delete(name)
	case "enable", "disable":
		err = st.SetDisabled(name, args[0] == "disable")
	case "admin", "unadmin":
		err = st.SetAdmin(name, args[0] == "admin")
	default:
		return usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "오류:", err)
		return 1
	}
	fmt.Println("완료")
	return 0
}
