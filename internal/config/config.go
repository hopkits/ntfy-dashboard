// Package config 负责加载 ntfy-dashboard 自身的管理配置。
//
// 配置文件默认位于 /etc/ntfy/dashboard.yml（可用 -config 覆盖）。
// 该文件只描述仪表盘自身的运行参数，与 ntfy server 的 server.yml 相互独立，
// 仪表盘不修改 ntfy server 的配置文件本身，而是通过 ntfy CLI / 只读数据库来管理。
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// Config 是仪表盘的全部配置。
type Config struct {
	// Listen 是仪表盘 HTTP 服务监听地址，例如 ":8090" 或 "127.0.0.1:8090"。
	Listen string `yaml:"listen"`

	// BaseURL 是仪表盘对外的基础 URL（用于生成链接/跳转 ntfy server）。
	BaseURL string `yaml:"base-url"`

	// Auth 包含仪表盘自身的管理员登录认证。
	Auth Auth `yaml:"auth"`

	// Session 包含会话相关配置。
	Session Session `yaml:"session"`

	// Ntfy 描述如何连接与操作本机 ntfy server。
	Ntfy Ntfy `yaml:"ntfy"`
}

// Auth 描述仪表盘管理员登录认证。
type Auth struct {
	// Username 是管理员登录用户名。
	Username string `yaml:"username"`

	// PasswordHash 是管理员密码的 bcrypt 哈希。留空则每次要求通过
	// 环境变量 NTFY_DASH_PASSWORD 提供明文（启动时计算哈希），
	// 避免把明文密码写进 yml。
	PasswordHash string `yaml:"password-hash"`

	// Secret 用于签发会话 cookie 的签名密钥。若为空则使用 PasswordHash
	// 派生，因此修改密码会令既有会话失效。
	Secret string `yaml:"secret"`
}

// Session 描述会话生命周期。
type Session struct {
	// TTL 是会话有效时长，默认 12h。
	TTL time.Duration `yaml:"ttl"`
	// CookieName 是会话 cookie 名称，默认 "ntfydash_session"。
	CookieName string `yaml:"cookie-name"`
	// Secure 表示是否仅通过 HTTPS 发送会话 cookie，默认 false。
	Secure bool `yaml:"secure"`
}

// Ntfy 描述与 ntfy server 的集成路径。
type Ntfy struct {
	// ServerURL 是 ntfy server 对外可达的基础 URL，例如 https://ntfy.example.com。
	// 用于仪表盘生成「跳转到话题」等链接；可留空。
	ServerURL string `yaml:"server-url"`

	// CliPath 是 ntfy 可执行文件的路径，默认 "ntfy"（依赖 PATH）。
	CliPath string `yaml:"cli-path"`

	// DataDir 是 ntfy 数据目录，包含 user.db / cache.db，默认 /var/lib/ntfy。
	// 旧版本或自定义配置可能把数据库放在别处，以 server.yml 的 cache-file 为准。
	DataDir string `yaml:"data-dir"`

	// CacheDB 是消息缓存数据库路径的可选显式覆盖。
	// 默认留空：以 server.yml 的 cache-file 为准；若那里也未配置，则视为
	// 没有消息缓存（ntfy 默认使用内存缓存），界面会给出提示。
	CacheDB string `yaml:"cache-db"`

	// UserDB 是用户/访问控制数据库路径；为空时根据 DataDir 推算为 user.db。
	UserDB string `yaml:"user-db"`

	// ServerConfigFile 是 ntfy server 配置文件路径，默认 /etc/ntfy/server.yml。
	ServerConfigFile string `yaml:"server-config-file"`

	// CliTimeout 是调用 ntfy CLI 的超时时间，默认 30s。
	CliTimeout time.Duration `yaml:"cli-timeout"`

	// ServiceName 是 ntfy 的 systemd 服务名，默认 "ntfy"。
	ServiceName string `yaml:"service-name"`

	// SystemctlPath 是 systemctl 可执行文件路径，默认 "systemctl"。
	SystemctlPath string `yaml:"systemctl-path"`

	// AllowRestart 表示是否允许从仪表盘重启 ntfy 服务，默认 true。
	// 关闭后配置保存仍可用，但不会执行重启。
	AllowRestart bool `yaml:"allow-restart"`

	// RestartTimeout 是重启服务与查询状态的超时时间，默认 30s。
	RestartTimeout time.Duration `yaml:"restart-timeout"`
}

// Default 返回带默认值的配置。字段未显式设置时使用这些默认值。
func Default() *Config {
	return &Config{
		Listen:  ":8090",
		BaseURL: "http://127.0.0.1:8090",
		Auth: Auth{
			Username: "admin",
		},
		Session: Session{
			TTL:        12 * time.Hour,
			CookieName: "ntfydash_session",
			Secure:     false,
		},
		Ntfy: Ntfy{
			ServerURL:        "",
			CliPath:          "ntfy",
			DataDir:          "/var/lib/ntfy",
			ServerConfigFile: "/etc/ntfy/server.yml",
			CliTimeout:       30 * time.Second,
			ServiceName:      "ntfy",
			SystemctlPath:    "systemctl",
			AllowRestart:     true,
			RestartTimeout:   30 * time.Second,
		},
	}
}

// Load 从 path 读取配置文件并应用默认值。
// 若 path 不存在且 defaultPath 参数为 true 时返回 nil 配置（调用方可决定是否
// 回退到默认配置继续启动）。
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("配置文件不存在: %s", path)
		}
		return nil, fmt.Errorf("读取配置文件 %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate 校验配置的合法性与一致性。
func (c *Config) validate() error {
	if c.Listen == "" {
		return errors.New("listen 不能为空")
	}
	if strings.TrimSpace(c.Auth.Username) == "" {
		return errors.New("auth.username 不能为空")
	}
	if c.Session.TTL <= 0 {
		c.Session.TTL = 12 * time.Hour
	}
	if c.Session.CookieName == "" {
		c.Session.CookieName = "ntfydash_session"
	}
	if c.Ntfy.CliTimeout <= 0 {
		c.Ntfy.CliTimeout = 30 * time.Second
	}
	// 数据目录默认值
	if c.Ntfy.DataDir == "" {
		c.Ntfy.DataDir = "/var/lib/ntfy"
	}
	if c.Ntfy.ServerConfigFile == "" {
		c.Ntfy.ServerConfigFile = "/etc/ntfy/server.yml"
	}
	// 注意：消息缓存路径不在此处推算。
	// ntfy 只有显式配置 cache-file 时才使用持久化缓存，因此路径必须以
	// server.yml 的 cache-file 为准；未配置时表示没有消息缓存，由上层提示。
	if c.Ntfy.UserDB == "" {
		c.Ntfy.UserDB = c.Ntfy.DataDir + "/user.db"
	}
	if c.Ntfy.CliPath == "" {
		c.Ntfy.CliPath = "ntfy"
	}
	if c.Ntfy.ServiceName == "" {
		c.Ntfy.ServiceName = "ntfy"
	}
	if c.Ntfy.SystemctlPath == "" {
		c.Ntfy.SystemctlPath = "systemctl"
	}
	if c.Ntfy.RestartTimeout <= 0 {
		c.Ntfy.RestartTimeout = 30 * time.Second
	}
	return nil
}

// CheckPassword 校验明文密码是否匹配配置中的 bcrypt 哈希。
func (c *Config) CheckPassword(plain string) bool {
	if c.Auth.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(c.Auth.PasswordHash), []byte(plain)) == nil
}

// SessionSecret 返回用于签名会话 cookie 的密钥。优先使用显式配置，
// 否则从密码哈希派生（保证确定性）。
func (c *Config) SessionSecret() []byte {
	if c.Auth.Secret != "" {
		return []byte(c.Auth.Secret)
	}
	hash := c.Auth.PasswordHash
	if hash == "" {
		hash = c.Auth.Username
	}
	return []byte("ntfy-dashboard:" + hash)
}

// HashPassword 生成 bcrypt 哈希，供运维生成 password-hash 使用。
func HashPassword(plain string, cost int) (string, error) {
	if cost <= 0 {
		cost = bcrypt.DefaultCost
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plain), cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}
