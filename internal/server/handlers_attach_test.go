package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newAttachTestServer 构造一个只带附件反代所需依赖的 Server 实例。
func newAttachTestServer(upstream string) *Server {
	hosts := attachHostsOf(upstream)
	s := &Server{
		log:          log.New(io.Discard, "", 0),
		attachHosts:  hosts,
		attachClient: nil,
	}
	s.attachClient = newAttachClient(s.hostAllowed)
	return s
}

// TestHandleAttachProxies 校验反代成功路径：内容、类型、缓存头与 inline 语义。
func TestHandleAttachProxies(t *testing.T) {
	const body = `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`
	var gotUA string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = io.WriteString(w, body)
	}))
	defer up.Close()

	s := newAttachTestServer(up.URL)
	target := up.URL + "/file/abc.svg"
	req := httptest.NewRequest(http.MethodGet, "/attach?u="+url.QueryEscape(target), nil)
	rec := httptest.NewRecorder()
	s.handleAttach(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "inline" {
		t.Errorf("Content-Disposition = %q，期望 inline", cd)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=300") {
		t.Errorf("Cache-Control = %q", cc)
	}
	if got := rec.Body.String(); got != body {
		t.Errorf("响应体 = %q", got)
	}
	if !strings.Contains(gotUA, "ntfy-dashboard") {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

// TestHandleAttachDownload 校验下载语义带上文件名。
func TestHandleAttachDownload(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "a,b\n1,2\n")
	}))
	defer up.Close()

	s := newAttachTestServer(up.URL)
	target := up.URL + "/file/report.csv"
	q := url.Values{"u": {target}, "dl": {"1"}, "name": {"磁盘使用率.csv"}}
	req := httptest.NewRequest(http.MethodGet, "/attach?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handleAttach(rec, req)

	cd := rec.Result().Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("Content-Disposition = %q，期望 attachment", cd)
	}
	if !strings.Contains(cd, "filename*=utf-8''") && !strings.Contains(cd, "%E7%A3%81") {
		t.Errorf("文件名未按 RFC 2231 编码: %q", cd)
	}
}

// TestHandleAttachRejects 校验各类非法输入与白名单外主机。
func TestHandleAttachRejects(t *testing.T) {
	s := newAttachTestServer("http://ntfy.example.com")
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"缺少参数", "/attach", http.StatusBadRequest},
		{"非 http(s)", "/attach?u=" + url.QueryEscape("file:///etc/passwd"), http.StatusBadRequest},
		{"带用户信息", "/attach?u=" + url.QueryEscape("http://user:pw@ntfy.example.com/x.png"), http.StatusBadRequest},
		{"白名单外主机", "/attach?u=" + url.QueryEscape("http://evil.example.net/x.png"), http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.raw, nil)
			rec := httptest.NewRecorder()
			s.handleAttach(rec, req)
			if rec.Code != c.want {
				t.Errorf("状态码 = %d，期望 %d（%s）", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestAttachHostsOf 校验白名单解析：去端口、忽略空值、小写归一。
func TestAttachHostsOf(t *testing.T) {
	got := attachHostsOf("https://ntfy.example.com:2586", "", "http://127.0.0.1:8090")
	for _, host := range []string{"ntfy.example.com", "127.0.0.1"} {
		if !got[host] {
			t.Errorf("白名单缺少 %q（实际 %v）", host, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("白名单条目数 = %d，期望 2", len(got))
	}
}

// TestHostOnly 校验端口剥离。
func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8090": "127.0.0.1",
		"ntfy.example":   "ntfy.example",
		"[::1]:8090":     "::1",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q，期望 %q", in, got, want)
		}
	}
}
