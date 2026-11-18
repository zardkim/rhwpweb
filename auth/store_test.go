package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newStore(t *testing.T, dir string) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStoreBasics(t *testing.T) {
	dir := t.TempDir()
	st := newStore(t, dir)
	var added []string
	st.OnAdd = func(n string) { added = append(added, n) }
	if err := st.Add("admin", "password1", true); err != nil {
		t.Fatal(err)
	}
	if err := st.Add("Kim", "password2", false); err != nil {
		t.Fatal(err)
	}
	if err := st.Add("kim", "password3", false); err != errExists {
		t.Fatalf("대소문자만 다른 이름은 중복: %v", err)
	}
	if err := st.Add("..", "password3", false); err != errBadName {
		t.Fatalf("..: %v", err)
	}
	if err := st.Add("lee", "short", false); err != errWeakPass {
		t.Fatalf("짧은 비번: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("OnAdd: %v", added)
	}
	if _, ok := st.Check("Kim", "password2"); !ok {
		t.Fatal("로그인")
	}
	if _, ok := st.Check("Kim", "wrong-pass"); ok {
		t.Fatal("틀린 비번")
	}
	u, _ := st.Get("Kim")
	sv := u.SessVer
	if err := st.SetPassword("Kim", "newpassword"); err != nil {
		t.Fatal(err)
	}
	u, _ = st.Get("Kim")
	if u.SessVer != sv+1 {
		t.Fatal("비번 변경 시 세션 버전 증가")
	}
	if err := st.SetDisabled("Kim", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Check("Kim", "newpassword"); ok {
		t.Fatal("비활성 사용자 로그인 불가")
	}
	// 마지막 활성 관리자 보호
	if err := st.SetDisabled("admin", true); err != errLastAdmin {
		t.Fatalf("마지막 관리자 비활성: %v", err)
	}
	if err := st.SetAdmin("admin", false); err != errLastAdmin {
		t.Fatalf("마지막 관리자 해제: %v", err)
	}
	if err := st.Delete("admin"); err != errLastAdmin {
		t.Fatalf("마지막 관리자 삭제: %v", err)
	}
	if err := st.Delete("nobody"); err != errNotFound {
		t.Fatalf("없는 사용자: %v", err)
	}
	st.TouchLogin("admin")
	if u, _ := st.Get("admin"); u.LastLogin == nil {
		t.Fatal("last login")
	}
	if l := st.List(); len(l) != 2 || l[0].Name != "admin" {
		t.Fatalf("list %+v", l)
	}
}

// 서버와 CLI 처럼 두 프로세스가 같은 DB 를 동시에 쓰는 상황
func TestTwoHandles(t *testing.T) {
	dir := t.TempDir()
	a := newStore(t, dir)
	b := newStore(t, dir)
	if err := a.Add("admin", "password1", true); err != nil {
		t.Fatal(err)
	}
	if err := b.Add("admin2", "password1", true); err != nil {
		t.Fatal(err)
	}
	if a.Count() != 2 {
		t.Fatal("다른 핸들의 변경이 보여야 함")
	}
	// 두 핸들이 동시에 서로 다른 관리자를 비활성화 → 둘 다 성공하면 관리자 0명
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = a.SetDisabled("admin", true) }()
	go func() { defer wg.Done(); errs[1] = b.SetDisabled("admin2", true) }()
	wg.Wait()
	if errs[0] == nil && errs[1] == nil {
		t.Fatal("동시 비활성화로 활성 관리자가 0명이 됨")
	}
}

func TestImportJSON(t *testing.T) {
	dir := t.TempDir()
	js := `{"users":[{"name":"admin","hash":"` + string(dummyHash) + `","admin":true,"sv":3,"created":"2026-11-01T00:00:00Z"},
	{"name":"kim","hash":"` + string(dummyHash) + `","disabled":true,"sv":1,"created":"2026-11-02T00:00:00Z","last_login":"2026-11-03T00:00:00Z"}]}`
	p := filepath.Join(dir, "users.json")
	os.WriteFile(p, []byte(js), 0o600)
	st := newStore(t, dir)
	n, err := st.ImportJSON(p)
	if err != nil || n != 2 {
		t.Fatalf("import %d %v", n, err)
	}
	u, _ := st.Get("admin")
	if !u.Admin || u.SessVer != 3 {
		t.Fatalf("admin %+v", u)
	}
	k, _ := st.Get("kim")
	if !k.Disabled || k.LastLogin == nil {
		t.Fatalf("kim %+v", k)
	}
}
