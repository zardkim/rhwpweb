package main

import (
	"crypto/rand"
	"embed"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

const prefix = "/rhwp-auth"

//go:embed templates/*.html
var tmplFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"fmtTime": func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"fmtDate": func(t time.Time) string { return t.Local().Format("2006-01-02") },
}).ParseFS(tmplFS, "templates/*.html"))

type App struct {
	store        *Store
	sess         *Sessions
	lim          *limiter
	title        string
	cookieSecure string     // auto | true | false
	files        *FileStore // nil 이면 서버 저장 기능 꺼짐
}

type page struct {
	Title   string
	Prefix  string
	User    *User
	CSRF    string
	Msg     string
	Err     string
	Secret  string // 새로 생성된 비밀번호 (한 번만 표시)
	RD      string
	Users   []User
	Name    string
	AppPath string
	Storage []storageCheck
	Version string
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET "+prefix+"/verify", a.verify)
	mux.HandleFunc("GET "+prefix+"/login", a.loginForm)
	mux.HandleFunc("POST "+prefix+"/login", a.loginPost)
	mux.HandleFunc("POST "+prefix+"/logout", a.logout)
	mux.HandleFunc("GET "+prefix+"/{$}", a.account)
	mux.HandleFunc("POST "+prefix+"/password", a.changePassword)
	mux.HandleFunc("GET "+prefix+"/admin", a.admin)
	mux.HandleFunc("POST "+prefix+"/admin", a.adminPost)
	mux.HandleFunc("GET "+prefix+"/api/me", a.me)
	mux.HandleFunc("GET "+prefix+"/api/fs", a.fsList)
	mux.HandleFunc("GET "+prefix+"/api/fs/file", a.fsFileGet)
	mux.HandleFunc("PUT "+prefix+"/api/fs/file", a.fsFilePut)
	mux.HandleFunc("DELETE "+prefix+"/api/fs/file", a.fsFileDelete)
	mux.HandleFunc("GET "+prefix+"/api/files", a.filesList)
	mux.HandleFunc("GET "+prefix+"/api/files/{name}", a.fileGet)
	mux.HandleFunc("PUT "+prefix+"/api/files/{name}", a.filePut)
	mux.HandleFunc("DELETE "+prefix+"/api/files/{name}", a.fileDelete)
	mux.HandleFunc("GET "+prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
	})
	return secureHeaders(mux)
}

func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' data:; "+
				"form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.ServeHTTP(w, r)
	})
}

// ---- 세션 헬퍼 ----------------------------------------------------------

// current 는 유효한 세션과 사용자를 돌려준다. 사용자 삭제·비활성화·비밀번호 변경 시 무효.
func (a *App) current(r *http.Request) (sessionData, User, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return sessionData{}, User{}, false
	}
	d, ok := a.sess.Parse(c.Value)
	if !ok {
		return sessionData{}, User{}, false
	}
	u, ok := a.store.Get(d.U)
	if !ok || u.Disabled || u.SessVer != d.V {
		return sessionData{}, User{}, false
	}
	return d, u, true
}

func (a *App) secure(r *http.Request) bool {
	switch a.cookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *App) setSession(w http.ResponseWriter, r *http.Request, u User, nonce string) {
	v, exp := a.sess.Issue(u, nonce)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: v, Path: "/", Expires: exp,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(ip)
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// safeRD: 로그인 후 이동할 경로. 같은 사이트의 /rhwp/ 또는 /rhwp-auth/ 아래만 허용 (open redirect 방지).
func safeRD(rd string) string {
	if rd == "" || strings.HasPrefix(rd, "//") || strings.Contains(rd, "\\") || strings.ContainsAny(rd, "\r\n") {
		return "/rhwp/"
	}
	if strings.HasPrefix(rd, "/rhwp/") || rd == "/rhwp" || strings.HasPrefix(rd, prefix+"/") {
		if strings.HasPrefix(rd, prefix+"/login") || strings.HasPrefix(rd, prefix+"/verify") {
			return "/rhwp/"
		}
		return rd
	}
	return "/rhwp/"
}

func (a *App) render(w http.ResponseWriter, name string, p page) {
	p.Title, p.Prefix, p.AppPath, p.Version = a.title, prefix, "/rhwp/", version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, p); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func randomPassword() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 14)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

// ---- 핸들러 -------------------------------------------------------------

// verify: nginx auth_request 용. 200 = 통과, 401 = 로그인 필요.
func (a *App) verify(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.current(r); ok {
		w.Header().Set("X-Auth-User", u.Name)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) {
	rd := safeRD(r.URL.Query().Get("rd"))
	if _, _, ok := a.current(r); ok {
		http.Redirect(w, r, rd, http.StatusFound)
		return
	}
	a.render(w, "login.html", page{RD: rd})
}

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("username"))
	pw := r.PostFormValue("password")
	rd := safeRD(r.PostFormValue("rd"))
	ip := clientIP(r)
	keyU, keyIP := "u:"+strings.ToLower(name), "ip:"+ip

	if d := max(a.lim.Blocked(keyU), a.lim.Blocked(keyIP)); d > 0 {
		w.WriteHeader(http.StatusTooManyRequests)
		a.render(w, "login.html", page{RD: rd, Name: name,
			Err: fmt.Sprintf("로그인 시도가 너무 많습니다. %d분 후 다시 시도하세요.", int(d.Minutes())+1)})
		return
	}
	u, ok := a.store.Check(name, pw)
	if !ok {
		a.lim.Fail(keyU, 5)
		a.lim.Fail(keyIP, 20)
		log.Printf("login fail user=%q ip=%s", name, ip)
		w.WriteHeader(http.StatusUnauthorized)
		a.render(w, "login.html", page{RD: rd, Name: name, Err: "사용자 이름 또는 비밀번호가 올바르지 않습니다."})
		return
	}
	a.lim.Reset(keyU)
	a.store.TouchLogin(u.Name)
	a.setSession(w, r, u, "")
	log.Printf("login ok user=%q ip=%s", u.Name, ip)
	// 바로 이동하지 않고 한 장을 거친다: 이 브라우저에 앞 사용자의 자동 저장 복구본·최근 문서가
	// 남아 있으면 비운 뒤 rd 로 이동한다 (templates/loggedin.html).
	a.render(w, "loggedin.html", page{User: &u, Name: u.Name, RD: rd})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.current(r)
	if ok && !a.sess.CheckCSRF(d, r.PostFormValue("csrf")) {
		http.Error(w, "bad csrf token", http.StatusForbidden)
		return
	}
	if ok {
		log.Printf("logout user=%q", u.Name)
	}
	a.clearSession(w, r)
	a.render(w, "loggedout.html", page{})
}

func (a *App) requireLogin(w http.ResponseWriter, r *http.Request) (sessionData, User, bool) {
	d, u, ok := a.current(r)
	if !ok {
		http.Redirect(w, r, prefix+"/login?rd="+r.URL.Path, http.StatusFound)
	}
	return d, u, ok
}

func (a *App) account(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.requireLogin(w, r)
	if !ok {
		return
	}
	a.render(w, "account.html", page{User: &u, CSRF: a.sess.CSRF(d)})
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.requireLogin(w, r)
	if !ok {
		return
	}
	if !a.sess.CheckCSRF(d, r.PostFormValue("csrf")) {
		http.Error(w, "bad csrf token", http.StatusForbidden)
		return
	}
	p := page{User: &u, CSRF: a.sess.CSRF(d)}
	cur, n1, n2 := r.PostFormValue("current"), r.PostFormValue("new1"), r.PostFormValue("new2")
	if _, ok := a.store.Check(u.Name, cur); !ok {
		p.Err = "현재 비밀번호가 올바르지 않습니다."
	} else if n1 != n2 {
		p.Err = "새 비밀번호가 서로 다릅니다."
	} else if err := a.store.SetPassword(u.Name, n1); err != nil {
		p.Err = err.Error()
	} else {
		// 세션 버전이 올라갔으므로 현재 브라우저에는 새 세션을 발급한다 (다른 기기는 로그아웃됨).
		u, _ = a.store.Get(u.Name)
		a.setSession(w, r, u, d.N)
		p.User = &u
		p.Msg = "비밀번호를 변경했습니다. 다른 기기의 로그인은 해제되었습니다."
		log.Printf("password changed user=%q", u.Name)
	}
	if p.Err != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	a.render(w, "account.html", p)
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) (sessionData, User, bool) {
	d, u, ok := a.requireLogin(w, r)
	if !ok {
		return d, u, false
	}
	if !u.Admin {
		http.Error(w, "관리자만 접근할 수 있습니다", http.StatusForbidden)
		return d, u, false
	}
	return d, u, true
}

func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	a.render(w, "admin.html", page{User: &u, CSRF: a.sess.CSRF(d), Users: a.store.List(), Storage: checkStorage()})
}

func (a *App) adminPost(w http.ResponseWriter, r *http.Request) {
	d, me, ok := a.requireAdmin(w, r)
	if !ok {
		return
	}
	if !a.sess.CheckCSRF(d, r.PostFormValue("csrf")) {
		http.Error(w, "bad csrf token", http.StatusForbidden)
		return
	}
	op := r.PostFormValue("op")
	name := strings.TrimSpace(r.PostFormValue("name"))
	p := page{User: &me, CSRF: a.sess.CSRF(d)}

	self := name == me.Name
	var err error
	switch op {
	case "create":
		pw := r.PostFormValue("password")
		generated := pw == ""
		if generated {
			pw = randomPassword()
		}
		if err = a.store.Add(name, pw, r.PostFormValue("admin") == "on"); err == nil {
			p.Msg = fmt.Sprintf("사용자 '%s' 를 추가했습니다.", name)
			if generated {
				p.Secret = pw
			}
		}
	case "reset":
		pw := r.PostFormValue("password")
		generated := pw == ""
		if generated {
			pw = randomPassword()
		}
		if err = a.store.SetPassword(name, pw); err == nil {
			p.Msg = fmt.Sprintf("'%s' 의 비밀번호를 재설정했습니다. 기존 로그인은 해제됩니다.", name)
			if generated {
				p.Secret = pw
			}
			if self {
				u, _ := a.store.Get(me.Name)
				a.setSession(w, r, u, d.N)
			}
		}
	case "disable", "enable":
		if self {
			err = fmt.Errorf("자기 자신은 비활성화할 수 없습니다")
		} else if err = a.store.SetDisabled(name, op == "disable"); err == nil {
			p.Msg = fmt.Sprintf("'%s' 를 %s했습니다.", name, map[string]string{"disable": "비활성화", "enable": "활성화"}[op])
		}
	case "admin", "unadmin":
		if self {
			err = fmt.Errorf("자기 자신의 관리자 권한은 바꿀 수 없습니다")
		} else if err = a.store.SetAdmin(name, op == "admin"); err == nil {
			p.Msg = fmt.Sprintf("'%s' 의 관리자 권한을 %s했습니다.", name, map[string]string{"admin": "부여", "unadmin": "해제"}[op])
		}
	case "delete":
		if self {
			err = fmt.Errorf("자기 자신은 삭제할 수 없습니다")
		} else if err = a.store.Delete(name); err == nil {
			p.Msg = fmt.Sprintf("'%s' 를 삭제했습니다.", name)
		}
	default:
		err = fmt.Errorf("알 수 없는 작업")
	}
	if err != nil {
		p.Err = err.Error()
		w.WriteHeader(http.StatusBadRequest)
	} else {
		log.Printf("admin %s target=%q by=%q", op, name, me.Name)
	}
	p.Users = a.store.List()
	p.Storage = checkStorage()
	a.render(w, "admin.html", p)
}
