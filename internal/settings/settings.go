// Package settings 读取与编辑 ntfy server 的 server.yml。
//
// 设计原则：
//   - 使用 yaml.Node 原地修改，保留注释、键顺序与仪表盘不认识的配置项；
//   - 只允许编辑白名单内的键，其余键只读展示，降低误改风险；
//   - 保存前重新解析校验 YAML 合法性；
//   - 保存时先备份原文件（server.yml.bak.<时间戳>），再原子写入（临时文件 + rename）。
package settings

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Kind 是配置值的类型，用于表单控件与 YAML 类型推断。
type Kind string

const (
	KindString   Kind = "string"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindDuration Kind = "duration"
	KindSize     Kind = "size"
	KindSecret   Kind = "secret"
)

// Field 是 server.yml 中的一个配置项。
type Field struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Kind        Kind   `json:"kind"`
	Description string `json:"description"`
	Editable    bool   `json:"editable"`
	// Restart 表示修改该键后需要重启 ntfy 服务才能生效。
	Restart bool `json:"restart"`
	// Set 表示该键在文件中实际存在。
	Set bool `json:"set"`
	// Protected 表示该键不允许被删除；留空提交时保持原值不变。
	Protected bool `json:"protected"`
}

// Snapshot 是一次配置读取结果。
type Snapshot struct {
	Path    string  `json:"path"`
	Exists  bool    `json:"exists"`
	Fields  []Field `json:"fields"`
	Unknown []Field `json:"unknown"` // 白名单外的已有键，只读
	Raw     string  `json:"raw"`
}

// keyMeta 描述一个已知配置键。
type keyMeta struct {
	Kind        Kind
	Description string
	Editable    bool
	Restart     bool
}

// protectedKeys 是不允许被删除的配置键。
// auth-users 承载静态用户定义（含密码哈希），删除后可能导致无法登录或服务异常，
// 因此只允许修改，留空时保持原值。
var protectedKeys = map[string]bool{
	"auth-users": true,
}

// knownKeys 是允许展示/编辑的 server.yml 键及其元数据。
var knownKeys = map[string]keyMeta{
	"base-url":                        {KindString, "对外可见的基础 URL，例如 https://ntfy.example.com", true, true},
	"listen-http":                     {KindString, "HTTP 监听地址，例如 :2586 或 127.0.0.1:2586", true, true},
	"listen-https":                    {KindString, "HTTPS 监听地址，例如 :443（需配合 key-file/cert-file）", true, true},
	"listen-unix":                     {KindString, "Unix socket 路径", true, true},
	"behind-proxy":                    {KindBool, "是否位于反向代理之后（影响客户端 IP 解析）", true, true},
	"cache-file":                      {KindString, "消息缓存数据库路径（SQLite）", true, true},
	"cache-duration":                  {KindDuration, "消息在缓存中保留多久，例如 12h", true, true},
	"auth-file":                       {KindString, "用户与访问控制数据库路径（user.db）", true, true},
	"auth-default-access":             {KindString, "未匹配任何规则时的默认权限：read-write / read-only / write-only / deny-all", true, true},
	"auth-users":                      {KindString, "静态用户定义（文件中以 YAML 列表保存）。此处用英文逗号分隔多个 user:bcrypt哈希:role；受保护项，留空表示保持不变", true, true},
	"enable-login":                    {KindBool, "是否允许 Web 端登录", true, true},
	"enable-signup":                   {KindBool, "是否允许 Web 端注册新用户", true, true},
	"enable-reset-password":           {KindBool, "是否允许通过邮件重置密码", true, true},
	"web-root":                        {KindString, "Web 应用挂载根路径，默认 app", true, true},
	"attachment-cache-dir":            {KindString, "附件缓存目录", true, true},
	"attachment-total-size-limit":     {KindSize, "附件总容量上限，例如 5G", true, true},
	"attachment-file-size-limit":      {KindSize, "单个附件大小上限，例如 15M", true, true},
	"attachment-expiry-duration":      {KindDuration, "附件保留时长，例如 3h", true, true},
	"message-size-limit":              {KindSize, "单条消息大小上限，例如 4K", true, true},
	"message-delay-limit":             {KindDuration, "消息最长延迟投递时间，例如 0", true, true},
	"keepalive-interval":              {KindDuration, "订阅保活间隔，例如 45s", true, true},
	"manager-interval":                {KindDuration, "后台清理任务运行间隔，例如 1m", true, true},
	"cache-batch-size":                {KindInt, "缓存批量写入大小", true, true},
	"cache-batch-timeout":             {KindDuration, "缓存批量写入超时", true, true},
	"poll-request-limit":              {KindString, "轮询请求速率限制", true, true},
	"poll-request-limit-burst":        {KindInt, "轮询请求速率限制突发值", true, true},
	"visitor-request-limit":           {KindString, "访客请求速率限制", true, true},
	"visitor-request-limit-burst":     {KindInt, "访客请求速率限制突发值", true, true},
	"visitor-request-limit-replenish": {KindDuration, "访客速率限制补充间隔", true, true},
	"database-url":                    {KindString, "PostgreSQL 连接串（配置后不使用 SQLite 缓存）", true, true},
	"upstream-base-url":               {KindString, "上游 ntfy 服务器地址（中继模式）", true, true},
	"smtp-sender-addr":                {KindString, "发件 SMTP 服务器地址 host:port", true, true},
	"smtp-sender-user":                {KindString, "SMTP 用户名", true, true},
	"smtp-sender-pass":                {KindSecret, "SMTP 密码", true, true},
	"smtp-sender-from":                {KindString, "发件人地址", true, true},
	"smtp-server-listen":              {KindString, "接收邮件的 SMTP 监听地址", true, true},
	"smtp-server-domain":              {KindString, "接收邮件的 SMTP 域名", true, true},
	"smtp-server-addr-prefix":         {KindString, "SMTP 收件地址前缀", true, true},
	"firebase-key-file":               {KindString, "Firebase 凭据文件路径", true, true},
	"enable-metrics":                  {KindBool, "是否启用 Prometheus 指标", true, true},
	"metrics-listen-http":             {KindString, "指标服务监听地址", true, true},
	"profile-listen-http":             {KindString, "pprof 性能分析监听地址", true, true},
	"debug":                           {KindBool, "是否开启调试日志", true, true},
	"log-level":                       {KindString, "日志级别：trace/debug/info/warn/error", true, true},
	"log-format":                      {KindString, "日志格式：text/json", true, true},
	"log-file":                        {KindString, "日志文件路径", true, true},
}

// Load 读取并解析 server.yml。
func Load(path string) (*Snapshot, error) {
	snap := &Snapshot{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在时仍返回空快照，便于前端创建
			snap.Exists = false
			for k, meta := range knownKeys {
				snap.Fields = append(snap.Fields, Field{
					Key: k, Kind: meta.Kind, Description: meta.Description,
					Editable: meta.Editable, Restart: meta.Restart,
					Protected: protectedKeys[k],
				})
			}
			sortFields(snap.Fields)
			return snap, nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	snap.Exists = true
	snap.Raw = string(data)

	values, err := parseValues(data)
	if err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	seen := map[string]bool{}
	for k, meta := range knownKeys {
		v := values[k]
		f := Field{
			Key: k, Value: displayValue(v, meta.Kind), Kind: meta.Kind,
			Description: meta.Description, Editable: meta.Editable,
			Restart: meta.Restart, Set: v != nil, Protected: protectedKeys[k],
		}
		snap.Fields = append(snap.Fields, f)
		seen[k] = true
	}
	sortFields(snap.Fields)

	// 白名单外的键：只读展示
	var unknownKeys []string
	for k := range values {
		if !seen[k] {
			unknownKeys = append(unknownKeys, k)
		}
	}
	sortStrings(unknownKeys)
	for _, k := range unknownKeys {
		snap.Unknown = append(snap.Unknown, Field{
			Key: k, Value: displayValue(values[k], KindString), Kind: KindString,
			Editable: false, Set: true,
		})
	}
	return snap, nil
}

// Update 按「只保留有值配置项」的规则重写 path，并返回备份文件路径。
//
// 规则：
//   - updates 中值为空字符串的白名单键会被从文件中删除；
//   - 非空值会写入（已存在则更新，不存在则追加）；
//   - 文件中任何白名单键若值为空，也会被清除（修复历史遗留的空值键）；
//   - 白名单之外的自定义键原样保留；
//   - 有值键的注释与顺序尽量保留。
func Update(path string, updates map[string]string) (string, error) {
	original, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("读取 %s: %w", path, err)
	}

	var doc yaml.Node
	if exists {
		if err := yaml.Unmarshal(original, &doc); err != nil {
			return "", fmt.Errorf("解析 %s: %w", path, err)
		}
	}
	root := documentMap(&doc)

	// 应用表单修改：空值表示删除该键。
	for key, val := range updates {
		meta, ok := knownKeys[key]
		if !ok {
			continue // 忽略非白名单键
		}
		val = strings.TrimSpace(val)
		if key == "auth-users" {
			// auth-users 必须以 YAML 列表写入，否则 ntfy 会报类型不匹配。
			if val == "" {
				normalizeAuthUsers(root) // 留空：保持原内容，仅规范为列表格式
				continue
			}
			if err := validateAuthUsers(val); err != nil {
				return "", err
			}
			setAuthUsers(root, splitAuthUsers(val))
			continue
		}
		if val == "" {
			// 受保护键留空表示保持原值，不做删除。
			if protectedKeys[key] {
				continue
			}
			removeKey(root, key)
			continue
		}
		if err := validateValue(key, val); err != nil {
			return "", err
		}
		setScalar(root, key, val, meta.Kind)
	}

	// 清理文件中遗留的空值白名单键。
	removeEmptyKnownKeys(root)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fmt.Errorf("生成 YAML: %w", err)
	}
	_ = enc.Close()
	newContent := buf.Bytes()

	// 保存前再次解析校验
	var check map[string]any
	if err := yaml.Unmarshal(newContent, &check); err != nil {
		return "", fmt.Errorf("生成的 YAML 不合法: %w", err)
	}

	// 备份
	backup := ""
	if exists {
		backup = fmt.Sprintf("%s.bak.%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, original, 0o600); err != nil {
			return "", fmt.Errorf("写入备份 %s: %w", backup, err)
		}
	}

	// 原子写入
	if err := atomicWrite(path, newContent, fileMode(path)); err != nil {
		return backup, err
	}
	return backup, nil
}

// validateValue 校验特定配置键的值格式，避免写入导致 ntfy 无法启动的内容。
func validateValue(key, val string) error {
	switch key {
	case "auth-users":
		return validateAuthUsers(val)
	}
	return nil
}

// validateAuthUsers 校验 auth-users 的格式：user:bcrypt哈希:role，多项以逗号分隔。
func validateAuthUsers(v string) error {
	for i, entry := range strings.Split(v, ",") {
		entry = strings.TrimSpace(entry)
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return fmt.Errorf("auth-users 第 %d 项格式错误：应为 user:bcrypt哈希:role", i+1)
		}
		name := strings.TrimSpace(parts[0])
		hash := strings.TrimSpace(parts[1])
		role := strings.TrimSpace(parts[2])
		if name == "" {
			return fmt.Errorf("auth-users 第 %d 项缺少用户名", i+1)
		}
		if !strings.HasPrefix(hash, "$2") {
			return fmt.Errorf("auth-users 第 %d 项（%s）不是合法的 bcrypt 哈希", i+1, name)
		}
		if role != "user" && role != "admin" {
			return fmt.Errorf("auth-users 第 %d 项（%s）角色只能是 user 或 admin", i+1, name)
		}
	}
	return nil
}

// parseValues 将 YAML 解析为 键 -> 原始值（保留原始标量文本）。
func parseValues(data []byte) (map[string]any, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(root.Content) == 0 {
		return out, nil
	}
	m := root.Content[0]
	if m.Kind != yaml.MappingNode {
		return out, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		v := m.Content[i+1]
		if v.Kind == yaml.SequenceNode {
			// 兼容列表写法：将序列元素以逗号拼接。
			parts := make([]string, 0, len(v.Content))
			for _, item := range v.Content {
				parts = append(parts, item.Value)
			}
			out[k] = strings.Join(parts, ",")
			continue
		}
		out[k] = v.Value
	}
	return out, nil
}

// documentMap 返回文档的根映射节点，必要时创建。
func documentMap(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content = []*yaml.Node{m}
	}
	return doc.Content[0]
}

// removeKey 从映射中删除指定键及其值。
func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// IsProtected 返回某个配置键是否为受保护键（不可删除）。
func IsProtected(key string) bool { return protectedKeys[key] }

// splitAuthUsers 将逗号分隔的用户定义拆分为列表。
func splitAuthUsers(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// setAuthUsers 将 auth-users 写为 YAML 列表（ntfy 要求 []interface{}）。
func setAuthUsers(m *yaml.Node, entries []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, e := range entries {
		seq.Content = append(seq.Content, &yaml.Node{
			Kind: yaml.ScalarNode, Tag: "!!str", Value: e, Style: yaml.DoubleQuotedStyle,
		})
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "auth-users" {
			seq.HeadComment = m.Content[i+1].HeadComment
			m.Content[i+1] = seq
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "auth-users"}, seq)
}

// normalizeAuthUsers 将已存在的 auth-users 规范为列表（不改变内容）。
func normalizeAuthUsers(m *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != "auth-users" {
			continue
		}
		v := m.Content[i+1]
		switch v.Kind {
		case yaml.SequenceNode:
			return // 已是列表
		case yaml.ScalarNode:
			if strings.TrimSpace(v.Value) == "" {
				return // 空值保持原样
			}
			setAuthUsers(m, splitAuthUsers(v.Value))
			return
		}
	}
}

// removeEmptyKnownKeys 删除白名单中值为空（空字符串或 null）的键。
// 未设置的配置项不应出现在文件中，否则 ntfy 可能把空值当作有效配置而报错。
func removeEmptyKnownKeys(m *yaml.Node) {
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if _, known := knownKeys[k.Value]; known {
			// 受保护键即使为空也保留，避免误删用户定义。
			if !protectedKeys[k.Value] && (v.Tag == "!!null" || strings.TrimSpace(v.Value) == "") {
				continue
			}
		}
		out = append(out, k, v)
	}
	m.Content = out
}

// setScalar 在映射中设置标量值，保留既有键的位置/注释。
func setScalar(m *yaml.Node, key, value string, kind Kind) {
	tag := scalarTag(kind, value)
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			v.Kind = yaml.ScalarNode
			v.Tag = tag
			v.Value = value
			v.Content = nil
			v.Style = 0
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
	)
}

// scalarTag 根据类型与值推断 YAML tag。
func scalarTag(kind Kind, value string) string {
	switch kind {
	case KindBool:
		if _, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return "!!bool"
		}
	case KindInt:
		if _, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return "!!int"
		}
	}
	return "!!str"
}

func displayValue(v any, kind Kind) string {
	if v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	if kind == KindSecret && s != "" {
		return "********"
	}
	return s
}

func fileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	// 新文件默认 0640：server.yml 可能含 SMTP 密码等敏感信息
	return 0o640
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".server.yml.tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	return nil
}

// Validate 校验一段 YAML 是否可解析。
func Validate(content string) error {
	var v any
	if err := yaml.Unmarshal([]byte(content), &v); err != nil {
		return err
	}
	return nil
}

// ErrNotEditable 表示尝试修改不可编辑/未知的键。
var ErrNotEditable = errors.New("该配置项不可编辑")

func sortFields(f []Field)   { sortSlice(f, func(a, b Field) bool { return a.Key < b.Key }) }
func sortStrings(s []string) { sortSlice(s, func(a, b string) bool { return a < b }) }

func sortSlice[T any](s []T, less func(a, b T) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
