package server

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/config"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfycli"
)

var (
	usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)
	topicRe    = regexp.MustCompile(`^[-_A-Za-z0-9*?]{1,64}$`)
)

// UsersData 是用户管理页的数据。
type UsersData struct {
	Users  []ntfycli.User
	AclRaw string
}

func (s *Server) handleUsersPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	data := UsersData{}
	users, err := s.cli.ListUsers(ctx)
	if err != nil {
		s.renderMsg(w, r, "users", "用户管理", "users", data, "读取用户列表失败："+err.Error())
		return
	}
	// 过滤匿名伪用户（"user * (role: anonymous)"），它不是可管理的账号。
	for _, u := range users {
		if u.IsAnonymous() {
			continue
		}
		data.Users = append(data.Users, u)
	}
	if acl, err := s.cli.ShowAccess(ctx); err == nil {
		data.AclRaw = acl
	}
	s.render(w, r, "users", "用户管理", "users", data)
}

func (s *Server) handleUserAdd(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := ntfycli.Role(r.FormValue("role"))
	if !usernameRe.MatchString(username) {
		s.redirectWith(w, r, "/users", "err", "用户名只能包含字母、数字、下划线、点、@ 和连字符，长度 1-64。")
		return
	}
	if len(password) < 8 {
		s.redirectWith(w, r, "/users", "err", "密码至少需要 8 个字符。")
		return
	}
	if !ntfycli.ValidRole(role) {
		s.redirectWith(w, r, "/users", "err", "非法角色。")
		return
	}
	hash, err := config.HashPassword(password, 0)
	if err != nil {
		s.redirectWith(w, r, "/users", "err", "生成密码哈希失败："+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.AddUser(ctx, username, hash, role, true); err != nil {
		s.redirectWith(w, r, "/users", "err", "添加用户失败："+err.Error())
		return
	}
	s.log.Printf("添加用户 %s（角色 %s）", username, role)
	s.redirectWith(w, r, "/users", "ok", "已添加用户 "+username+"。")
}

func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	role := ntfycli.Role(r.FormValue("role"))
	if !usernameRe.MatchString(username) || !ntfycli.ValidRole(role) {
		s.redirectWith(w, r, "/users", "err", "参数非法。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.ChangeRole(ctx, username, role); err != nil {
		s.redirectWith(w, r, "/users", "err", "修改角色失败："+err.Error())
		return
	}
	s.log.Printf("修改用户 %s 角色为 %s", username, role)
	s.redirectWith(w, r, "/users", "ok", "已更新 "+username+" 的角色。")
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if !usernameRe.MatchString(username) {
		s.redirectWith(w, r, "/users", "err", "用户名非法。")
		return
	}
	if len(password) < 8 {
		s.redirectWith(w, r, "/users", "err", "密码至少需要 8 个字符。")
		return
	}
	hash, err := config.HashPassword(password, 0)
	if err != nil {
		s.redirectWith(w, r, "/users", "err", "生成密码哈希失败："+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.ChangePassword(ctx, username, hash, true); err != nil {
		s.redirectWith(w, r, "/users", "err", "修改密码失败："+err.Error())
		return
	}
	s.log.Printf("修改用户 %s 的密码", username)
	s.redirectWith(w, r, "/users", "ok", "已修改 "+username+" 的密码。")
}

func (s *Server) handleUserRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	if !usernameRe.MatchString(username) {
		s.redirectWith(w, r, "/users", "err", "用户名非法。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.RemoveUser(ctx, username); err != nil {
		s.redirectWith(w, r, "/users", "err", "删除用户失败："+err.Error())
		return
	}
	s.log.Printf("删除用户 %s", username)
	s.redirectWith(w, r, "/users", "ok", "已删除用户 "+username+"。")
}

func (s *Server) handleAccessSet(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	topic := strings.TrimSpace(r.FormValue("topic"))
	perm := ntfycli.Permission(r.FormValue("permission"))
	if username == "" {
		s.redirectWith(w, r, "/users", "err", "请选择用户。")
		return
	}
	if username != "everyone" && !usernameRe.MatchString(username) {
		s.redirectWith(w, r, "/users", "err", "用户名非法。")
		return
	}
	if !topicRe.MatchString(topic) {
		s.redirectWith(w, r, "/users", "err", "主题只能包含字母、数字、-、_、*、?，长度 1-64。")
		return
	}
	if !ntfycli.ValidPermission(perm) {
		s.redirectWith(w, r, "/users", "err", "非法权限。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.SetAccess(ctx, username, topic, perm); err != nil {
		s.redirectWith(w, r, "/users", "err", "设置权限失败："+err.Error())
		return
	}
	s.log.Printf("为用户 %s 设置主题 %s 权限 %s", username, topic, perm)
	s.redirectWith(w, r, "/users", "ok", "已为用户 "+username+" 设置主题 "+topic+" 的 "+string(perm)+" 权限。")
}

func (s *Server) handleAccessReset(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	topic := strings.TrimSpace(r.FormValue("topic"))
	if username == "" {
		s.redirectWith(w, r, "/users", "err", "请选择用户。")
		return
	}
	if username != "everyone" && !usernameRe.MatchString(username) {
		s.redirectWith(w, r, "/users", "err", "用户名非法。")
		return
	}
	if topic != "" && !topicRe.MatchString(topic) {
		s.redirectWith(w, r, "/users", "err", "主题格式非法。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.cli.ResetAccess(ctx, username, topic); err != nil {
		s.redirectWith(w, r, "/users", "err", "重置权限失败："+err.Error())
		return
	}
	s.log.Printf("重置用户 %s 的访问规则（topic=%q）", username, topic)
	s.redirectWith(w, r, "/users", "ok", "已重置 "+username+" 的访问规则。")
}
