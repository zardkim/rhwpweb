package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidNames(t *testing.T) {
	ok := []string{"보고서.hwp", "a.HWPX", "회의 자료 (1).hwpx", "x.hml"}
	bad := []string{"", ".hwp", ".secret.hwp", "../a.hwp", "a/b.hwp", `a\b.hwp`, "a.txt", "a.hwp ",
		" a.hwp", "a\x00.hwp", "a\n.hwp", "a:b.hwp", "a?.hwp", strings.Repeat("가", 70) + ".hwp", "hwp", "@x.hwp", "#x.hwp"}
	for _, n := range ok {
		if !validFileName(n) {
			t.Errorf("허용돼야 함: %q", n)
		}
	}
	for _, n := range bad {
		if validFileName(n) {
			t.Errorf("거부돼야 함: %q", n)
		}
	}
	for _, p := range []string{"my/../x", "my//x", "my/./x", "mount/@eaDir", "my/a\\b", "my/.git"} {
		if _, err := splitPath(p); err == nil {
			t.Errorf("경로 거부돼야 함: %q", p)
		}
	}
	if _, err := splitPath("my/폴더 A/b:c.hwp"); err != nil {
		t.Error("읽기 경로는 느슨하게 (콜론 허용)")
	}
}

func TestUserDirReserved(t *testing.T) {
	for _, n := range []string{"..", ".a", "mount", "MOUNT", "a/b", ""} {
		if validUserDir(n) {
			t.Errorf("거부돼야 함: %q", n)
		}
	}
	st := newStore(t, t.TempDir())
	if err := st.Add("Mount", "password1", true); err != errReserved {
		t.Fatalf("예약어: %v", err)
	}
}

func setup(t *testing.T) (string, *FileStore) {
	base := t.TempDir()
	fsx := openFileStore(base, 1)
	if fsx == nil {
		t.Fatal("store nil")
	}
	return base, fsx
}

func TestMyFiles(t *testing.T) {
	base, fsx := setup(t)
	info, created, err := fsx.Save("alice", "my/문서.hwp", strings.NewReader("v1"), precondition{ifNoneMatch: true})
	if err != nil || !created {
		t.Fatalf("save: %v %v", err, created)
	}
	if _, _, err := fsx.Save("alice", "my/문서.hwp", strings.NewReader("x"), precondition{ifNoneMatch: true}); !errors.Is(err, errPrecondition) {
		t.Fatalf("If-None-Match: %v", err)
	}
	if _, _, err := fsx.Save("alice", "my/문서.hwp", strings.NewReader("x"), precondition{ifMatch: `"stale"`}); !errors.Is(err, errPrecondition) {
		t.Fatalf("If-Match stale: %v", err)
	}
	info2, created, err := fsx.Save("alice", "my/문서.hwp", strings.NewReader("v22"), precondition{ifMatch: info.ETag})
	if err != nil || created || info2.ETag == info.ETag {
		t.Fatalf("overwrite: %v %v", err, created)
	}
	// 하위 폴더 (NAS 에서 만든 폴더)
	os.MkdirAll(filepath.Join(base, "alice", "2026", "보고"), 0o755)
	if _, _, err := fsx.Save("alice", "my/2026/보고/주간.hwpx", strings.NewReader("x"), precondition{}); err != nil {
		t.Fatalf("하위 폴더 저장: %v", err)
	}
	if _, _, err := fsx.Save("alice", "my/없는폴더/a.hwp", strings.NewReader("x"), precondition{}); err == nil {
		t.Fatal("없는 폴더에는 저장 안 됨")
	}
	os.WriteFile(filepath.Join(base, "alice", ".upload-x.tmp"), nil, 0o644)
	os.WriteFile(filepath.Join(base, "alice", "note.txt"), nil, 0o644)
	os.Mkdir(filepath.Join(base, "alice", "@eaDir"), 0o755)
	l, wr, err := fsx.List("alice", "my")
	if err != nil || !wr || len(l) != 2 || l[0].Type != "dir" || l[0].Name != "2026" || l[1].Name != "문서.hwp" {
		t.Fatalf("list: %v %+v", err, l)
	}
	if l, _, _ := fsx.List("bob", "my"); l == nil || len(l) != 0 {
		t.Fatal("bob 은 빈 목록(null 아님)이어야 함")
	}
	// 밖을 가리키는 링크
	os.WriteFile(filepath.Join(base, "outside.hwp"), []byte("secret"), 0o644)
	os.Symlink("../outside.hwp", filepath.Join(base, "alice", "rel.hwp"))
	os.Symlink(filepath.Join(base, "outside.hwp"), filepath.Join(base, "alice", "abs.hwp"))
	for _, p := range []string{"my/rel.hwp", "my/abs.hwp"} {
		if f, _, err := fsx.Open("alice", p); err == nil {
			f.Close()
			t.Fatalf("링크로 밖을 읽으면 안 됨: %s", p)
		}
	}
	// 다른 사용자 폴더로 가는 길은 없다
	if _, _, err := fsx.Open("bob", "my/../alice/문서.hwp"); err == nil {
		t.Fatal("..")
	}
	if err := fsx.Delete("alice", "my/문서.hwp", `"stale"`, false); !errors.Is(err, errPrecondition) {
		t.Fatal("delete stale")
	}
	if err := fsx.Delete("alice", "my/문서.hwp", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestMounts(t *testing.T) {
	base, fsx := setup(t)
	if st, err := os.Stat(filepath.Join(base, "mount")); err != nil || !st.IsDir() {
		t.Fatal("files/mount 자동 생성")
	}
	os.MkdirAll(filepath.Join(base, "mount", "공용", "양식"), 0o755)
	os.WriteFile(filepath.Join(base, "mount", "공용", "양식", "신청서.hwp"), []byte("x"), 0o644)
	sp, _, err := fsx.List("alice", "")
	if err != nil || len(sp) != 2 || sp[0].Path != "my" || sp[1].Path != "mount/공용" {
		t.Fatalf("spaces %+v", sp)
	}
	l, _, err := fsx.List("bob", "mount/공용/양식")
	if err != nil || len(l) != 1 || l[0].Path != "mount/공용/양식/신청서.hwp" {
		t.Fatalf("mount list %v %+v", err, l)
	}
	if _, _, err := fsx.Save("bob", "mount/x.hwp", strings.NewReader("x"), precondition{}); !errors.Is(err, errNotHere) {
		t.Fatalf("files/mount 바로 아래 저장 금지: %v", err)
	}
	if _, _, err := fsx.Save("bob", "mount/공용/양식/작성.hwp", strings.NewReader("x"), precondition{}); err != nil {
		t.Fatalf("공유 폴더 저장: %v", err)
	}
	if err := fsx.Delete("bob", "mount/공용/양식/작성.hwp", "", false); !errors.Is(err, errAdminOnly) {
		t.Fatalf("공유 폴더 삭제는 관리자만: %v", err)
	}
	if err := fsx.Delete("admin", "mount/공용/양식/작성.hwp", "", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fsx.List("bob", "other/x"); !errors.Is(err, errBadPath) {
		t.Fatal("알 수 없는 공간")
	}
}
