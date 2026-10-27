package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfydb"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/settings"
)

// OverviewData 是概览页的数据。
type OverviewData struct {
	Stats            ntfydb.Stats
	DBOK             bool
	DBErr            string
	CliOK            bool
	CliErr           string
	UserCount        int
	Recent           []ntfydb.Message
	ServerURL        string
	ServerConfigPath string
	ConfigExists     bool
	CacheDBPath      string
	// ListenHTTP / ListenHTTPS 是已清洗过的监听地址，ntfy 用 "-" 表示显式关闭，
	// 这类占位值在这里会被过滤为空串，模板据此决定是否渲染。
	ListenHTTP  string
	ListenHTTPS string
	NtfyVersion string
}

// cleanListen 归一化 ntfy 的监听地址取值：空值与表示「关闭」的占位符一律视为未设置。
func cleanListen(v string) string {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "", "-", "off", "false", "none", "disabled":
		return ""
	}
	return v
}

// listenPort 从 ":8080"、"127.0.0.1:8080" 这类监听地址里取出端口号。
func listenPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	i := strings.LastIndex(addr, ":")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	port := addr[i+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return port
}

// withListenPort 在 base-url 没写端口时补上监听端口，让它成为一个完整可点的链接。
// 端口与协议默认值相同（http→80、https→443）时不补，避免把反向代理的地址写歪。
func withListenPort(raw, listenHTTP, listenHTTPS string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Port() != "" {
		return raw
	}
	var port string
	switch strings.ToLower(u.Scheme) {
	case "https":
		port = listenPort(listenHTTPS)
		if port == "443" {
			port = ""
		}
	case "http":
		port = listenPort(listenHTTP)
		if port == "80" {
			port = ""
		}
	default:
		return raw
	}
	if port == "" {
		return raw
	}
	u.Host = net.JoinHostPort(u.Hostname(), port)
	return u.String()
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := OverviewData{
		ServerConfigPath: s.configPath,
		CacheDBPath:      s.dbPath,
		NtfyVersion:      s.ntfyVer,
		ServerURL:        s.cfg.Ntfy.ServerURL,
	}
	if _, err := os.Stat(s.configPath); err == nil {
		data.ConfigExists = true
	}
	if snap, err := settings.Load(s.configPath); err == nil {
		for _, f := range snap.Fields {
			if !f.Set || f.Value == "" {
				continue
			}
			switch f.Key {
			case "base-url":
				if data.ServerURL == "" {
					data.ServerURL = f.Value
				}
			case "listen-http":
				data.ListenHTTP = f.Value
			case "listen-https":
				data.ListenHTTPS = f.Value
			}
		}
	}
	data.ListenHTTP = cleanListen(data.ListenHTTP)
	data.ListenHTTPS = cleanListen(data.ListenHTTPS)
	data.ServerURL = withListenPort(data.ServerURL, data.ListenHTTP, data.ListenHTTPS)


	if store, err := s.openDB(); err != nil {
		data.DBOK = false
		data.DBErr = err.Error()
	} else {
		defer store.Close()
		if st, err := store.Stats(ctx); err != nil {
			data.DBErr = err.Error()
		} else {
			data.Stats = st
			data.DBOK = true
		}
		if msgs, _, err := store.Messages(ctx, ntfydb.MessagesQuery{Limit: 8}); err == nil {
			data.Recent = msgs
		}
	}

	if users, err := s.cli.ListUsers(ctx); err != nil {
		data.CliOK = false
		data.CliErr = err.Error()
	} else {
		data.CliOK = true
		// 排除匿名伪用户（* / anonymous），只统计真实账号。
		for _, u := range users {
			if !u.IsAnonymous() {
				data.UserCount++
			}
		}
	}

	s.render(w, r, "dashboard", "概览", "dashboard", data)
}

// ServerPageData 是服务器配置页的数据。
type ServerPageData struct {
	*settings.Snapshot
	AllowRestart  bool
	ServiceName   string
	ServiceStatus string
	ServiceErr    string
}

// handleServerPage 展示 server.yml 配置与服务状态。
func (s *Server) handleServerPage(w http.ResponseWriter, r *http.Request) {
	snap, err := settings.Load(s.configPath)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "读取配置文件失败："+err.Error())
		return
	}
	data := ServerPageData{
		Snapshot:     snap,
		AllowRestart: s.cfg.Ntfy.AllowRestart,
		ServiceName:  s.cfg.Ntfy.ServiceName,
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Ntfy.RestartTimeout)
	defer cancel()
	if status, err := s.svc.Status(ctx); err != nil {
		data.ServiceStatus = "unknown"
		data.ServiceErr = err.Error()
	} else {
		data.ServiceStatus = status
	}
	s.render(w, r, "server", "服务器配置", "server", data)
}

// handleServerSave 保存 server.yml 修改。
func (s *Server) handleServerSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	snap, err := settings.Load(s.configPath)
	if err != nil {
		s.redirectWith(w, r, "/server", "err", "读取配置失败："+err.Error())
		return
	}
	kinds := make(map[string]settings.Kind, len(snap.Fields))
	for _, f := range snap.Fields {
		kinds[f.Key] = f.Kind
	}
	updates := make(map[string]string)
	for k, vals := range r.PostForm {
		if !strings.HasPrefix(k, "f_") {
			continue
		}
		key := strings.TrimPrefix(k, "f_")
		kind, ok := kinds[key]
		if !ok {
			continue
		}
		v := ""
		if len(vals) > 0 {
			v = strings.TrimSpace(vals[0])
		}
		// 敏感字段留空表示保持不变
		if kind == settings.KindSecret && v == "" {
			continue
		}
		updates[key] = v
	}
	if len(updates) == 0 {
		s.redirectWith(w, r, "/server", "err", "没有可保存的修改。")
		return
	}
	backup, err := settings.Update(s.configPath, updates)
	if err != nil {
		s.redirectWith(w, r, "/server", "err", "保存失败："+err.Error())
		return
	}
	msg := "配置已保存（仅写入有值的配置项，留空的项已从文件中移除）。"
	if backup != "" {
		msg += "原文件已备份为 " + backup + "。"
	}
	msg += "如需生效，请点击「重启 ntfy 服务」。"
	s.log.Printf("保存 server.yml 成功，备份：%s", backup)
	s.redirectWith(w, r, "/server", "ok", msg)
}

// handleServiceRestart 手动重启 ntfy 服务。
func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	if !s.cfg.Ntfy.AllowRestart {
		s.redirectWith(w, r, "/server", "err", "配置中已禁用重启（allow-restart=false）。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Ntfy.RestartTimeout)
	defer cancel()
	status, err := s.svc.Restart(ctx)
	if err != nil {
		s.log.Printf("手动重启服务 %s 失败: %v", s.cfg.Ntfy.ServiceName, err)
		s.redirectWith(w, r, "/server", "err", "重启失败："+err.Error())
		return
	}
	s.log.Printf("手动重启服务 %s，状态 %s", s.cfg.Ntfy.ServiceName, status)
	s.redirectWith(w, r, "/server", "ok", "已重启 "+s.cfg.Ntfy.ServiceName+"，当前状态："+status+"。")
}

// TopicsData 是频道列表页的数据。
type TopicsData struct {
	Topics []ntfydb.Topic
	Stats  ntfydb.Stats
	DBOK   bool
	DBErr  string
}

func (s *Server) handleTopicsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data := TopicsData{}
	store, err := s.openDB()
	if err != nil {
		data.DBOK = false
		data.DBErr = err.Error()
		s.render(w, r, "topics", "频道与消息", "topics", data)
		return
	}
	defer store.Close()
	data.DBOK = true
	if st, err := store.Stats(ctx); err != nil {
		data.DBErr = err.Error()
	} else {
		data.Stats = st
	}
	if topics, err := store.Topics(ctx); err != nil {
		data.DBErr = err.Error()
	} else {
		data.Topics = topics
	}
	s.render(w, r, "topics", "频道与消息", "topics", data)
}

// MessagesData 是频道消息页的数据。
type MessagesData struct {
	Topic      string
	Messages   []ntfydb.Message
	Total      int64
	Limit      int
	Offset     int
	Search     string
	HasPrev    bool
	HasNext    bool
	PrevOffset int
	NextOffset int
	PageFrom   int64
	PageTo     int64
}

func (s *Server) handleMessagesPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	topic := r.PathValue("topic")
	if topic == "" {
		s.fail(w, r, http.StatusBadRequest, "缺少频道名称")
		return
	}
	q := r.URL.Query()
	search := strings.TrimSpace(q.Get("q"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	const limit = 50

	store, err := s.openDB()
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "读取消息缓存失败："+err.Error())
		return
	}
	defer store.Close()

	msgs, total, err := store.Messages(ctx, ntfydb.MessagesQuery{
		Topic: topic, Search: search, Limit: limit, Offset: offset,
	})
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "查询消息失败："+err.Error())
		return
	}
	data := MessagesData{
		Topic:    topic,
		Messages: msgs,
		Total:    total,
		Limit:    limit,
		Offset:   offset,
		Search:   search,
		HasPrev:  offset > 0,
		HasNext:  int64(offset+limit) < total,
	}
	data.PrevOffset = offset - limit
	if data.PrevOffset < 0 {
		data.PrevOffset = 0
	}
	data.NextOffset = offset + limit
	if len(msgs) > 0 {
		data.PageFrom = int64(offset + 1)
		data.PageTo = int64(offset + len(msgs))
	}
	s.render(w, r, "messages", "频道 "+topic, "topics", data)
}

// handleTopicClear 清空频道缓存。
func (s *Server) handleTopicClear(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	topic := r.PathValue("topic")
	store, err := s.openDB()
	if err != nil {
		s.redirectWith(w, r, "/topics", "err", "读取缓存失败："+err.Error())
		return
	}
	defer store.Close()
	n, err := store.ClearTopic(r.Context(), topic)
	if err != nil {
		s.redirectWith(w, r, "/topics/"+topic, "err", "清空失败："+err.Error())
		return
	}
	s.log.Printf("清空频道 %s，删除 %d 条消息", topic, n)
	s.redirectWith(w, r, "/topics", "ok", "已清空频道 "+topic+" 的 "+strconv.FormatInt(n, 10)+" 条消息。")
}

// handleMessageDelete 删除单条消息。
func (s *Server) handleMessageDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	mid := r.PathValue("mid")
	topic := r.FormValue("topic")
	store, err := s.openDB()
	if err != nil {
		s.redirectWith(w, r, "/topics", "err", "读取缓存失败："+err.Error())
		return
	}
	defer store.Close()
	if err := store.Delete(r.Context(), mid); err != nil {
		s.redirectWith(w, r, "/topics", "err", "删除失败："+err.Error())
		return
	}
	back := "/topics"
	if topic != "" {
		back = "/topics/" + topic
	}
	s.redirectWith(w, r, back, "ok", "消息已删除。")
}
