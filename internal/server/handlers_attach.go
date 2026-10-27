package server

import (
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// 附件反代：把 ntfy 服务端的附件取回后由仪表盘同源输出。
//
// 为什么不直接让浏览器加载 ntfy 上的附件地址：
//   - 页面 CSP 是 img-src 'self' data:，跨源 <img> 一律被拦（预览空白，但 <a> 下载不受影响）；
//   - 仪表盘走 HTTPS、附件是 HTTP 时会被浏览器当作混合内容拦掉；
//   - ntfy 若挂在反向代理后的内网地址，浏览器根本访问不到。
//
// 安全边界：只允许取回白名单主机（ntfy server-url / server.yml base-url / 仪表盘自身主机），
// 只允许 http(s)，禁止 URL 里带用户信息，重定向逐跳校验，响应体积有上限。

const (
	// maxAttachBytes 是单个附件的最大透传体积。
	maxAttachBytes = 64 << 20
	// attachCacheSeconds 让浏览器对同一附件短时间复用，避免每次翻页都回源。
	attachCacheSeconds = 300
)

// newAttachClient 构造附件反代用的 HTTP 客户端。
// 重定向必须仍落在白名单主机内，最多 3 跳。
func newAttachClient(allowed func(host string) bool) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			if !allowed(req.URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// hostAllowed 判断目标主机是否在白名单内（按主机名比较，忽略端口）。
func (s *Server) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if s.attachHosts[host] {
		return true
	}
	return false
}

// attachHostsOf 从若干 URL 里提取允许访问的主机名。
func attachHostsOf(urls ...string) map[string]bool {
	out := map[string]bool{}
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
		}
		out[strings.ToLower(u.Hostname())] = true
	}
	return out
}

// handleAttach 透传 ntfy 服务端的附件，供预览与下载使用。
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("u"))
	if raw == "" {
		http.Error(w, "缺少附件地址", http.StatusBadRequest)
		return
	}

	target, err := url.Parse(raw)
	if err != nil {
		http.Error(w, "附件地址无法解析", http.StatusBadRequest)
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		http.Error(w, "只支持 http/https 附件", http.StatusBadRequest)
		return
	}
	if target.User != nil {
		http.Error(w, "附件地址不允许携带用户信息", http.StatusBadRequest)
		return
	}
	// 仪表盘自身主机始终允许（附件与仪表盘同源部署的情况）
	if !s.hostAllowed(target.Hostname()) && !strings.EqualFold(target.Hostname(), hostOnly(r.Host)) {
		s.log.Printf("附件反代拒绝：主机 %q 不在白名单", target.Hostname())
		http.Error(w, "附件主机不在白名单内："+target.Hostname()+"（把 ntfy server-url 配成附件所在地址即可）", http.StatusForbidden)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		http.Error(w, "构造上游请求失败", http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", "ntfy-dashboard (attachment proxy)")
	req.Header.Set("Accept", "*/*")

	resp, err := s.attachClient.Do(req)
	if err != nil {
		s.log.Printf("附件反代失败 %s: %v", target.Redacted(), err)
		http.Error(w, "取回附件失败", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.log.Printf("附件反代上游返回 %d: %s", resp.StatusCode, target.Redacted())
		http.Error(w, "上游返回 "+strconv.Itoa(resp.StatusCode), http.StatusBadGateway)
		return
	}

	h := w.Header()
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age="+strconv.Itoa(attachCacheSeconds))

	if q.Get("dl") == "1" {
		name := strings.TrimSpace(q.Get("name"))
		if name == "" {
			name = path.Base(target.Path)
		}
		if name == "" || name == "." || name == "/" {
			name = "attachment"
		}
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	} else {
		h.Set("Content-Disposition", "inline")
	}

	if resp.ContentLength > 0 && resp.ContentLength <= maxAttachBytes {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}

	if _, err := io.Copy(w, io.LimitReader(resp.Body, maxAttachBytes)); err != nil {
		// 响应头已发出，只能记录
		s.log.Printf("附件透传中断 %s: %v", target.Redacted(), err)
	}
}

// hostOnly 去掉 host:port 里的端口（兼容 IPv6 字面量）。
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}
