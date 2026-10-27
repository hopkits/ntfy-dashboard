// Package ntfycli 封装对本机 ntfy 可执行文件的调用。
//
// 仪表盘不直接改写 ntfy 的 user.db，而是通过官方 CLI 命令管理用户与访问控制，
// 保证与 ntfy 自身的校验、哈希、审计逻辑一致。
//
// 已确认的 CLI 语法（ntfy >= 2.x）：
//
//	ntfy user add [--role=admin|user] [--ignore-exists] USERNAME
//	ntfy user change-pass USERNAME
//	ntfy user change-role USERNAME ROLE
//	ntfy user remove USERNAME
//	ntfy user list
//	ntfy user hash-pass
//	ntfy access [USERNAME [TOPIC [PERMISSION]]]
//	ntfy access --reset [USERNAME [TOPIC]]
//
// 密码非交互传入：设置环境变量 NTFY_PASSWORD（明文）或 NTFY_PASSWORD_HASH（bcrypt 哈希）。
// 本包优先使用 NTFY_PASSWORD_HASH，避免明文密码进入子进程环境。
package ntfycli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Permission 是 ntfy 访问控制权限的枚举（与 CLI 接受的值一致）。
type Permission string

const (
	PermReadWrite Permission = "read-write"
	PermReadOnly  Permission = "read-only"
	PermWriteOnly Permission = "write-only"
	PermDeny      Permission = "deny"
)

// ValidPermission 校验权限值是否合法。
func ValidPermission(p Permission) bool {
	switch p {
	case PermReadWrite, PermReadOnly, PermWriteOnly, PermDeny:
		return true
	}
	return false
}

// Role 是 ntfy 用户角色。
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// ValidRole 校验角色是否合法。
func ValidRole(r Role) bool {
	return r == RoleUser || r == RoleAdmin
}

// IsAnonymous 判断该项是否为 ntfy 输出中的匿名伪用户
// （`user * (role: anonymous)`），它不是真实账号，统计与列表应排除。
func (u User) IsAnonymous() bool {
	return u.Name == "*" || u.Role == "anonymous"
}

// Grant 是一条主题访问规则。
type Grant struct {
	TopicPattern string
	Permission   Permission
}

// User 是从 `ntfy user list` 解析出的用户及其权限。
type User struct {
	Name   string
	Role   Role
	Tier   string
	Grants []Grant
	// Provisioned 表示该用户定义在 server.yml 中（无法在 CLI 中改密码）。
	Provisioned bool
}

// Client 是 ntfy CLI 的调用封装。
type Client struct {
	// CliPath 是 ntfy 可执行文件路径（默认 "ntfy"）。
	CliPath string
	// ConfigFile 是 ntfy server 配置文件路径，通过 --config 传给 CLI，
	// 以便 CLI 找到正确的 auth-file。
	ConfigFile string
	// Timeout 是单次命令超时。
	Timeout time.Duration
}

// New 创建 Client。
func New(cliPath, configFile string, timeout time.Duration) *Client {
	if cliPath == "" {
		cliPath = "ntfy"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{CliPath: cliPath, ConfigFile: configFile, Timeout: timeout}
}

// CommandError 携带失败命令的 stderr，便于前端展示原因。
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("ntfy %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *CommandError) Unwrap() error { return e.Err }

// Version 执行 `ntfy --version`，返回首行版本信息。
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "--version")
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	return line, nil
}

// buildArgs 在子命令之后插入 --config。
//
// ntfy 的 --config 定义在 user/access 等子命令自身（而非全局），因此必须写成
// `ntfy user --config FILE list`，不能写成 `ntfy --config FILE user list`。
// 若首个参数是 flag（如 --version），则不做插入。
func (c *Client) buildArgs(args []string) []string {
	if c.ConfigFile == "" || len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args[0], "--config", c.ConfigFile)
	out = append(out, args[1:]...)
	return out
}

// env 返回子进程环境：始终注入 NTFY_CONFIG_FILE 作为兜底（部分版本按环境变量读取）。
func (c *Client) env(extraEnv []string) []string {
	env := os.Environ()
	if c.ConfigFile != "" {
		env = append(env, "NTFY_CONFIG_FILE="+c.ConfigFile)
	}
	return append(env, extraEnv...)
}

// run 执行 ntfy 命令并返回 stdout。extraEnv 会追加到子进程环境。
func (c *Client) run(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	fullArgs := c.buildArgs(args)
	cmd := exec.CommandContext(ctx, c.CliPath, fullArgs...)
	cmd.Env = c.env(extraEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", &CommandError{Args: args, Err: errors.New("命令执行超时")}
		}
		return "", &CommandError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// ListUsers 执行 `ntfy user list` 并解析为结构化用户列表。
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	out, err := c.run(ctx, nil, "user", "list")
	if err != nil {
		return nil, err
	}
	return ParseUserList(out), nil
}

// AddUser 添加用户。passwordHash 为 true 时 password 参数应传入 bcrypt 哈希。
func (c *Client) AddUser(ctx context.Context, username, password string, role Role, passwordHash bool) error {
	if !ValidRole(role) {
		return fmt.Errorf("非法角色: %s", role)
	}
	if strings.TrimSpace(username) == "" {
		return errors.New("用户名不能为空")
	}
	env := passwordEnv(password, passwordHash)
	_, err := c.run(ctx, env, "user", "add", "--role", string(role), username)
	return err
}

// ChangePassword 修改用户密码。
func (c *Client) ChangePassword(ctx context.Context, username, password string, passwordHash bool) error {
	env := passwordEnv(password, passwordHash)
	_, err := c.run(ctx, env, "user", "change-pass", username)
	return err
}

// ChangeRole 修改用户角色。
func (c *Client) ChangeRole(ctx context.Context, username string, role Role) error {
	if !ValidRole(role) {
		return fmt.Errorf("非法角色: %s", role)
	}
	_, err := c.run(ctx, nil, "user", "change-role", username, string(role))
	return err
}

// RemoveUser 删除用户。
func (c *Client) RemoveUser(ctx context.Context, username string) error {
	_, err := c.run(ctx, nil, "user", "remove", username)
	return err
}

// SetAccess 授予/修改某用户对某主题的权限。
// username 可为 "everyone" 表示匿名客户端；topic 支持通配符（如 "up*"）。
func (c *Client) SetAccess(ctx context.Context, username, topic string, perm Permission) error {
	if !ValidPermission(perm) {
		return fmt.Errorf("非法权限: %s", perm)
	}
	if strings.TrimSpace(topic) == "" {
		return errors.New("主题不能为空")
	}
	_, err := c.run(ctx, nil, "access", username, topic, string(perm))
	return err
}

// ResetAccess 重置某用户（及主题）的访问规则。topic 为空时重置该用户全部规则。
func (c *Client) ResetAccess(ctx context.Context, username, topic string) error {
	args := []string{"access", "--reset"}
	if username != "" {
		args = append(args, username)
		if topic != "" {
			args = append(args, topic)
		}
	}
	_, err := c.run(ctx, nil, args...)
	return err
}

// ShowAccess 执行 `ntfy access` 返回完整 ACL 的原始文本（便于原样展示）。
func (c *Client) ShowAccess(ctx context.Context) (string, error) {
	return c.run(ctx, nil, "access")
}

// ShowUserAccess 执行 `ntfy access USERNAME` 返回该用户权限原始文本。
func (c *Client) ShowUserAccess(ctx context.Context, username string) (string, error) {
	return c.run(ctx, nil, "access", username)
}

// HashPassword 通过 `ntfy user hash-pass` 生成 bcrypt 哈希（密码经 stdin 传入）。
func (c *Client) HashPassword(ctx context.Context, password string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	args := c.buildArgs([]string{"user", "hash-pass"})
	cmd := exec.CommandContext(ctx, c.CliPath, args...)
	cmd.Env = c.env(nil)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &CommandError{Args: args, Stderr: stderr.String(), Err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// passwordEnv 构造密码环境变量。优先使用哈希形式。
func passwordEnv(password string, isHash bool) []string {
	if isHash {
		return []string{"NTFY_PASSWORD_HASH=" + password}
	}
	return []string{"NTFY_PASSWORD=" + password}
}

var (
	// userLineRe 匹配 `user NAME (role: ROLE, tier: TIER)` 及可选的后缀说明。
	userLineRe = regexp.MustCompile(`^user\s+(\S+)\s+\(role:\s*([A-Za-z-]+)(?:,\s*tier:\s*([^,)]+))?(.*)\)\s*$`)
	// grantLineRe 匹配 `- read-write access to topic PATTERN` 等形式。
	grantLineRe = regexp.MustCompile(`^-\s+(.*?)\s+access\s+to\s+topic\s+(.+?)\s*$`)
)

// ParseUserList 解析 `ntfy user list` 的文本输出。
//
// 输出示例：
//
//	user phil (role: user, tier: none)
//	- read-write access to topic mytopic
//	- no access to topic secret*
//	user bob (role: admin)
//	- read-write access to all topics (admin role)
func ParseUserList(out string) []User {
	var users []User
	var cur *User
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := userLineRe.FindStringSubmatch(line); m != nil {
			tier := strings.TrimSpace(m[3])
			rest := strings.ToLower(m[4])
			u := User{
				Name:        m[1],
				Role:        Role(m[2]),
				Tier:        tier,
				Provisioned: strings.Contains(rest, "provisioned"),
			}
			users = append(users, u)
			cur = &users[len(users)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := grantLineRe.FindStringSubmatch(line); m != nil {
			permText := strings.ToLower(m[1])
			perm := Permission("")
			switch {
			case strings.Contains(permText, "read-write"):
				perm = PermReadWrite
			case strings.Contains(permText, "read-only"):
				perm = PermReadOnly
			case strings.Contains(permText, "write-only"):
				perm = PermWriteOnly
			case strings.HasPrefix(permText, "no"):
				perm = PermDeny
			}
			if perm != "" {
				cur.Grants = append(cur.Grants, Grant{
					TopicPattern: strings.TrimSpace(m[2]),
					Permission:   perm,
				})
			}
		}
	}
	return users
}
