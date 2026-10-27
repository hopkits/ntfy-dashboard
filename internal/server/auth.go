package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/config"
)

const csrfCookieName = "ntfydash_csrf"

// sessionManager 负责签发与校验登录会话 cookie，以及双提交 CSRF cookie。
//
// 会话 cookie 值 = base64url(payloadJSON) + "." + base64url(HMAC-SHA256(payload))，
// payload 含用户名与过期时间。密钥来自配置（或由密码哈希派生），
// 因此进程重启后既有会话仍然有效，修改密码则会使会话整体失效。
type sessionManager struct {
	secret     []byte
	cookieName string
	ttl        time.Duration
	secure     bool
}

type sessionPayload struct {
	User    string `json:"u"`
	Expires int64  `json:"e"`
}

func newSessionManager(cfg *config.Config) *sessionManager {
	return &sessionManager{
		secret:     cfg.SessionSecret(),
		cookieName: cfg.Session.CookieName,
		ttl:        cfg.Session.TTL,
		secure:     cfg.Session.Secure,
	}
}

func (m *sessionManager) sign(payload []byte) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// issue 签发会话 cookie。
func (m *sessionManager) issue(w http.ResponseWriter, username string) {
	payload, _ := json.Marshal(sessionPayload{
		User:    username,
		Expires: time.Now().Add(m.ttl).Unix(),
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	value := body + "." + m.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(m.ttl.Seconds()),
	})
}

// current 返回当前登录用户名。
func (m *sessionManager) current(r *http.Request) (string, bool) {
	c, err := r.Cookie(m.cookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	expected := m.sign(payload)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return "", false
	}
	var p sessionPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", false
	}
	if p.User == "" || time.Now().Unix() > p.Expires {
		return "", false
	}
	return p.User, true
}

// clear 清除会话 cookie。
func (m *sessionManager) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     m.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ensureCSRF 确保请求带有 CSRF cookie，并返回其值。
func (m *sessionManager) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil && len(c.Value) >= 16 {
		return c.Value
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	token := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	return token
}

// csrfToken 返回当前请求的 CSRF token（用于渲染表单）。
func (m *sessionManager) csrfToken(r *http.Request) string {
	if c, err := r.Cookie(csrfCookieName); err == nil {
		return c.Value
	}
	return ""
}

// checkCSRF 校验表单中的 csrf 字段与 cookie 是否一致。
func (m *sessionManager) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	form := r.FormValue("csrf")
	if form == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(form)) == 1
}
