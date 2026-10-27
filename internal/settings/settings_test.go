package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndUpdatePreservesCommentsAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	original := `# ntfy 配置
base-url: "http://old.example.com"
listen-http: ":2586"
# 缓存配置
cache-file: "/var/cache/ntfy/cache.db"
custom-unknown-key: "keep-me"
behind-proxy: true
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !snap.Exists {
		t.Fatal("Exists 应为 true")
	}
	var baseURL, listen string
	for _, f := range snap.Fields {
		switch f.Key {
		case "base-url":
			baseURL = f.Value
		case "listen-http":
			listen = f.Value
		}
	}
	if baseURL != "http://old.example.com" || listen != ":2586" {
		t.Fatalf("读取值错误: base-url=%q listen=%q", baseURL, listen)
	}
	if len(snap.Unknown) != 1 || snap.Unknown[0].Key != "custom-unknown-key" {
		t.Fatalf("未知键解析错误: %+v", snap.Unknown)
	}

	backup, err := Update(path, map[string]string{
		"base-url":     "https://new.example.com",
		"behind-proxy": "false",
		"not-allowed":  "should-be-ignored",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if backup == "" {
		t.Fatal("应生成备份文件")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}

	data, _ := os.ReadFile(path)
	got := string(data)
	for _, want := range []string{
		"# ntfy 配置", "# 缓存配置",
		`base-url: https://new.example.com`,
		"behind-proxy: false",
		`custom-unknown-key: "keep-me"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("保存结果缺少 %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "not-allowed") {
		t.Error("非白名单键不应被写入")
	}

	// 原始文件应保留在备份中
	bak, _ := os.ReadFile(backup)
	if !strings.Contains(string(bak), `base-url: "http://old.example.com"`) {
		t.Error("备份内容不是原文件")
	}
}

// 保存时应删除空值项，只保留有值的配置。
func TestUpdateRemovesEmptyKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	original := `# 配置
base-url: "http://example.com"
listen-https: ""
database-url:
behind-proxy: true
custom-key: "keep"
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := Update(path, map[string]string{
		"listen-https": "", // 删除空字符串
		"database-url": "", // 删除 null
		"behind-proxy": "", // 删除布尔
		"base-url":     "http://example.com",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	for _, gone := range []string{"listen-https", "database-url", "behind-proxy"} {
		if strings.Contains(got, gone) {
			t.Errorf("空值项 %q 应被删除\n%s", gone, got)
		}
	}
	for _, want := range []string{"base-url: http://example.com", `custom-key: "keep"`, "# 配置"} {
		if !strings.Contains(got, want) {
			t.Errorf("应保留 %q\n%s", want, got)
		}
	}
}

// auth-users 为受保护键：留空不得删除。
func TestAuthUsersIsProtected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	original := `auth-users: "admin:$2a$10$abcdefghijklmnopqrstuv:admin"
base-url: "http://old"
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, map[string]string{
		"auth-users": "", // 应保持原值
		"base-url":   "http://new",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	if !strings.Contains(got, "auth-users:") || !strings.Contains(got, "$2a$10$abcdefghijklmnopqrstuv") {
		t.Errorf("auth-users 不应被删除\n%s", got)
	}
	if !strings.Contains(got, "base-url: http://new") {
		t.Errorf("其他键应正常更新\n%s", got)
	}
	// 必须以 YAML 列表形式保存，否则 ntfy 报类型不匹配。
	if !strings.Contains(got, `- "admin:$2a$10$abcdefghijklmnopqrstuv:admin"`) {
		t.Errorf("auth-users 应保存为 YAML 列表\n%s", got)
	}
}

// 旧的逗号字符串写法在保存时应规范为 YAML 列表。
func TestAuthUsersNormalizedToList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	original := `auth-users: "a:$2a$10$aaa:admin,b:$2a$10$bbb:user"
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, map[string]string{"auth-users": ""}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := os.ReadFile(path)
	s := string(got)
	if !strings.Contains(s, "auth-users:") || strings.Contains(s, `auth-users: "`) {
		t.Errorf("应改为列表写法\n%s", s)
	}
	for _, want := range []string{`- "a:$2a$10$aaa:admin"`, `- "b:$2a$10$bbb:user"`} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %q\n%s", want, s)
		}
	}
}

// 提交合法的新值时应写为列表。
func TestAuthUsersSetAsList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	if err := os.WriteFile(path, []byte("base-url: \"http://x\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	val := "bob:$2a$10$bbb:user,carol:$2a$10$ccc:admin"
	if _, err := Update(path, map[string]string{"auth-users": val}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	s, _ := os.ReadFile(path)
	for _, want := range []string{`- "bob:$2a$10$bbb:user"`, `- "carol:$2a$10$ccc:admin"`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("缺少 %q\n%s", want, s)
		}
	}
}

// auth-users 格式非法时应拒绝保存，且不改动文件。
func TestAuthUsersValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	original := "auth-users: \"admin:$2a$10$abcdefghijklmnopqrstuv:admin\"\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"admin:plaintext:admin",
		"admin:$2a$10$xxxxxxxx",
		"admin:$2a$10$xxxxxxxx:root",
	}
	for _, v := range bad {
		if _, err := Update(path, map[string]string{"auth-users": v}); err == nil {
			t.Errorf("非法 auth-users %q 应被拒绝", v)
		}
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("校验失败时不应改动文件\n%s", data)
	}
}

// 列表写法的 auth-users 应能解析为逗号分隔字符串。
func TestParseValuesSequence(t *testing.T) {
	values, err := parseValues([]byte(`auth-users:
  - "admin:$2a$10$aaa:admin"
  - "bob:$2a$10$bbb:user"
`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := values["auth-users"].(string)
	want := "admin:$2a$10$aaa:admin,bob:$2a$10$bbb:user"
	if got != want {
		t.Errorf("序列解析 = %q, 期望 %q", got, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	snap, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("缺失文件不应报错: %v", err)
	}
	if snap.Exists {
		t.Error("Exists 应为 false")
	}
	if len(snap.Fields) == 0 {
		t.Error("应返回默认字段列表")
	}
}

func TestSecretNotOverwrittenByEmpty(t *testing.T) {
	// 该行为由 server 层跳过空值实现；此处验证 Load 将 secret 遮蔽。
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	_ = os.WriteFile(path, []byte("smtp-sender-pass: \"hunter2\"\n"), 0o600)
	snap, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Fields {
		if f.Key == "smtp-sender-pass" && f.Value != "********" {
			t.Errorf("secret 应被遮蔽，实得 %q", f.Value)
		}
	}
}
