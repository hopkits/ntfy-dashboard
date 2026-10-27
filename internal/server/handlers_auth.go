package server

import (
	"net/http"
	"strings"
)

// handleLoginPage 展示登录页；已登录则跳转概览。
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sess.current(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderMsg(w, r, "login", "登录", "", nil, "")
}

// handleLogin 校验用户名密码并签发会话。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == s.cfg.Auth.Username && s.cfg.CheckPassword(password) {
		s.sess.issue(w, username)
		s.log.Printf("管理员 %s 登录成功 (%s)", username, clientIP(r))
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.log.Printf("登录失败 (%s): 用户名=%q", clientIP(r), username)
	w.WriteHeader(http.StatusUnauthorized)
	s.renderMsg(w, r, "login", "登录", "", nil, "用户名或密码错误。")
}

// handleLogout 清除会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if user, ok := s.sess.current(r); ok {
		s.log.Printf("管理员 %s 退出登录", user)
	}
	s.sess.clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func clientIP(r *http.Request) string {
	if host, _, ok := strings.Cut(r.RemoteAddr, ":"); ok {
		return host
	}
	return r.RemoteAddr
}
