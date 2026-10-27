package ntfydb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const testSchema = `
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  mid TEXT NOT NULL, sequence_id TEXT NOT NULL, time INT NOT NULL,
  event TEXT NOT NULL, expires INT NOT NULL, topic TEXT NOT NULL,
  message TEXT NOT NULL, title TEXT NOT NULL, priority INT NOT NULL,
  tags TEXT NOT NULL, click TEXT NOT NULL, icon TEXT NOT NULL,
  actions TEXT NOT NULL, attachment_name TEXT NOT NULL, attachment_type TEXT NOT NULL,
  attachment_size INT NOT NULL, attachment_expires INT NOT NULL, attachment_url TEXT NOT NULL,
  attachment_deleted INT NOT NULL, sender TEXT NOT NULL, user TEXT NOT NULL,
  content_type TEXT NOT NULL, encoding TEXT NOT NULL, published INT NOT NULL
);
CREATE TABLE stats (key TEXT PRIMARY KEY, value INT);
INSERT INTO stats (key, value) VALUES ('messages', 0);
`

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	rows := []struct {
		mid, topic, msg, title, tags, sender string
		ts                                   int64
		pri                                  int
	}{
		{"m1", "alerts", "disk full", "Disk", "warning,disk", "agent", now - 100, 4},
		{"m2", "alerts", "backup ok", "Backup", "white_check_mark", "agent", now - 50, 3},
		{"m3", "deploys", "deploy done", "Deploy", "rocket", "ci", now - 10, 3},
	}
	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO messages
			(mid,sequence_id,time,event,expires,topic,message,title,priority,tags,click,icon,actions,
			 attachment_name,attachment_type,attachment_size,attachment_expires,attachment_url,
			 attachment_deleted,sender,user,content_type,encoding,published)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.mid, "seq", r.ts, "message", 0, r.topic, r.msg, r.title, r.pri, r.tags,
			"", "", "", "", "", 0, 0, "", 0, r.sender, "", "text/plain", "", 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE stats SET value = 3 WHERE key = 'messages'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store, path
}

func TestTopicsAndMessages(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	topics, err := store.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("期望 2 个话题，实得 %d", len(topics))
	}
	if topics[0].Name != "deploys" || topics[0].Count != 1 {
		t.Errorf("话题排序/计数错误: %+v", topics[0])
	}
	if topics[1].Name != "alerts" || topics[1].Count != 2 {
		t.Errorf("话题排序/计数错误: %+v", topics[1])
	}

	msgs, total, err := store.Messages(ctx, MessagesQuery{Topic: "alerts", Limit: 10})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if total != 2 || len(msgs) != 2 {
		t.Fatalf("alerts 期望 2 条，实得 total=%d len=%d", total, len(msgs))
	}
	// 时间倒序：backup ok 更新
	if msgs[0].Message != "backup ok" {
		t.Errorf("排序错误: %+v", msgs[0])
	}
	if len(msgs[0].Tags) != 1 || msgs[0].Tags[0] != "white_check_mark" {
		t.Errorf("tags 解析错误: %+v", msgs[0].Tags)
	}
	if msgs[1].Priority != 4 {
		t.Errorf("priority 解析错误: %d", msgs[1].Priority)
	}

	// 搜索
	found, total, err := store.Messages(ctx, MessagesQuery{Search: "disk", Limit: 10})
	if err != nil || total != 1 || len(found) != 1 || found[0].Mid != "m1" {
		t.Errorf("搜索错误: %v total=%d found=%+v", err, total, found)
	}
}

func TestDeleteAndClearKeepsStats(t *testing.T) {
	store, path := newTestStore(t)
	defer store.Close()
	ctx := context.Background()

	if err := store.Delete(ctx, "m3"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "m3"); err != ErrNotFound {
		t.Errorf("删除后应返回 ErrNotFound，实得 %v", err)
	}
	if statsValue(t, path) != 2 {
		t.Errorf("stats 应减为 2，实得 %d", statsValue(t, path))
	}

	n, err := store.ClearTopic(ctx, "alerts")
	if err != nil {
		t.Fatalf("ClearTopic: %v", err)
	}
	if n != 2 {
		t.Errorf("期望清除 2 条，实得 %d", n)
	}
	if statsValue(t, path) != 0 {
		t.Errorf("stats 应为 0，实得 %d", statsValue(t, path))
	}
}

func statsValue(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int64
	if err := db.QueryRow(`SELECT value FROM stats WHERE key='messages'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.db")); err == nil {
		t.Fatal("缺失文件应报错")
	}
}
