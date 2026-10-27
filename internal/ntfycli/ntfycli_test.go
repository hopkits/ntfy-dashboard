package ntfycli

import "testing"

func TestParseUserList(t *testing.T) {
	out := `user phil (role: user, tier: none)
- read-write access to topic mytopic
- no access to topic secret*
user bob (role: admin)
- read-write access to all topics (admin role)
user alice (role: user, tier: pro, provisioned in config)
- read-only access to topic alerts
`
	users := ParseUserList(out)
	if len(users) != 3 {
		t.Fatalf("期望 3 个用户，实得 %d", len(users))
	}
	if users[0].Name != "phil" || users[0].Role != RoleUser {
		t.Errorf("第一个用户解析错误: %+v", users[0])
	}
	if len(users[0].Grants) != 2 {
		t.Fatalf("phil 期望 2 条权限，实得 %d", len(users[0].Grants))
	}
	if users[0].Grants[0].Permission != PermReadWrite || users[0].Grants[0].TopicPattern != "mytopic" {
		t.Errorf("phil 权限解析错误: %+v", users[0].Grants[0])
	}
	if users[0].Grants[1].Permission != PermDeny || users[0].Grants[1].TopicPattern != "secret*" {
		t.Errorf("phil deny 解析错误: %+v", users[0].Grants[1])
	}
	if users[1].Role != RoleAdmin {
		t.Errorf("bob 角色错误: %s", users[1].Role)
	}
	// admin 的 "all topics" 行不是具体主题规则，不应计入 Grants
	if len(users[1].Grants) != 0 {
		t.Errorf("bob 不应有主题权限，实得 %+v", users[1].Grants)
	}
	if !users[2].Provisioned || users[2].Tier != "pro" {
		t.Errorf("alice 解析错误: %+v", users[2])
	}
	if users[2].Grants[0].Permission != PermReadOnly {
		t.Errorf("alice 权限错误: %+v", users[2].Grants[0])
	}
}

// 真实 ntfy v2.34 的 user list 输出（含匿名伪用户）。
func TestParseUserListRealOutput(t *testing.T) {
	out := `user bob (role: admin, tier: none)
- read-write access to all topics (admin role)
user alice (role: user, tier: none)
- no topic-specific permissions
user * (role: anonymous, tier: none)
- no topic-specific permissions
- no access to any (other) topics (server config)
`
	users := ParseUserList(out)
	if len(users) != 3 {
		t.Fatalf("期望 3 项，实得 %d: %+v", len(users), users)
	}
	if users[0].Name != "bob" || users[0].Role != RoleAdmin {
		t.Errorf("bob 解析错误: %+v", users[0])
	}
	if users[1].Name != "alice" || len(users[1].Grants) != 0 {
		t.Errorf("alice 解析错误: %+v", users[1])
	}
	if users[2].Name != "*" || string(users[2].Role) != "anonymous" {
		t.Errorf("匿名项解析错误: %+v", users[2])
	}
	if !users[2].IsAnonymous() {
		t.Error("匿名项应被 IsAnonymous 识别")
	}
	real := 0
	for _, u := range users {
		if !u.IsAnonymous() {
			real++
		}
	}
	if real != 2 {
		t.Errorf("真实用户数应为 2，实得 %d", real)
	}
}

func TestParseUserListEmpty(t *testing.T) {
	if got := ParseUserList(""); len(got) != 0 {
		t.Errorf("空输入应返回空列表，实得 %+v", got)
	}
}

func TestBuildArgsInsertsConfigAfterSubcommand(t *testing.T) {
	c := New("ntfy", "/etc/ntfy/server.yml", 0)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"user", "list"}, []string{"user", "--config", "/etc/ntfy/server.yml", "list"}},
		{[]string{"user", "add", "--role", "user", "bob"}, []string{"user", "--config", "/etc/ntfy/server.yml", "add", "--role", "user", "bob"}},
		{[]string{"access", "phil", "topic", "rw"}, []string{"access", "--config", "/etc/ntfy/server.yml", "phil", "topic", "rw"}},
		// 以 flag 开头时不插入（如 --version）
		{[]string{"--version"}, []string{"--version"}},
	}
	for _, tc := range cases {
		got := c.buildArgs(append([]string{}, tc.args...))
		if len(got) != len(tc.want) {
			t.Fatalf("buildArgs(%v) = %v, 期望 %v", tc.args, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("buildArgs(%v) = %v, 期望 %v", tc.args, got, tc.want)
			}
		}
	}
}

func TestBuildArgsWithoutConfigFile(t *testing.T) {
	c := New("ntfy", "", 0)
	got := c.buildArgs([]string{"user", "list"})
	if len(got) != 2 || got[0] != "user" || got[1] != "list" {
		t.Fatalf("无配置文件时不应插入 --config: %v", got)
	}
}

func TestValidPermissionAndRole(t *testing.T) {
	for _, p := range []Permission{PermReadWrite, PermReadOnly, PermWriteOnly, PermDeny} {
		if !ValidPermission(p) {
			t.Errorf("权限 %s 应合法", p)
		}
	}
	if ValidPermission("owner") {
		t.Error("owner 不应合法")
	}
	if !ValidRole(RoleAdmin) || !ValidRole(RoleUser) || ValidRole("root") {
		t.Error("角色校验错误")
	}
}
