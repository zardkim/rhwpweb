package main

// 서버 저장소 (서버 저장/불러오기).
//
// 경로는 가상 경로로 주고받는다:
//
//	""                         최상위: '내 문서' + 공유(마운트) 폴더 목록
//	my[/하위폴더…][/파일]       files/<사용자>/…        — 본인만
//	mount/<이름>[/…][/파일]     files/mount/<이름>/…    — 로그인한 모든 사용자 (NAS 의 다른 폴더를 마운트)
//
//	GET    /rhwp-auth/api/fs?path=<폴더>        목록 (+ CSRF 토큰, 쓰기 가능 여부)
//	GET    /rhwp-auth/api/fs/file?path=<파일>   받기 (ETag)
//	PUT    /rhwp-auth/api/fs/file?path=<파일>   저장. If-Match: <etag> → 그 사이 바뀌었으면 412,
//	                                            If-None-Match: * → 이미 있으면 412
//	DELETE /rhwp-auth/api/fs/file?path=<파일>   삭제 (공유 폴더는 관리자만)
//	GET    /rhwp-auth/api/me                    로그인 사용자·관리자 여부·CSRF
//
// 이전 API(/rhwp-auth/api/files[/이름])는 my/ 로 그대로 동작한다 (웹·인증 이미지 버전이 잠깐 어긋나도 되게).
// 모든 파일 접근은 os.Root 를 거쳐 저장소(또는 그 마운트) 밖으로 벗어날 수 없다.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	csrfHeader  = "X-RHWP-CSRF"
	mountDir    = "mount" // files/mount/<이름> = 공유(마운트) 폴더
	maxEntries  = 3000
	maxSegments = 32
)

type FileStore struct {
	dir     string
	maxSize int64
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
}

type entry struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Type     string     `json:"type"` // space | dir | file
	Size     int64      `json:"size,omitempty"`
	MTime    *time.Time `json:"mtime,omitempty"`
	ETag     string     `json:"etag,omitempty"`
	Writable bool       `json:"writable,omitempty"` // space 일 때
}

type fileInfo struct {
	Name  string    `json:"name"`
	Path  string    `json:"path,omitempty"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	ETag  string    `json:"etag"`
}

var (
	errBadFileName = errors.New("파일 이름이 올바르지 않습니다 (.hwp, .hwpx, .hml 만, 특수문자 \\ / : * ? \" < > | 제외)")
	errBadPath     = errors.New("경로가 올바르지 않습니다")
	errNoStorage   = errors.New("서버 저장소를 사용할 수 없습니다")
	errReadOnly    = errors.New("읽기 전용 폴더입니다. 다른 폴더에 저장하세요")
	errNoPerm      = errors.New("이 폴더에 쓸 권한이 없습니다 (NAS 폴더 권한과 PUID/PGID 를 확인하세요)")
	errAdminOnly   = errors.New("공유 폴더의 파일은 관리자만 삭제할 수 있습니다")
	errNotHere     = errors.New("여기에는 저장할 수 없습니다. '내 문서' 또는 공유 폴더 안을 고르세요")
)

var allowedExt = []string{".hwp", ".hwpx", ".hml"}

func hasDocExt(name string) bool {
	lower := strings.ToLower(name)
	for _, ext := range allowedExt {
		if strings.HasSuffix(lower, ext) && len(lower) > len(ext) {
			return true
		}
	}
	return false
}

// validSegment: 읽을 때 쓰는 느슨한 검사. NAS 에 이미 있는 폴더·파일 이름을 최대한 받아들이되
// 경로 조작(.., 구분자, 제어문자)과 숨김·시스템 항목(., @eaDir, #recycle)은 막는다.
func validSegment(s string) bool {
	if s == "" || len(s) > 255 || !utf8.ValidString(s) || s == "." || s == ".." {
		return false
	}
	if s[0] == '.' || s[0] == '@' || s[0] == '#' {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// validFileName: 새로 저장할 때의 엄격한 검사 (SMB 에서도 문제없는 이름).
func validFileName(name string) bool {
	if !validSegment(name) || len(name) > 200 || strings.HasSuffix(name, " ") || strings.HasPrefix(name, " ") {
		return false
	}
	if strings.ContainsAny(name, `:*?"<>|`) {
		return false
	}
	return hasDocExt(name)
}

// validUserDir: 사용자 이름을 폴더 이름으로 쓸 수 있는지 (".." 와 예약어 mount 차단).
func validUserDir(name string) bool {
	return nameRe.MatchString(name) && !strings.HasPrefix(name, ".") && !strings.EqualFold(name, mountDir)
}

func etagOf(st fs.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, st.ModTime().UnixNano(), st.Size())
}

func toInfo(st fs.FileInfo, vpath string) fileInfo {
	return fileInfo{Name: st.Name(), Path: vpath, Size: st.Size(), MTime: st.ModTime().UTC(), ETag: etagOf(st)}
}

func splitPath(p string) ([]string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil, nil
	}
	parts := strings.Split(p, "/")
	if len(parts) > maxSegments {
		return nil, errBadPath
	}
	for _, s := range parts {
		if !validSegment(s) {
			return nil, errBadPath
		}
	}
	return parts, nil
}

func (fsx *FileStore) lock(key string) *sync.Mutex {
	fsx.mu.Lock()
	defer fsx.mu.Unlock()
	if fsx.locks == nil {
		fsx.locks = map[string]*sync.Mutex{}
	}
	m, ok := fsx.locks[key]
	if !ok {
		m = &sync.Mutex{}
		fsx.locks[key] = m
	}
	return m
}

// target: 가상 경로가 가리키는 곳.
type target struct {
	root    *os.Root // 공간의 뿌리 (files/<사용자> 또는 files/mount)
	rel     []string // 뿌리 아래 상대 경로
	shared  bool     // 공유(마운트) 공간
	lockKey string
	abs     string // 컨테이너 안 절대 경로 (쓰기 가능 여부 확인용)
}

func (t *target) Close() {
	if t.root != nil {
		t.root.Close()
	}
}

func (t *target) relPath() string {
	if len(t.rel) == 0 {
		return "."
	}
	return strings.Join(t.rel, "/")
}

// resolve: parts[0] 이 my 또는 mount 여야 한다. mount 는 mount/<이름> 아래만 (files/mount 자체는 X).
func (fsx *FileStore) resolve(user string, parts []string) (*target, error) {
	if len(parts) == 0 {
		return nil, errBadPath
	}
	switch parts[0] {
	case "my":
		if !validUserDir(user) {
			return nil, errBadName
		}
		top, err := os.OpenRoot(fsx.dir)
		if err != nil {
			log.Printf("files: 저장소 %s 열기 실패: %v", fsx.dir, err)
			return nil, errNoStorage
		}
		defer top.Close()
		if err := top.Mkdir(user, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			log.Printf("files: %s/%s 만들기 실패: %v", fsx.dir, user, err)
			return nil, errNoStorage
		}
		r, err := top.OpenRoot(user)
		if err != nil {
			return nil, errNoStorage
		}
		return &target{root: r, rel: parts[1:], lockKey: "u:" + user,
			abs: filepath.Join(append([]string{fsx.dir, user}, parts[1:]...)...)}, nil
	case mountDir:
		if len(parts) < 2 {
			return nil, errNotHere
		}
		r, err := os.OpenRoot(filepath.Join(fsx.dir, mountDir))
		if err != nil {
			return nil, fs.ErrNotExist
		}
		return &target{root: r, rel: parts[1:], shared: true, lockKey: "mount",
			abs: filepath.Join(append([]string{fsx.dir, mountDir}, parts[1:]...)...)}, nil
	}
	return nil, errBadPath
}

func writable(abs string) bool { return syscall.Access(abs, 2 /* W_OK */) == nil }

// spaces: 최상위 목록 — 내 문서 + files/mount 아래 폴더들.
func (fsx *FileStore) spaces() []entry {
	out := []entry{{Name: "내 문서", Path: "my", Type: "space", Writable: true}}
	ents, err := os.ReadDir(filepath.Join(fsx.dir, mountDir))
	if err != nil {
		return out
	}
	for _, e := range ents {
		n := e.Name()
		if !validSegment(n) {
			continue
		}
		if !e.IsDir() {
			if st, err := os.Stat(filepath.Join(fsx.dir, mountDir, n)); err != nil || !st.IsDir() {
				continue
			}
		}
		out = append(out, entry{Name: n, Path: mountDir + "/" + n, Type: "space",
			Writable: writable(filepath.Join(fsx.dir, mountDir, n))})
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return out[1+i].Name < out[1+j].Name })
	return out
}

// List: 폴더 목록. 폴더 먼저(이름순), 문서는 최근 수정 순. 문서가 아닌 파일은 숨긴다.
func (fsx *FileStore) List(user, vpath string) ([]entry, bool, error) {
	parts, err := splitPath(vpath)
	if err != nil {
		return nil, false, err
	}
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == mountDir) {
		sp := fsx.spaces()
		if len(parts) == 1 {
			sp = sp[1:]
		}
		return sp, false, nil
	}
	t, err := fsx.resolve(user, parts)
	if err != nil {
		return nil, false, err
	}
	defer t.Close()
	d, err := t.root.Open(t.relPath())
	if err != nil {
		return nil, false, err
	}
	defer d.Close()
	ents, err := d.ReadDir(maxEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	base := strings.Join(parts, "/")
	var dirs, files []entry
	for _, e := range ents {
		n := e.Name()
		if !validSegment(n) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 { // 링크는 대상 종류로 판단 (밖을 가리키면 열 때 막힌다)
			if st, err := t.root.Stat(t.relPath() + "/" + n); err == nil {
				info, isDir = st, st.IsDir()
			} else {
				continue
			}
		}
		if isDir {
			dirs = append(dirs, entry{Name: n, Path: base + "/" + n, Type: "dir"})
		} else if info.Mode().IsRegular() && hasDocExt(n) {
			mt := info.ModTime().UTC()
			files = append(files, entry{Name: n, Path: base + "/" + n, Type: "file", Size: info.Size(), MTime: &mt, ETag: etagOf(info)})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].MTime.After(*files[j].MTime) })
	out := append(append(make([]entry, 0, len(dirs)+len(files)), dirs...), files...)
	return out, writable(t.abs), nil
}

func (fsx *FileStore) Open(user, vpath string) (*os.File, fs.FileInfo, error) {
	parts, err := splitPath(vpath)
	if err != nil || len(parts) < 2 || !hasDocExt(parts[len(parts)-1]) {
		return nil, nil, errBadFileName
	}
	t, err := fsx.resolve(user, parts)
	if err != nil {
		return nil, nil, err
	}
	defer t.Close()
	f, err := t.root.Open(t.relPath())
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, fs.ErrNotExist
	}
	return f, st, nil
}

type precondition struct {
	ifMatch     string // 이 ETag 일 때만 덮어쓴다
	ifNoneMatch bool   // 없을 때만 만든다
}

var errPrecondition = errors.New("precondition failed")

func mapWriteErr(err error) error {
	switch {
	case errors.Is(err, syscall.EROFS):
		return errReadOnly
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return errNoPerm
	}
	return err
}

// Save: 같은 폴더의 임시 파일에 쓴 뒤 rename 한다 (도중에 끊겨도 기존 파일은 온전).
// 폴더는 미리 있어야 한다 ('내 문서' 뿌리는 자동으로 만든다).
func (fsx *FileStore) Save(user, vpath string, body io.Reader, pre precondition) (fileInfo, bool, error) {
	parts, err := splitPath(vpath)
	if err != nil || len(parts) < 2 {
		return fileInfo{}, false, errBadFileName
	}
	name := parts[len(parts)-1]
	if !validFileName(name) {
		return fileInfo{}, false, errBadFileName
	}
	if parts[0] == mountDir && len(parts) < 3 {
		return fileInfo{}, false, errNotHere
	}
	t, err := fsx.resolve(user, parts)
	if err != nil {
		return fileInfo{}, false, err
	}
	defer t.Close()
	lk := fsx.lock(t.lockKey)
	lk.Lock()
	defer lk.Unlock()

	dir := t.root
	if len(t.rel) > 1 {
		sub, err := t.root.OpenRoot(strings.Join(t.rel[:len(t.rel)-1], "/"))
		if err != nil {
			return fileInfo{}, false, err
		}
		defer sub.Close()
		dir = sub
	}

	cur, statErr := dir.Stat(name)
	exists := statErr == nil
	if exists && !cur.Mode().IsRegular() {
		return fileInfo{}, false, errBadFileName
	}
	if pre.ifNoneMatch && exists {
		return toInfo(cur, vpath), false, errPrecondition
	}
	if pre.ifMatch != "" && (!exists || etagOf(cur) != pre.ifMatch) {
		if exists {
			return toInfo(cur, vpath), false, errPrecondition
		}
		return fileInfo{}, false, errPrecondition
	}

	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	tmp := ".upload-" + hex.EncodeToString(rb) + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		log.Printf("files: 임시 파일 만들기 실패 user=%q path=%q: %v", user, vpath, err)
		return fileInfo{}, false, mapWriteErr(err)
	}
	_, err = io.Copy(f, body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = dir.Rename(tmp, name)
	}
	if err != nil {
		_ = dir.Remove(tmp)
		return fileInfo{}, false, mapWriteErr(err)
	}
	st, err := dir.Stat(name)
	if err != nil {
		return fileInfo{}, false, err
	}
	return toInfo(st, vpath), !exists, nil
}

func (fsx *FileStore) Delete(user, vpath, ifMatch string, admin bool) error {
	parts, err := splitPath(vpath)
	if err != nil || len(parts) < 2 || !hasDocExt(parts[len(parts)-1]) {
		return errBadFileName
	}
	t, err := fsx.resolve(user, parts)
	if err != nil {
		return err
	}
	defer t.Close()
	if t.shared && !admin {
		return errAdminOnly
	}
	lk := fsx.lock(t.lockKey)
	lk.Lock()
	defer lk.Unlock()
	st, err := t.root.Lstat(t.relPath())
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errBadFileName
	}
	if ifMatch != "" && etagOf(st) != ifMatch {
		return errPrecondition
	}
	return mapWriteErr(t.root.Remove(t.relPath()))
}

// ---- HTTP ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// apiUser: API 는 로그인 화면으로 보내지 않고 401 JSON 으로 답한다.
func (a *App) apiUser(w http.ResponseWriter, r *http.Request, mutating bool) (sessionData, User, bool) {
	d, u, ok := a.current(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "로그인이 필요합니다")
		return d, User{}, false
	}
	if mutating && !a.sess.CheckCSRF(d, r.Header.Get(csrfHeader)) {
		jsonErr(w, http.StatusForbidden, "bad csrf token")
		return d, User{}, false
	}
	if a.files == nil {
		jsonErr(w, http.StatusServiceUnavailable, errNoStorage.Error())
		return d, User{}, false
	}
	return d, u, true
}

func (a *App) fileErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadFileName), errors.Is(err, errBadPath), errors.Is(err, errNotHere), errors.Is(err, errBadName):
		jsonErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		jsonErr(w, http.StatusNotFound, "파일이나 폴더가 없습니다")
	case errors.Is(err, errReadOnly), errors.Is(err, errNoPerm), errors.Is(err, errAdminOnly):
		jsonErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, errNoStorage):
		jsonErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			jsonErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("파일이 너무 큽니다 (최대 %dMB)", a.files.maxSize>>20))
			return
		}
		if strings.Contains(err.Error(), "escapes from parent") || strings.Contains(err.Error(), "path escapes") {
			jsonErr(w, http.StatusForbidden, "저장소 밖을 가리키는 링크는 열 수 없습니다")
			return
		}
		log.Printf("files: %v", err)
		jsonErr(w, http.StatusInternalServerError, "저장소 오류")
	}
}

// me: 편집기 메뉴(계정·로그아웃·사용자 관리)용
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.current(r)
	if !ok {
		jsonErr(w, http.StatusUnauthorized, "로그인이 필요합니다")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u.Name, "admin": u.Admin, "csrf": a.sess.CSRF(d), "version": version})
}

func (a *App) fsList(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.apiUser(w, r, false)
	if !ok {
		return
	}
	p := r.URL.Query().Get("path")
	list, wr, err := a.files.List(u.Name, p)
	if err != nil {
		a.fileErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": strings.Trim(p, "/"), "writable": wr, "entries": list,
		"user": u.Name, "admin": u.Admin, "csrf": a.sess.CSRF(d), "maxBytes": a.files.maxSize,
	})
}

func (a *App) serveFile(w http.ResponseWriter, r *http.Request, u User, vpath string) {
	f, st, err := a.files.Open(u.Name, vpath)
	if err != nil {
		a.fileErr(w, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("ETag", etagOf(st))
	h.Set("Content-Disposition", "attachment")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (a *App) putFile(w http.ResponseWriter, r *http.Request, u User, vpath string) {
	pre := precondition{ifMatch: r.Header.Get("If-Match"), ifNoneMatch: r.Header.Get("If-None-Match") == "*"}
	body := http.MaxBytesReader(w, r.Body, a.files.maxSize)
	info, created, err := a.files.Save(u.Name, vpath, body, pre)
	if errors.Is(err, errPrecondition) {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{
			"error": "서버의 파일이 그 사이 바뀌었거나 이미 있습니다", "current": info,
		})
		return
	}
	if err != nil {
		a.fileErr(w, err)
		return
	}
	log.Printf("files: save user=%q path=%q size=%d", u.Name, vpath, info.Size)
	w.Header().Set("ETag", info.ETag)
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, info)
}

func (a *App) deleteFile(w http.ResponseWriter, r *http.Request, u User, vpath string) {
	if err := a.files.Delete(u.Name, vpath, r.Header.Get("If-Match"), u.Admin); err != nil {
		if errors.Is(err, errPrecondition) {
			jsonErr(w, http.StatusPreconditionFailed, "서버의 파일이 그 사이 바뀌었습니다")
			return
		}
		a.fileErr(w, err)
		return
	}
	log.Printf("files: delete user=%q path=%q", u.Name, vpath)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) fsFileGet(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, false); ok {
		a.serveFile(w, r, u, r.URL.Query().Get("path"))
	}
}

func (a *App) fsFilePut(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, true); ok {
		a.putFile(w, r, u, r.URL.Query().Get("path"))
	}
}

func (a *App) fsFileDelete(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, true); ok {
		a.deleteFile(w, r, u, r.URL.Query().Get("path"))
	}
}

// ---- 이전 API (/api/files[/이름] = my/이름) ------------------------------

func legacyPath(name string) string {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return "my/\x00" // splitPath 가 거부
	}
	return "my/" + name
}

func (a *App) filesList(w http.ResponseWriter, r *http.Request) {
	d, u, ok := a.apiUser(w, r, false)
	if !ok {
		return
	}
	list, _, err := a.files.List(u.Name, "my")
	if err != nil {
		a.fileErr(w, err)
		return
	}
	files := []fileInfo{}
	for _, e := range list {
		if e.Type == "file" {
			files = append(files, fileInfo{Name: e.Name, Size: e.Size, MTime: *e.MTime, ETag: e.ETag})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": u.Name, "csrf": a.sess.CSRF(d), "maxBytes": a.files.maxSize, "files": files,
	})
}

func (a *App) fileGet(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, false); ok {
		a.serveFile(w, r, u, legacyPath(r.PathValue("name")))
	}
}

func (a *App) filePut(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, true); ok {
		a.putFile(w, r, u, legacyPath(r.PathValue("name")))
	}
}

func (a *App) fileDelete(w http.ResponseWriter, r *http.Request) {
	if _, u, ok := a.apiUser(w, r, true); ok {
		a.deleteFile(w, r, u, legacyPath(r.PathValue("name")))
	}
}

// ensureUserDir: 사용자 폴더를 만든다 (이미 있으면 그대로). 저장소를 쓸 수 없으면 조용히 넘어간다.
func ensureUserDir(dir, user string) {
	if dir == "" || dir == "off" || !validUserDir(user) {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	if err := root.Mkdir(user, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		log.Printf("files: 사용자 폴더 %s/%s 만들기 실패: %v", dir, user, err)
	}
}

// openFileStore: 저장소 폴더를 확인한다. 쓸 수 없으면 경고만 남기고 nil (서버 저장 기능 꺼짐).
// files/mount 폴더(공유 폴더를 마운트하는 자리)는 없으면 만든다.
func openFileStore(dir string, maxMB int) *FileStore {
	if dir == "" || dir == "off" {
		log.Printf("files: 서버 저장소 사용 안 함")
		return nil
	}
	probe := dir + "/.rhwp-write-test"
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		log.Printf("files: ⚠ %s 에 쓸 수 없어 서버 저장 기능을 끕니다 (uid=%d): %v — README 의 '서버 저장소' 참고",
			dir, os.Getuid(), err)
		return nil
	}
	_ = os.Remove(probe)
	if err := os.Mkdir(filepath.Join(dir, mountDir), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		log.Printf("files: %s/%s 만들기 실패: %v", dir, mountDir, err)
	}
	fsx := &FileStore{dir: dir, maxSize: int64(maxMB) << 20}
	var names []string
	for _, s := range fsx.spaces()[1:] {
		names = append(names, s.Name)
	}
	log.Printf("files: 서버 저장소 %s (파일당 최대 %dMB), 공유 폴더 %d개 %v", dir, maxMB, len(names), names)
	return fsx
}
