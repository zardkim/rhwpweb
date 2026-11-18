package main

// 사용자 저장소: SQLite (db/users.db).
//
// 서버와 CLI(docker exec … rhwp-auth user …)가 서로 다른 프로세스로 같은 파일을 쓴다.
// WAL + busy_timeout 으로 동시 접근을 견디고, 변경은 BEGIN IMMEDIATE 트랜잭션 안에서
// "활성 관리자 최소 1명" 을 확인한 뒤 커밋한다.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const bcryptCost = 12

var (
	errNotFound  = errors.New("사용자를 찾을 수 없습니다")
	errExists    = errors.New("이미 있는 사용자 이름입니다 (대소문자만 다른 이름도 같은 것으로 봅니다)")
	errBadName   = errors.New("사용자 이름은 영문·숫자·._- 2~32자여야 합니다 (점으로 시작할 수 없음)")
	errWeakPass  = errors.New("비밀번호는 8자 이상이어야 합니다")
	errLastAdmin = errors.New("활성 관리자가 최소 1명은 있어야 합니다")
	errReserved  = errors.New("'mount' 는 공유 폴더 자리로 예약된 이름이라 사용자 이름으로 쓸 수 없습니다")
	nameRe       = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{1,31}$`) // 점으로 시작 금지 (폴더 이름으로 쓴다)
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcryptCost)
)

type User struct {
	Name      string     `json:"name"`
	Hash      string     `json:"hash"`
	Admin     bool       `json:"admin"`
	Disabled  bool       `json:"disabled"`
	SessVer   int        `json:"sv"`
	Created   time.Time  `json:"created"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

type Store struct {
	db   *sql.DB
	path string
	// OnAdd 는 사용자를 만든 직후 불린다 (사용자 폴더 만들기).
	OnAdd func(name string)
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  name       TEXT PRIMARY KEY COLLATE NOCASE,
  hash       TEXT    NOT NULL,
  admin      INTEGER NOT NULL DEFAULT 0,
  disabled   INTEGER NOT NULL DEFAULT 0,
  sess_ver   INTEGER NOT NULL DEFAULT 1,
  created    TEXT    NOT NULL,
  last_login TEXT
);`

func OpenStore(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)" +
		"&_pragma=temp_store(MEMORY)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s 초기화 실패: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const cols = `name, hash, admin, disabled, sess_ver, created, last_login`

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (User, error) {
	var u User
	var created string
	var last sql.NullString
	if err := r.Scan(&u.Name, &u.Hash, &u.Admin, &u.Disabled, &u.SessVer, &created, &last); err != nil {
		return User{}, err
	}
	u.Created, _ = time.Parse(time.RFC3339Nano, created)
	if last.Valid {
		if t, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
			u.LastLogin = &t
		}
	}
	return u, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// mutate: 쓰기 잠금 트랜잭션 안에서 fn 을 실행하고, 관리자 불변식을 확인한 뒤 커밋한다.
func (s *Store) mutate(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin() // _txlock=immediate → BEGIN IMMEDIATE
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	var total, admins int
	if err := tx.QueryRow(`SELECT COUNT(*), COALESCE(SUM(admin = 1 AND disabled = 0), 0) FROM users`).Scan(&total, &admins); err != nil {
		return err
	}
	if total > 0 && admins == 0 {
		return errLastAdmin
	}
	return tx.Commit()
}

func (s *Store) Get(name string) (User, bool) {
	u, err := scanUser(s.db.QueryRow(`SELECT `+cols+` FROM users WHERE name = ?`, name))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("store: %v", err)
		}
		return User{}, false
	}
	return u, true
}

func (s *Store) List() []User {
	rows, err := s.db.Query(`SELECT ` + cols + ` FROM users ORDER BY name COLLATE NOCASE`)
	if err != nil {
		log.Printf("store: %v", err)
		return nil
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		if u, err := scanUser(rows); err == nil {
			out = append(out, u)
		}
	}
	return out
}

func (s *Store) Count() int {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		log.Printf("store: %v", err)
	}
	return n
}

func hashPassword(pw string) (string, error) {
	if len([]rune(pw)) < 8 {
		return "", errWeakPass
	}
	if len(pw) > 72 { // bcrypt 한계
		return "", errors.New("비밀번호는 72바이트 이하여야 합니다")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Store) Add(name, pw string, admin bool) error {
	if !nameRe.MatchString(name) {
		return errBadName
	}
	if strings.EqualFold(name, mountDir) {
		return errReserved
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	err = s.mutate(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (name, hash, admin, sess_ver, created) VALUES (?, ?, ?, 1, ?)`,
			name, h, admin, ts(time.Now()))
		if isUnique(err) {
			return errExists
		}
		return err
	})
	if err == nil && s.OnAdd != nil {
		s.OnAdd(name)
	}
	return err
}

// exec1: 한 사용자에 대한 UPDATE/DELETE. 대상이 없으면 errNotFound.
func (s *Store) exec1(query string, args ...any) error {
	return s.mutate(func(tx *sql.Tx) error {
		r, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return errNotFound
		}
		return nil
	})
}

// SetPassword 는 세션 버전을 올려 기존 로그인 세션을 모두 끊는다.
func (s *Store) SetPassword(name, pw string) error {
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	return s.exec1(`UPDATE users SET hash = ?, sess_ver = sess_ver + 1 WHERE name = ?`, h, name)
}

func (s *Store) SetDisabled(name string, v bool) error {
	return s.exec1(`UPDATE users SET disabled = ?, sess_ver = sess_ver + 1 WHERE name = ?`, v, name)
}

func (s *Store) SetAdmin(name string, v bool) error {
	return s.exec1(`UPDATE users SET admin = ? WHERE name = ?`, v, name)
}

func (s *Store) Delete(name string) error {
	return s.exec1(`DELETE FROM users WHERE name = ?`, name)
}

func (s *Store) TouchLogin(name string) {
	if _, err := s.db.Exec(`UPDATE users SET last_login = ? WHERE name = ?`, ts(time.Now()), name); err != nil {
		log.Printf("store: %v", err)
	}
}

// Check 는 존재하지 않는 사용자도 같은 시간이 걸리도록 더미 해시와 비교한다.
func (s *Store) Check(name, pw string) (User, bool) {
	u, ok := s.Get(name)
	if !ok {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(pw)) != nil {
		return User{}, false
	}
	if u.Disabled {
		return User{}, false
	}
	return u, true
}

// ImportJSON: 이전 버전(0.2.x)의 users.json 을 가져온다. DB 에 사용자가 없을 때만 호출한다.
// 비밀번호 해시·관리자·비활성·세션 버전을 그대로 옮기므로 기존 로그인도 유지된다.
func (s *Store) ImportJSON(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var d struct {
		Users []User `json:"users"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return 0, fmt.Errorf("%s 파싱 실패: %w", path, err)
	}
	n := 0
	err = s.mutate(func(tx *sql.Tx) error {
		for _, u := range d.Users {
			if u.Name == "" || u.Hash == "" {
				continue
			}
			if strings.EqualFold(u.Name, mountDir) {
				log.Printf("⚠ 사용자 %q 는 예약된 이름이라 가져오지 않습니다 (공유 폴더 자리와 겹침)", u.Name)
				continue
			}
			created := u.Created
			if created.IsZero() {
				created = time.Now()
			}
			var last any
			if u.LastLogin != nil {
				last = ts(*u.LastLogin)
			}
			sv := u.SessVer
			if sv < 1 {
				sv = 1
			}
			if _, err := tx.Exec(`INSERT INTO users (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				u.Name, u.Hash, u.Admin, u.Disabled, sv, ts(created), last); err != nil {
				if isUnique(err) {
					continue
				}
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
