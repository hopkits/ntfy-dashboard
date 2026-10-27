// 预览生成器（仅用于设计验收，不参与产品构建）。
//
// 用真实的模板与真实的数据结构渲染各页面为静态 HTML，供截图核对视觉效果。
// 用法：go run ./cmd/preview <输出目录> <演示 server.yml 路径>
//
// 注意：生成的 HTML 里模板仍会写 /static/… 等站点绝对路径（那是产品运行时的路径），
// 产出后需再跑 preview/fix_paths.py 改成相对路径，否则用 file:// 打开会丢样式与图片。
package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/buildinfo"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfycli"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/ntfydb"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/server"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/settings"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/web"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "用法: preview <输出目录> <server.yml>")
		os.Exit(1)
	}
	outDir, cfgPath := os.Args[1], os.Args[2]
	// 预览页要显示和正式构建一致的版本徽章，这里直接注入（产品构建走 -ldflags）。
	buildinfo.Version = "v0.7"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}

	tpl, err := web.NewTemplates()
	if err != nil {
		fatal(err)
	}

	now := time.Now()
	snap, err := settings.Load(cfgPath)
	if err != nil {
		fatal(err)
	}

	pages := map[string]struct {
		title  string
		active string
		data   any
	}{
		"dashboard": {"概览", "dashboard", server.OverviewData{
			Stats:            ntfydb.Stats{MessageCount: 1284, TopicCount: 6, FileSize: 48234496},
			DBOK:             true,
			CliOK:            true,
			UserCount:        4,
			ServerURL:        "http://ntfy.example.com:2586",
			ServerConfigPath: "/etc/ntfy/server.yml",
			ConfigExists:     true,
			CacheDBPath:      "/var/cache/ntfy/cache.db",
			ListenHTTP:       ":2586",
			NtfyVersion:      "ntfy 2.11.0",
			Recent: []ntfydb.Message{
				{Topic: "alerts", Time: now.Add(-3 * time.Minute), Title: "备份任务完成", Message: "nightly-backup 完成，耗时 3 分 12 秒，共 42 GiB。"},
				{Topic: "ci", Time: now.Add(-27 * time.Minute), Title: "构建 #1843 失败", Message: "pipeline e2e 阶段超时，请查看日志。"},
				{Topic: "monitor", Time: now.Add(-2 * time.Hour), Message: "home-nas 磁盘使用率 87%，剩余 108 GiB。"},
				{Topic: "alerts", Time: now.Add(-5 * time.Hour), Title: "证书即将到期", Message: "ntfy.example.com 的 TLS 证书将在 12 天后到期。"},
			},
		}},
		"topics": {"频道与消息", "topics", server.TopicsData{
			DBOK:  true,
			Stats: ntfydb.Stats{MessageCount: 1284, TopicCount: 6, FileSize: 48234496},
			Topics: []ntfydb.Topic{
				{Name: "alerts", Count: 812, Last: now.Add(-3 * time.Minute), LastMsg: "nightly-backup 完成，耗时 3 分 12 秒"},
				{Name: "monitor", Count: 265, Last: now.Add(-2 * time.Hour), LastMsg: "home-nas 磁盘使用率 87%，剩余 108 GiB"},
				{Name: "ci", Count: 148, Last: now.Add(-27 * time.Minute), LastMsg: "构建 #1843 失败"},
				{Name: "homelab", Count: 41, Last: now.Add(-9 * time.Hour), LastMsg: "路由器固件已更新"},
				{Name: "notify-home", Count: 12, Last: now.Add(-30 * time.Hour), LastMsg: "测试消息"},
				{Name: "backup-status", Count: 6, Last: now.Add(-4 * 24 * time.Hour), LastMsg: "增量备份完成"},
			},
		}},
		"messages": {"频道 alerts", "topics", server.MessagesData{
			Topic:      "alerts",
			Total:      812,
			Limit:      50,
			PageFrom:   1,
			PageTo:     5,
			HasNext:    true,
			NextOffset: 50,
			Messages: []ntfydb.Message{
				{Mid: "a5", Time: now.Add(-1 * time.Minute), Topic: "alerts", Title: "监控面板快照",
					Message: "grafana 面板 5xx 比例突增，附图为例。", Priority: 4, Tags: []string{"chart_with_upwards_trend"},
					AttachmentName: "grafana-panel.svg", AttachmentType: "image/svg+xml", AttachmentSize: 184320,
					AttachmentURL: "http://127.0.0.1:18099/img/panel.svg", Sender: "grafana"},
				{Mid: "a1", Time: now.Add(-3 * time.Minute), Topic: "alerts", Title: "备份任务完成",
					Message: "nightly-backup 完成，耗时 3 分 12 秒，共 42 GiB。", Priority: 3, Tags: []string{"white_check_mark", "backup"}, Sender: "backup-bot"},
				{Mid: "a2", Time: now.Add(-27 * time.Minute), Topic: "alerts", Title: "构建 #1843 失败",
					Message: "pipeline e2e 阶段超时，请查看日志。", Priority: 5, Tags: []string{"rotating_light"}, Click: "https://ci.example.com/1843", Sender: "ci"},
				{Mid: "a3", Time: now.Add(-2 * time.Hour), Topic: "alerts",
					Message: "home-nas 磁盘使用率 87%，剩余 108 GiB。", Priority: 4, Tags: []string{"warning"},
					AttachmentName: "disk-usage-report-2026-10-27.csv", AttachmentType: "text/csv", AttachmentSize: 20480,
					AttachmentURL: "http://127.0.0.1:18099/img/disk-usage-report.csv", Sender: "node-exporter"},
				{Mid: "a6", Time: now.Add(-3 * time.Hour), Topic: "alerts",
					Message: "http://127.0.0.1:18099/img/chart.svg", Priority: 3, Tags: []string{"bar_chart"}, Sender: "grafana"},
				{Mid: "a4", Time: now.Add(-5 * time.Hour), Topic: "alerts", Title: "证书即将到期",
					Message: "ntfy.example.com 的 TLS 证书将在 12 天后到期。", Priority: 2, Tags: []string{"lock"}, User: "admin"},
			},
		}},
		"users": {"用户与权限", "users", server.UsersData{
			Users: []ntfycli.User{
				{Name: "admin", Role: ntfycli.RoleAdmin, Grants: []ntfycli.Grant{
					{TopicPattern: "*", Permission: ntfycli.PermReadWrite},
				}},
				{Name: "backup-bot", Role: ntfycli.RoleUser, Grants: []ntfycli.Grant{
					{TopicPattern: "alerts", Permission: ntfycli.PermWriteOnly},
					{TopicPattern: "backup-*", Permission: ntfycli.PermWriteOnly},
				}},
				{Name: "guest", Role: ntfycli.RoleUser, Grants: []ntfycli.Grant{
					{TopicPattern: "monitor", Permission: ntfycli.PermReadOnly},
					{TopicPattern: "secret", Permission: ntfycli.PermDeny},
				}},
				{Name: "legacy", Role: ntfycli.RoleUser, Provisioned: true},
			},
			AclRaw: "user admin (role: admin)\n- All topics: read-write\n\nuser backup-bot (role: user)\n- alerts: write-only\n- backup-*: write-only\n\nuser guest (role: user)\n- monitor: read-only\n- secret: deny",
		}},
		"server": {"服务器配置", "server", server.ServerPageData{
			Snapshot:      snap,
			AllowRestart:  true,
			ServiceName:   "ntfy",
			ServiceStatus: "active",
		}},
		"error": {"404", "", map[string]any{"Code": 404, "Message": "页面不存在或已被移除。"}},
		"login": {"登录", "", nil},
	}

	order := []string{"login", "dashboard", "topics", "messages", "users", "server", "error"}
	for _, name := range order {
		p := pages[name]
		pd := web.PageData{
			Title:  p.title,
			Active: p.active,
			CSRF:   "preview-csrf-token",
			Data:   p.data,
			User:   "admin",
		}
		if name == "login" {
			pd.User = "" // 登录页不显示侧边栏
		}
		rec := httptest.NewRecorder()
		if err := tpl.Render(rec, name, pd); err != nil {
			fatal(fmt.Errorf("渲染 %s: %w", name, err))
		}
		if err := os.WriteFile(filepath.Join(outDir, name+".html"), rec.Body.Bytes(), 0o644); err != nil {
			fatal(err)
		}
		fmt.Println("生成", name+".html")
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
