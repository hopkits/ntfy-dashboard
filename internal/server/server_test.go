package server

import (
	"testing"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/config"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/settings"
)

func TestIsLoopbackListen(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8090", false},         // 所有网卡
		{"0.0.0.0:8090", false},  // 所有 IPv4
		{"[::]:8090", false},     // 所有 IPv6
		{"127.0.0.1:8090", true}, // IPv4 回环
		{"[::1]:8090", true},     // IPv6 回环
		{"localhost:8090", true}, // 主机名回环
		{"192.168.1.10:8090", false},
		{"8090", false}, // 无主机部分，视为所有网卡
	}
	for _, c := range cases {
		if got := isLoopbackListen(c.addr); got != c.want {
			t.Errorf("isLoopbackListen(%q) = %v, 期望 %v", c.addr, got, c.want)
		}
	}
}

func TestResolveCachePath(t *testing.T) {
	// server.yml 配置了 cache-file：应采用它。
	cfg := config.Default()
	snap := &settings.Snapshot{Fields: []settings.Field{
		{Key: "cache-file", Value: "/srv/ntfy/cache.db", Set: true},
	}}
	if got, pg := resolveCachePath(cfg, snap); got != "/srv/ntfy/cache.db" || pg {
		t.Errorf("cache-file 解析 = %q, postgres=%v", got, pg)
	}

	// server.yml 未配置 cache-file：不应猜测默认路径。
	snap = &settings.Snapshot{Fields: []settings.Field{
		{Key: "cache-file", Set: false},
	}}
	if got, pg := resolveCachePath(cfg, snap); got != "" || pg {
		t.Errorf("未配置时应为空，实得 %q, postgres=%v", got, pg)
	}

	// 配置文件不存在（nil 快照）且无显式覆盖：仍为空。
	if got, _ := resolveCachePath(cfg, nil); got != "" {
		t.Errorf("无配置时应为空，实得 %q", got)
	}

	// dashboard.yml 的 cache-db 作为显式覆盖。
	cfg2 := config.Default()
	cfg2.Ntfy.CacheDB = "/custom/cache.db"
	if got, _ := resolveCachePath(cfg2, nil); got != "/custom/cache.db" {
		t.Errorf("显式覆盖未生效: %q", got)
	}

	// server.yml 的 cache-file 优先于 dashboard.yml 覆盖。
	snap = &settings.Snapshot{Fields: []settings.Field{
		{Key: "cache-file", Value: "/from-server-yml/cache.db", Set: true},
	}}
	if got, _ := resolveCachePath(cfg2, snap); got != "/from-server-yml/cache.db" {
		t.Errorf("server.yml 应优先，实得 %q", got)
	}

	// database-url 表示使用 PostgreSQL。
	snap = &settings.Snapshot{Fields: []settings.Field{
		{Key: "database-url", Value: "postgres://user@db/ntfy", Set: true},
	}}
	if _, pg := resolveCachePath(cfg, snap); !pg {
		t.Error("database-url 应标记 postgres=true")
	}
}
