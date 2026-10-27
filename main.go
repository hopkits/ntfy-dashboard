// ntfy-dashboard 是 ntfy server 的本地 Web 管理控制台。
//
// 功能：
//   - 查看/编辑 /etc/ntfy/server.yml（自动备份、格式校验）
//   - 通过 ntfy CLI 管理用户、角色与访问控制
//   - 浏览/清理 cache.db 中的频道与消息
//
// 用法：
//
//	ntfy-dashboard -config /etc/ntfy/dashboard.yml
//	ntfy-dashboard -hash-password          # 生成配置所需的密码哈希
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/ntfy-dashboard/ntfy-dashboard/internal/buildinfo"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/config"
	"github.com/ntfy-dashboard/ntfy-dashboard/internal/server"
)

// version 由构建脚本通过 -ldflags 注入。
var version = "dev"

func main() {
	var (
		configPath = flag.String("config", "/etc/ntfy/dashboard.yml", "仪表盘配置文件路径")
		listen     = flag.String("listen", "", "覆盖配置中的监听地址，例如 127.0.0.1:8090")
		hashPass   = flag.Bool("hash-password", false, "读取密码并输出 bcrypt 哈希后退出")
		printVer   = flag.Bool("version", false, "输出版本号后退出")
		genSecret  = flag.Bool("gen-secret", false, "生成随机会话密钥（写入配置的 auth.secret）后退出")
	)
	flag.Parse()

	if *printVer {
		fmt.Printf("ntfy-dashboard %s\n", version)
		return
	}
	if *hashPass {
		runHashPassword()
		return
	}
	if *genSecret {
		runGenSecret()
		return
	}

	logger := log.New(os.Stderr, "ntfy-dashboard: ", log.LstdFlags|log.Lmsgprefix)
	// 界面品牌区要显示版本号；沿用 main.version 的 -ldflags 约定，同步给模板层。
	buildinfo.Version = version
	logger.Printf("版本 %s", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("%v\n提示：请参考 configs/dashboard.yml.example 创建 %s", err, *configPath)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}

	// 未配置密码哈希时，从环境变量读取明文密码，启动时计算哈希。
	if cfg.Auth.PasswordHash == "" {
		pw := os.Getenv("NTFY_DASH_PASSWORD")
		if pw == "" {
			logger.Fatalf("配置缺少 auth.password-hash，且未设置环境变量 NTFY_DASH_PASSWORD")
		}
		hash, err := config.HashPassword(pw, 0)
		if err != nil {
			logger.Fatalf("计算密码哈希失败: %v", err)
		}
		cfg.Auth.PasswordHash = hash
		logger.Printf("已从 NTFY_DASH_PASSWORD 环境变量加载管理员密码")
	}

	warnPerms(logger, *configPath)

	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.Fatalf("初始化服务失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run() }()

	select {
	case err := <-errCh:
		if err != nil {
			logger.Fatalf("服务异常退出: %v", err)
		}
	case <-ctx.Done():
		logger.Printf("收到退出信号，正在关闭…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Printf("关闭服务出错: %v", err)
		}
	}
	logger.Printf("已退出")
}

// runHashPassword 读取密码（不回显）并输出 bcrypt 哈希。
func runHashPassword() {
	pw, err := readPassword("请输入要哈希的密码: ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取密码失败:", err)
		os.Exit(1)
	}
	if len(pw) < 8 {
		fmt.Fprintln(os.Stderr, "密码至少需要 8 个字符")
		os.Exit(1)
	}
	hash, err := config.HashPassword(pw, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成哈希失败:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
	fmt.Fprintln(os.Stderr, "\n将上面这行填入 dashboard.yml 的 auth.password-hash（注意保留引号外的原样文本）。")
}

// runGenSecret 生成随机会话密钥。
func runGenSecret() {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fmt.Fprintln(os.Stderr, "生成随机数失败:", err)
		os.Exit(1)
	}
	fmt.Printf("%x\n", buf)
}

// readPassword 从终端读取密码；非终端环境回退到按行读取。
func readPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// warnPerms 在配置文件对其他用户可写时给出告警。
func warnPerms(logger *log.Logger, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Mode().Perm()&0o007 != 0 {
		logger.Printf("警告：%s 权限为 %o，建议执行 chmod 600 %s（文件可能包含敏感信息）",
			path, fi.Mode().Perm(), path)
	}
}
