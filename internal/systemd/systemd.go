// Package systemd 封装对 systemctl 的调用（重启服务、查询状态）。
//
// 安全约束：
//   - 服务名在构造时用白名单正则校验，禁止空格、斜杠、分号等字符；
//   - 命令通过 exec.CommandContext 直接传参，不经过 shell 解析，避免注入。
package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// serviceNameRe 限制服务名只包含常见 systemd 单元名字符。
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,128}$`)

// Manager 是 systemctl 的调用封装。
type Manager struct {
	// Bin 是 systemctl 路径，默认 "systemctl"。
	Bin string
	// Service 是目标服务名，例如 "ntfy" 或 "ntfy.service"。
	Service string
	// Timeout 是单次调用超时。
	Timeout time.Duration
}

// New 创建 Manager 并校验服务名。
func New(bin, service string, timeout time.Duration) (*Manager, error) {
	if bin == "" {
		bin = "systemctl"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if !ValidServiceName(service) {
		return nil, fmt.Errorf("非法的服务名: %q（仅允许字母、数字、_ . @ -）", service)
	}
	return &Manager{Bin: bin, Service: service, Timeout: timeout}, nil
}

// ValidServiceName 校验服务名是否安全。
func ValidServiceName(name string) bool {
	name = strings.TrimSuffix(name, ".service")
	return serviceNameRe.MatchString(name)
}

// Restart 重启服务并返回其最新状态（active/inactive/failed 等）。
func (m *Manager) Restart(ctx context.Context) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	if out, err := m.run(ctx, "restart", m.Service); err != nil {
		return "", fmt.Errorf("systemctl restart %s 失败: %w%s", m.Service, err, formatOutput(out))
	}
	// 重启命令返回后再查询状态；失败不影响“重启已执行”的结论。
	status, serr := m.Status(ctx)
	if serr != nil {
		return "unknown", nil
	}
	return status, nil
}

// Status 返回服务当前状态（systemctl is-active 的输出）。
func (m *Manager) Status(ctx context.Context) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	out, err := m.run(ctx, "is-active", m.Service)
	status := strings.TrimSpace(out)
	// is-active 对 inactive/failed 会返回非零退出码，但输出仍是有效状态。
	if err != nil {
		if status != "" {
			return status, nil
		}
		return "", fmt.Errorf("systemctl is-active %s: %w", m.Service, err)
	}
	return status, nil
}

func (m *Manager) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, m.Bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, errors.New("命令执行超时")
		}
		return out, err
	}
	return out, nil
}

func formatOutput(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	return "：" + out
}
