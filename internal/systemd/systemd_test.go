package systemd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidServiceName(t *testing.T) {
	valid := []string{"ntfy", "ntfy.service", "ntfy@instance", "my_ntfy.1"}
	for _, s := range valid {
		if !ValidServiceName(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	invalid := []string{"", "ntfy; rm -rf /", "../../etc/passwd", "ntfy service", "$(id)", "a/b"}
	for _, s := range invalid {
		if ValidServiceName(s) {
			t.Errorf("%q 应非法", s)
		}
	}
}

func TestNewRejectsInvalidService(t *testing.T) {
	if _, err := New("systemctl", "bad name; rm", time.Second); err == nil {
		t.Fatal("非法服务名应报错")
	}
	m, err := New("systemctl", "ntfy", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if m.Bin != "systemctl" || m.Service != "ntfy" {
		t.Errorf("构造结果错误: %+v", m)
	}
}

// fakeSystemctl 生成一个模拟 systemctl 的脚本。
func fakeSystemctl(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusAndRestart(t *testing.T) {
	bin := fakeSystemctl(t, `
case "$1" in
  is-active) echo active ;;
  restart) echo restarted ;;
  *) echo "unknown action" >&2; exit 1 ;;
esac
`)
	m, err := New(bin, "ntfy", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if st, err := m.Status(ctx); err != nil || st != "active" {
		t.Fatalf("Status = %q, %v", st, err)
	}
	if st, err := m.Restart(ctx); err != nil || st != "active" {
		t.Fatalf("Restart = %q, %v", st, err)
	}
}

func TestRestartFailure(t *testing.T) {
	bin := fakeSystemctl(t, `echo "Unit $2 not found." >&2; exit 5`)
	m, _ := New(bin, "nothere", 5*time.Second)
	if _, err := m.Restart(context.Background()); err == nil {
		t.Fatal("应返回错误")
	}
}

func TestInactiveStatusIsNotError(t *testing.T) {
	bin := fakeSystemctl(t, `echo inactive; exit 3`)
	m, _ := New(bin, "ntfy", 5*time.Second)
	st, err := m.Status(context.Background())
	if err != nil || st != "inactive" {
		t.Fatalf("inactive 应作为状态返回: %q, %v", st, err)
	}
}
