// Package server 实现 ntfy 仪表盘的 HTTP 服务：认证、路由与各管理页面。
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/config"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfycli"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfydb"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/settings"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/systemd"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/web"
)

// Server 是仪表盘 HTTP 服务的运行时。
type Server struct {
	cfg  *config.Config
	cli  *ntfycli.Client
	svc  *systemd.Manager
	tpl  *web.Templates
	sess *sessionManager
	log  *log.Logger

	static     http.Handler
	configPath string
	dbPath     string

	// attachHosts / attachClient 支撑 /attach 附件反代。
	// 白名单按主机名比较，避免附件端口与 server-url 不一致时被误拒。
	attachHosts  map[string]bool
	attachClient *http.Client
	// postgres 表示 ntfy 使用 PostgreSQL，SQLite 缓存不可用。
	postgres bool
	ntfyVer  string

	httpSrv *http.Server
}

// New 构造 Server。只初始化依赖，不监听端口。
func New(cfg *config.Config, logger *log.Logger) (*Server, error) {
	tpl, err := web.NewTemplates()
	if err != nil {
		return nil, err
	}
	static, err := web.StaticHandler()
	if err != nil {
		return nil, err
	}
	svc, err := systemd.New(cfg.Ntfy.SystemctlPath, cfg.Ntfy.ServiceName, cfg.Ntfy.RestartTimeout)
	if err != nil {
		return nil, fmt.Errorf("初始化 systemd 管理器: %w", err)
	}
	s := &Server{
		cfg:        cfg,
		cli:        ntfycli.New(cfg.Ntfy.CliPath, cfg.Ntfy.ServerConfigFile, cfg.Ntfy.CliTimeout),
		svc:        svc,
		tpl:        tpl,
		sess:       newSessionManager(cfg),
		log:        logger,
		static:     static,
		configPath: cfg.Ntfy.ServerConfigFile,
	}

	// 消息缓存路径以 server.yml 的 cache-file 为准；dashboard.yml 的
	// ntfy.cache-db 仅作为显式覆盖。两者都没有时表示未配置缓存。
	var snap *settings.Snapshot
	if loaded, err := settings.Load(cfg.Ntfy.ServerConfigFile); err == nil {
		snap = loaded
	}
	s.dbPath, s.postgres = resolveCachePath(cfg, snap)

	// 附件反代的主机白名单：ntfy server-url、server.yml 的 base-url，
	// 仪表盘自身主机在请求期另行放行。附件地址通常由 ntfy 用 base-url 生成，
	// 两者都可能缺省，所以合并取并集。
	s.attachHosts = attachHostsOf(cfg.Ntfy.ServerURL, snapValue(snap, "base-url"))
	s.attachClient = newAttachClient(s.hostAllowed)

	// 探测 ntfy CLI 与版本（失败不影响启动）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if v, err := s.cli.Version(ctx); err == nil {
		s.ntfyVer = v
	}

	s.httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 对外监听且未启用 Secure cookie 时给出安全告警。
	if !isLoopbackListen(cfg.Listen) && !cfg.Session.Secure {
		s.log.Printf("警告：仪表盘监听 %q（非回环地址）且 session.secure=false；"+
			"登录会话可能经明文传输。建议启用 HTTPS 反向代理并将 session.secure 设为 true，"+
			"或用防火墙限制访问来源", cfg.Listen)
	}
	return s, nil
}

// snapValue 取 server.yml 快照中某个已知键的字符串值；快照缺失时返回空串。
func snapValue(snap *settings.Snapshot, key string) string {
	if snap == nil {
		return ""
	}
	for _, f := range snap.Fields {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}

// resolveCachePath 决定消息缓存数据库路径。
//
// 优先级：server.yml 的 cache-file > dashboard.yml 的 ntfy.cache-db > 未配置（空字符串）。
// 若 server.yml 配置了 database-url，则 postgres=true，表示消息存储在 PostgreSQL，
// 此时不使用 SQLite 缓存。
func resolveCachePath(cfg *config.Config, snap *settings.Snapshot) (dbPath string, postgres bool) {
	dbPath = strings.TrimSpace(cfg.Ntfy.CacheDB)
	if snap == nil {
		return dbPath, false
	}
	for _, f := range snap.Fields {
		if !f.Set || strings.TrimSpace(f.Value) == "" {
			continue
		}
		switch f.Key {
		case "cache-file":
			dbPath = strings.TrimSpace(f.Value)
		case "database-url":
			postgres = true
		}
	}
	return dbPath, postgres
}

// isLoopbackListen 判断监听地址是否仅限本机回环。
// 空主机名（如 ":8090"）表示所有网卡，返回 false。
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run 启动 HTTP 服务（阻塞）。
func (s *Server) Run() error {
	s.log.Printf("ntfy-dashboard 监听 %s", s.cfg.Listen)
	if s.postgres {
		s.log.Printf("检测到 database-url，消息缓存位于 PostgreSQL，频道/消息页不可用")
	}
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 优雅关闭。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// Handler 返回完整的 HTTP 处理器（便于测试）。
func (s *Server) Handler() http.Handler { return s.httpSrv.Handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// 公开路由
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// 受保护路由
	mux.HandleFunc("GET /{$}", s.auth(s.handleDashboard))
	mux.HandleFunc("GET /server", s.auth(s.handleServerPage))
	mux.HandleFunc("POST /server", s.auth(s.handleServerSave))
	mux.HandleFunc("POST /server/restart", s.auth(s.handleServiceRestart))
	mux.HandleFunc("GET /users", s.auth(s.handleUsersPage))
	mux.HandleFunc("POST /users/add", s.auth(s.handleUserAdd))
	mux.HandleFunc("POST /users/role", s.auth(s.handleUserRole))
	mux.HandleFunc("POST /users/password", s.auth(s.handleUserPassword))
	mux.HandleFunc("POST /users/remove", s.auth(s.handleUserRemove))
	mux.HandleFunc("POST /users/access", s.auth(s.handleAccessSet))
	mux.HandleFunc("POST /users/access/reset", s.auth(s.handleAccessReset))
	mux.HandleFunc("GET /topics", s.auth(s.handleTopicsPage))
	mux.HandleFunc("GET /topics/{topic}", s.auth(s.handleMessagesPage))
	mux.HandleFunc("GET /attach", s.auth(s.handleAttach))
	mux.HandleFunc("POST /topics/{topic}/clear", s.auth(s.handleTopicClear))
	mux.HandleFunc("POST /messages/{mid}/delete", s.auth(s.handleMessageDelete))

	return s.middleware(mux)
}

// middleware 添加通用安全响应头与请求体大小限制。
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

// auth 包装受保护处理器：未登录时重定向到登录页。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.sess.current(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireCSRF 校验 CSRF token；失败时返回错误页并中止。
func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !s.sess.checkCSRF(r) {
		s.fail(w, r, http.StatusForbidden, "表单已过期或校验失败，请刷新页面后重试。")
		return false
	}
	return true
}

// render 渲染页面，flash/错误提示来自查询参数。
func (s *Server) render(w http.ResponseWriter, r *http.Request, page, title, active string, data any) {
	s.renderMsg(w, r, page, title, active, data, "")
}

// renderMsg 渲染页面，并可显式附加一条错误提示（覆盖查询参数中的 err）。
func (s *Server) renderMsg(w http.ResponseWriter, r *http.Request, page, title, active string, data any, errMsg string) {
	// 确保 CSRF cookie 存在（首次访问即可拿到 token）
	token := s.sess.ensureCSRF(w, r)
	user, _ := s.sess.current(r)
	pd := web.PageData{
		Title:  title,
		User:   user,
		CSRF:   token,
		Active: active,
		Data:   data,
	}
	q := r.URL.Query()
	pd.Flash = q.Get("ok")
	pd.Error = q.Get("err")
	if errMsg != "" {
		pd.Error = errMsg
	}
	if err := s.tpl.Render(w, page, pd); err != nil {
		s.log.Printf("渲染页面 %s 失败: %v", page, err)
	}
}

// fail 渲染错误页。
func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.WriteHeader(code)
	user, _ := s.sess.current(r)
	pd := web.PageData{
		Title: fmt.Sprintf("%d", code),
		User:  user,
		CSRF:  s.sess.csrfToken(r),
		Data:  map[string]any{"Code": code, "Message": msg},
	}
	if err := s.tpl.Render(w, "error", pd); err != nil {
		s.log.Printf("渲染错误页失败: %v", err)
	}
}

// redirectWith 重定向并附带提示消息。
func (s *Server) redirectWith(w http.ResponseWriter, r *http.Request, path, key, msg string) {
	if msg != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		path += sep + key + "=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

// openDB 打开消息缓存（每次请求独立打开，避免 ntfy 重启后句柄失效）。
func (s *Server) openDB() (*ntfydb.Store, error) {
	if s.postgres {
		return nil, errors.New("ntfy 使用 PostgreSQL（server.yml 配置了 database-url），本仪表盘暂仅支持读取 SQLite 缓存")
	}
	if s.dbPath == "" {
		return nil, errors.New("未配置消息缓存：server.yml 未设置 cache-file（ntfy 默认使用内存缓存），无法浏览频道与消息")
	}
	return ntfydb.Open(s.dbPath)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
