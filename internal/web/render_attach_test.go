package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestAttachSrcWrapsRemote 校验远程附件地址被包成同源反代路径。
func TestAttachSrcWrapsRemote(t *testing.T) {
	raw := "http://ntfy.example.com/file/abc.png"
	got := attachSrc(raw)
	want := "/attach?u=" + url.QueryEscape(raw)
	if got != want {
		t.Fatalf("attachSrc = %q，期望 %q", got, want)
	}
}

// TestAttachSrcKeepsRelative 校验非远程地址原样返回（预览页用的是相对路径）。
func TestAttachSrcKeepsRelative(t *testing.T) {
	for _, in := range []string{"../img/panel.svg", "/static/x.png", ""} {
		if got := attachSrc(in); got != in {
			t.Errorf("attachSrc(%q) = %q，期望原样", in, got)
		}
	}
}

// TestAttachHrefDownload 校验下载地址带 dl=1 与文件名。
func TestAttachHrefDownload(t *testing.T) {
	raw := "https://ntfy.example.com/file/r.csv"
	got := attachHref(raw, "磁盘.csv")
	if !strings.HasPrefix(got, "/attach?u=") {
		t.Fatalf("attachHref = %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	q := u.Query()
	if q.Get("dl") != "1" {
		t.Errorf("dl = %q", q.Get("dl"))
	}
	if q.Get("name") != "磁盘.csv" {
		t.Errorf("name = %q", q.Get("name"))
	}
	if q.Get("u") != raw {
		t.Errorf("u = %q", q.Get("u"))
	}
}

// TestMessagesTemplateUsesProxy 校验消息页渲染出来的是反代地址，
// 而不是会被 CSP 拦掉的跨源原始地址。
func TestMessagesTemplateUsesProxy(t *testing.T) {
	tpl, err := NewTemplates()
	if err != nil {
		t.Fatalf("构造模板: %v", err)
	}

	raw := "http://ntfy.example.com/file/abc.png"
	data := map[string]any{
		"Topic":    "alerts",
		"Total":    1,
		"Limit":    50,
		"PageFrom": 1,
		"PageTo":   1,
		"Messages": []map[string]any{{
			"Mid":            "m1",
			"Time":           time.Now(),
			"Topic":          "alerts",
			"Title":          "监控快照",
			"Message":        "附图为例",
			"Priority":       4,
			"AttachmentName": "abc.png",
			"AttachmentType": "image/png",
			"AttachmentSize": int64(2048),
			"AttachmentURL":  raw,
			"ContentType":    "image/png",
			"Sender":         "grafana",
		}},
	}

	rec := httptest.NewRecorder()
	err = tpl.Render(rec, "messages", PageData{
		Title: "频道 alerts", Active: "topics", CSRF: "tok", User: "admin", Data: data,
	})
	if err != nil {
		t.Fatalf("渲染: %v", err)
	}
	html := rec.Body.String()

	if strings.Contains(html, `src="`+raw+`"`) || strings.Contains(html, `data-preview="`+raw+`"`) {
		t.Fatalf("仍然直接引用了跨源地址，会被 CSP 拦掉")
	}
	if !strings.Contains(html, "/attach?u="+url.QueryEscape(raw)) &&
		!strings.Contains(html, "/attach?u="+strings.ReplaceAll(url.QueryEscape(raw), "%2F", "%2F")) {
		t.Errorf("没有找到反代地址；片段:\n%s", excerpt(html, "thumb"))
	}
	if !strings.Contains(html, "dl=1") {
		t.Errorf("下载链接缺少 dl=1；片段:\n%s", excerpt(html, "file-chip"))
	}
}

// excerpt 截取包含关键字的一小段 HTML，便于失败时定位。
func excerpt(s, keyword string) string {
	i := strings.Index(s, keyword)
	if i < 0 {
		return "(未找到 " + keyword + ")"
	}
	start := i - 200
	if start < 0 {
		start = 0
	}
	end := i + 300
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}
