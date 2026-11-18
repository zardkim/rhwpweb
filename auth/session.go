package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

const cookieName = "rhwp_session"

type sessionData struct {
	U string `json:"u"` // 사용자
	V int    `json:"v"` // 사용자 세션 버전 (비밀번호 변경·비활성화 시 증가 → 기존 세션 무효)
	E int64  `json:"e"` // 만료 (unix)
	N string `json:"n"` // 임의값 (CSRF 토큰 파생용)
}

type Sessions struct {
	key []byte
	ttl time.Duration
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Sessions) mac(parts ...string) []byte {
	m := hmac.New(sha256.New, s.key)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return m.Sum(nil)
}

// Issue 는 새 세션을 만든다. nonce 를 넘기면(세션 재발급) CSRF 토큰이 그대로 유지된다.
func (s *Sessions) Issue(u User, nonce string) (string, time.Time) {
	if nonce == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		nonce = b64(b)
	}
	exp := time.Now().Add(s.ttl)
	d := sessionData{U: u.Name, V: u.SessVer, E: exp.Unix(), N: nonce}
	j, _ := json.Marshal(d)
	payload := b64(j)
	return payload + "." + b64(s.mac("session", payload)), exp
}

func (s *Sessions) Parse(v string) (sessionData, bool) {
	payload, sig, ok := strings.Cut(v, ".")
	if !ok {
		return sessionData{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac("session", payload)) {
		return sessionData{}, false
	}
	j, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return sessionData{}, false
	}
	var d sessionData
	if json.Unmarshal(j, &d) != nil || time.Now().Unix() > d.E {
		return sessionData{}, false
	}
	return d, true
}

// CSRF 토큰은 세션마다 고정 (세션 nonce 에서 파생).
func (s *Sessions) CSRF(d sessionData) string {
	return b64(s.mac("csrf", d.U, d.N))[:32]
}

func (s *Sessions) CheckCSRF(d sessionData, tok string) bool {
	return hmac.Equal([]byte(s.CSRF(d)), []byte(tok))
}

// loadKey: RHWP_AUTH_SECRET 이 있으면 그것을, 없으면 데이터 디렉터리의 secret.key 를 쓴다(없으면 생성).
func loadKey(path string) ([]byte, error) {
	if v := os.Getenv("RHWP_AUTH_SECRET"); v != "" {
		if len(v) < 32 {
			return nil, errors.New("RHWP_AUTH_SECRET 은 32자 이상이어야 합니다")
		}
		return []byte(v), nil
	}
	if b, err := os.ReadFile(path); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) < 32 {
			return nil, errors.New(path + " 형식이 잘못되었습니다")
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

// ---- 로그인 실패 제한 ---------------------------------------------------

type limiter struct {
	mu     sync.Mutex
	window time.Duration
	m      map[string]*failRec
}

type failRec struct {
	n     int
	first time.Time
	until time.Time
}

func newLimiter(window time.Duration) *limiter {
	return &limiter{window: window, m: map[string]*failRec{}}
}

// Blocked: key 가 잠겨 있으면 남은 시간을 돌려준다.
func (l *limiter) Blocked(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.m[key]
	if !ok {
		return 0
	}
	if d := time.Until(r.until); d > 0 {
		return d
	}
	return 0
}

func (l *limiter) Fail(key string, max int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	r, ok := l.m[key]
	if !ok || now.Sub(r.first) > l.window {
		r = &failRec{first: now}
		l.m[key] = r
	}
	r.n++
	if r.n >= max {
		r.until = now.Add(l.window)
		r.n, r.first = 0, now
	}
	// 오래된 항목 정리
	if len(l.m) > 10000 {
		for k, v := range l.m {
			if now.Sub(v.first) > l.window && now.After(v.until) {
				delete(l.m, k)
			}
		}
	}
}

func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, key)
}
