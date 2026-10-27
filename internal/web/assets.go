// Package web 内嵌仪表盘的前端模板与静态资源（单二进制部署，无需外部文件）。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/buildinfo"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/settings"
)

// PWA 清单不是常见扩展名，显式注册 MIME，避免 FileServer 回退成 text/plain。
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

//go:embed templates static
var assets embed.FS

// PageData 是渲染页面时传入模板的数据。
type PageData struct {
	Title  string
	User   string
	CSRF   string
	Flash  string
	Error  string
	Active string
	Data   any
}

// Templates 保存按页面预先解析好的模板集合。
type Templates struct {
	pages map[string]*template.Template
}

// CfgGroup 是服务器配置页的分组元数据。
// 配置项数量较多（40+），按语义分组可显著降低查找成本。
type CfgGroup struct {
	ID    string
	Title string
	Hint  string
}

// cfgGroups 是分组顺序，渲染顺序与此一致。
var cfgGroups = []CfgGroup{
	{"core", "基础与访问", "对外地址、监听端口与反向代理"},
	{"cache", "缓存与消息", "缓存位置、保留时长与写入节奏"},
	{"attach", "附件", "附件目录、容量与时效"},
	{"auth", "认证与用户", "登录、注册与访问控制"},
	{"mail", "邮件与上游", "邮件通知、Firebase 与中继上游"},
	{"limit", "限流", "访客与轮询请求速率限制"},
	{"ops", "运维与日志", "指标、性能分析与日志输出"},
}

// cfgGroupOfKey 决定每个配置键归属的分组。
var cfgGroupOfKey = map[string]string{
	"base-url": "core", "listen-http": "core", "listen-https": "core",
	"listen-unix": "core", "behind-proxy": "core", "web-root": "core",
	"database-url": "core",

	"cache-file": "cache", "cache-duration": "cache", "cache-batch-size": "cache",
	"cache-batch-timeout": "cache", "message-size-limit": "cache",
	"message-delay-limit": "cache", "keepalive-interval": "cache",
	"manager-interval": "cache",

	"attachment-cache-dir": "attach", "attachment-total-size-limit": "attach",
	"attachment-file-size-limit": "attach", "attachment-expiry-duration": "attach",

	"auth-file": "auth", "auth-default-access": "auth", "auth-users": "auth",
	"enable-login": "auth", "enable-signup": "auth", "enable-reset-password": "auth",

	"smtp-sender-addr": "mail", "smtp-sender-user": "mail", "smtp-sender-pass": "mail",
	"smtp-sender-from": "mail", "smtp-server-listen": "mail",
	"smtp-server-domain": "mail", "smtp-server-addr-prefix": "mail",
	"firebase-key-file": "mail", "upstream-base-url": "mail",

	"poll-request-limit": "limit", "poll-request-limit-burst": "limit",
	"visitor-request-limit": "limit", "visitor-request-limit-burst": "limit",
	"visitor-request-limit-replenish": "limit",

	"enable-metrics": "ops", "metrics-listen-http": "ops",
	"profile-listen-http": "ops", "debug": "ops", "log-level": "ops",
	"log-format": "ops", "log-file": "ops",
}

// imageExts 是可内联预览的图片扩展名。
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".avif": true, ".svg": true, ".heic": true, ".heif": true,
}

// isRemoteURL 判断字符串是否是可直接引用的 http(s) 地址。
func isRemoteURL(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// urlExt 取 URL 路径部分的扩展名（小写），会丢掉查询串与锚点。
func urlExt(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return ""
	}
	ext := strings.ToLower(s[i:])
	if len(ext) > 6 {
		return ""
	}
	return ext
}

// isImageRef 判断一个 URL 是否指向图片文件。
func isImageRef(s string) bool {
	return isRemoteURL(s) && imageExts[urlExt(s)]
}

// previewSrc 返回消息里可做缩略图预览的图片地址，没有则返回空串。
// 依次看：附件（按 MIME 或扩展名）、图片类消息正文里的直接链接。
func previewSrc(attachURL, attachType, message, contentType string) string {
	if isRemoteURL(attachURL) && (strings.HasPrefix(strings.ToLower(attachType), "image/") || isImageRef(attachURL)) {
		return attachURL
	}
	if strings.HasPrefix(strings.ToLower(contentType), "image/") && isRemoteURL(message) {
		return strings.TrimSpace(message)
	}
	if isImageRef(message) {
		return strings.TrimSpace(message)
	}
	return ""
}

// attachSrc 把原始附件地址包装成仪表盘的同源反代地址，供 <img> 使用。
//
// 直接把 ntfy 上的跨源地址塞进 <img> 会被页面 CSP（img-src 'self'）拦掉，
// 表现为「缩略图空白、但附件链接能下载」；仪表盘走 HTTPS 时还会被当作混合内容拦掉。
// 非 http(s) 的地址原样返回，交给浏览器处理。
func attachSrc(raw string) string {
	raw = strings.TrimSpace(raw)
	if !isRemoteURL(raw) {
		return raw
	}
	return "/attach?u=" + url.QueryEscape(raw)
}

// attachHref 生成附件下载地址（同源反代 + download 语义）。
func attachHref(raw, name string) string {
	raw = strings.TrimSpace(raw)
	if !isRemoteURL(raw) {
		return raw
	}
	out := "/attach?u=" + url.QueryEscape(raw) + "&dl=1"
	if n := strings.TrimSpace(name); n != "" {
		out += "&name=" + url.QueryEscape(n)
	}
	return out
}

// templateFuncs 返回渲染模板时可用的辅助函数。

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"fmtSize": func(n int64) string {
			const unit = 1024
			if n < unit {
				return fmt.Sprintf("%d B", n)
			}
			div, exp := int64(unit), 0
			for v := n / unit; v >= unit; v /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
		},
		"hasPrefix": func(s, p string) bool {
			return len(s) >= len(p) && s[:len(p)] == p
		},
		// fmtAgo 把时间渲染为「3 分钟前」这类相对描述，超过一周回落到日期。
		"fmtAgo": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			d := time.Since(t)
			switch {
			case d < 0:
				return t.Local().Format("01-02 15:04")
			case d < time.Minute:
				return "刚刚"
			case d < time.Hour:
				return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%d 小时前", int(d.Hours()))
			case d < 7*24*time.Hour:
				return fmt.Sprintf("%d 天前", int(d.Hours()/24))
			default:
				return t.Local().Format("2006-01-02")
			}
		},
		// initial 取用户名首字符作为头像占位。
		"initial": func(s string) string {
			for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
				return string(r)
			}
			return "?"
		},
		"cfgGroups": func() []CfgGroup { return cfgGroups },
		// appVersion / appName / appProduct 供品牌区展示，版本号来自构建期注入。
		"appVersion": func() string { return buildinfo.Version },
		"appName":    func() string { return buildinfo.AppName },
		"appProduct": func() string { return buildinfo.Product },
		// previewSrc 判断消息是否带可预览图片，返回图片地址（无则空串）。
		"previewSrc": previewSrc,
		"attachSrc":  attachSrc,
		"attachHref": attachHref,
		// isImageRef 供模板判断任意地址是否为图片，用于附件图标选择。
		"isImageRef": isImageRef,
		"cfgGroup": func(key string) string {
			if g, ok := cfgGroupOfKey[key]; ok {
				return g
			}
			return "ops"
		},
		// cfgGroupOpen 判断分组内是否已有配置项被设置，用于决定默认展开状态。
		"cfgGroupOpen": func(fields []settings.Field, id string) bool {
			for _, f := range fields {
				if f.Set && f.Value != "" && func() string {
					if g, ok := cfgGroupOfKey[f.Key]; ok {
						return g
					}
					return "ops"
				}() == id {
					return true
				}
			}
			return false
		},
	}
}

// NewTemplates 解析所有页面模板。每个页面由 base.html + <page>.html 组合而成，
// 页面文件通过 {{define "content"}} 提供正文。
func NewTemplates() (*Templates, error) {
	pages := []string{"login", "dashboard", "server", "users", "topics", "messages", "error"}
	t := &Templates{pages: make(map[string]*template.Template, len(pages))}
	funcs := templateFuncs()
	for _, p := range pages {
		tpl, err := template.New("layout").Funcs(funcs).ParseFS(assets,
			"templates/base.html", "templates/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("解析模板 %s: %w", p, err)
		}
		t.pages[p] = tpl
	}
	return t, nil
}

// Render 渲染指定页面。
func (t *Templates) Render(w http.ResponseWriter, page string, data PageData) error {
	tpl, ok := t.pages[page]
	if !ok {
		return fmt.Errorf("未知页面: %s", page)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tpl.ExecuteTemplate(w, "layout", data)
}

// StaticHandler 返回内嵌静态资源的 HTTP 处理器（路径 /static/*）。
func StaticHandler() (http.Handler, error) {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub))), nil
}
