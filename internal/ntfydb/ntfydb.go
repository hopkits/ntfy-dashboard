// Package ntfydb 只读/受控地访问 ntfy 的消息缓存数据库（cache.db，SQLite）。
//
// ntfy 的 cache.db 表结构随版本演进：旧版本 messages 表用 id 作为消息主键，
// 新版本改为 mid 并新增 event/expires/sender/user/content_type 等列。
// 本包在打开时通过 PRAGMA table_info 探测实际列，缺失列以默认值补齐，
// 因此可同时兼容新旧版本数据库。
//
// 注意：仪表盘删除消息会直接改动 cache.db。运行时 ntfy server 也持有该库，
// 因此连接启用 busy_timeout，并在删除时同步维护 stats 表的消息计数。
package ntfydb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示消息不存在。
var ErrNotFound = errors.New("消息不存在")

// Message 是 cache.db 中的一条消息（或控制事件）。
type Message struct {
	Mid            string    `json:"mid"`
	Time           time.Time `json:"time"`
	Event          string    `json:"event"`
	Topic          string    `json:"topic"`
	Message        string    `json:"message"`
	Title          string    `json:"title"`
	Priority       int       `json:"priority"`
	Tags           []string  `json:"tags"`
	Click          string    `json:"click"`
	Icon           string    `json:"icon"`
	Actions        string    `json:"actions"`
	Sender         string    `json:"sender"`
	User           string    `json:"user"`
	ContentType    string    `json:"content_type"`
	Encoding       string    `json:"encoding"`
	AttachmentName string    `json:"attachment_name"`
	AttachmentType string    `json:"attachment_type"`
	AttachmentSize int64     `json:"attachment_size"`
	AttachmentURL  string    `json:"attachment_url"`
	Expires        time.Time `json:"expires"`
	Published      bool      `json:"published"`
}

// Topic 是某话题的聚合统计。
type Topic struct {
	Name    string    `json:"name"`
	Count   int64     `json:"count"`
	Last    time.Time `json:"last"`
	LastMsg string    `json:"last_message"`
}

// Stats 是缓存库的总体统计。
type Stats struct {
	MessageCount int64 `json:"message_count"`
	TopicCount   int64 `json:"topic_count"`
	FileSize     int64 `json:"file_size"`
}

// Store 是 cache.db 的访问句柄。
type Store struct {
	path string
	db   *sql.DB
	cols map[string]bool
	// midExpr 是消息主键表达式（新版本 mid，旧版本 id）。
	midExpr string
}

// Open 打开 cache.db。连接为读写（删除功能需要），并设置 busy_timeout 以
// 容忍与 ntfy server 的并发访问。path 不存在时返回错误。
func Open(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("缓存数据库不存在: %s", path)
		}
		return nil, err
	}
	dsn := "file:" + url.PathEscape(path) + "?mode=rw&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开缓存数据库: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(time.Minute)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接缓存数据库: %w", err)
	}
	s := &Store{path: path, db: db}
	if err := s.detectColumns(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接。
func (s *Store) Close() error { return s.db.Close() }

// Path 返回数据库路径。
func (s *Store) Path() string { return s.path }

func (s *Store) detectColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return fmt.Errorf("读取表结构: %w", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("解析表结构: %w", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return errors.New("messages 表不存在，无法读取缓存")
	}
	s.cols = cols
	if cols["mid"] {
		s.midExpr = "mid"
	} else if cols["id"] {
		s.midExpr = "CAST(id AS TEXT)"
	} else {
		return errors.New("messages 表缺少 mid/id 主键列")
	}
	return nil
}

// col 返回可用列名，缺失时返回 fallback SQL 表达式。
func (s *Store) col(name, fallback string) string {
	if s.cols[name] {
		return name
	}
	return fallback
}

func (s *Store) selectList() string {
	return strings.Join([]string{
		s.midExpr + " AS mid",
		s.col("time", "0") + " AS time",
		s.col("event", "'message'") + " AS event",
		s.col("topic", "''") + " AS topic",
		s.col("message", "''") + " AS message",
		s.col("title", "''") + " AS title",
		s.col("priority", "0") + " AS priority",
		s.col("tags", "''") + " AS tags",
		s.col("click", "''") + " AS click",
		s.col("icon", "''") + " AS icon",
		s.col("actions", "''") + " AS actions",
		s.col("sender", "''") + " AS sender",
		s.col("user", "''") + " AS user",
		s.col("content_type", "''") + " AS content_type",
		s.col("encoding", "''") + " AS encoding",
		s.col("attachment_name", "''") + " AS attachment_name",
		s.col("attachment_type", "''") + " AS attachment_type",
		s.col("attachment_size", "0") + " AS attachment_size",
		s.col("attachment_url", "''") + " AS attachment_url",
		s.col("expires", "0") + " AS expires",
		s.col("published", "1") + " AS published",
	}, ", ")
}

// scanMessage 按 selectList 的顺序扫描一行。
func scanMessage(sc interface {
	Scan(dest ...any) error
}) (*Message, error) {
	var (
		m          Message
		ts         int64
		tags       string
		expires    int64
		published  int64
		priority   int64
		attachSize int64
	)
	if err := sc.Scan(
		&m.Mid, &ts, &m.Event, &m.Topic, &m.Message, &m.Title, &priority, &tags,
		&m.Click, &m.Icon, &m.Actions, &m.Sender, &m.User, &m.ContentType, &m.Encoding,
		&m.AttachmentName, &m.AttachmentType, &attachSize, &m.AttachmentURL, &expires, &published,
	); err != nil {
		return nil, err
	}
	m.Priority = int(priority)
	m.AttachmentSize = attachSize
	m.Published = published != 0
	if ts > 0 {
		m.Time = time.Unix(ts, 0)
	}
	if expires > 0 {
		m.Expires = time.Unix(expires, 0)
	}
	m.Tags = splitTags(tags)
	return &m, nil
}

func splitTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Topics 返回话题列表及每个话题的消息数、最后一条消息时间。
func (s *Store) Topics(ctx context.Context) ([]Topic, error) {
	q := `SELECT topic, COUNT(*) AS cnt, MAX(time) AS last FROM messages`
	if s.cols["published"] {
		q += ` WHERE published = 1`
	}
	q += ` GROUP BY topic ORDER BY last DESC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("查询话题: %w", err)
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var (
			t    Topic
			last int64
		)
		if err := rows.Scan(&t.Name, &t.Count, &last); err != nil {
			return nil, err
		}
		if last > 0 {
			t.Last = time.Unix(last, 0)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 补充每个话题的最后一条消息摘要（单独查询，避免复杂 SQL）
	for i := range out {
		_ = s.db.QueryRowContext(ctx,
			`SELECT message FROM messages WHERE topic = ? ORDER BY time DESC LIMIT 1`,
			out[i].Name,
		).Scan(&out[i].LastMsg)
	}
	return out, nil
}

// MessagesQuery 是消息列表查询参数。
type MessagesQuery struct {
	Topic  string
	Search string
	Limit  int
	Offset int
}

// Messages 按条件分页查询消息（时间倒序）。
func (s *Store) Messages(ctx context.Context, q MessagesQuery) ([]Message, int64, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	var where []string
	var args []any
	if q.Topic != "" {
		where = append(where, "topic = ?")
		args = append(args, q.Topic)
	}
	if q.Search != "" {
		where = append(where, "(message LIKE ? OR title LIKE ?)")
		like := "%" + q.Search + "%"
		args = append(args, like, like)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages"+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计消息: %w", err)
	}
	listSQL := "SELECT " + s.selectList() + " FROM messages" + whereSQL +
		" ORDER BY time DESC LIMIT ? OFFSET ?"
	listArgs := append(append([]any{}, args...), q.Limit, q.Offset)
	rows, err := s.db.QueryContext(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询消息: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Get 按消息 ID 查询单条消息。
func (s *Store) Get(ctx context.Context, mid string) (*Message, error) {
	q := "SELECT " + s.selectList() + " FROM messages WHERE " + s.midExpr + " = ? LIMIT 1"
	m, err := scanMessage(s.db.QueryRowContext(ctx, q, mid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询消息: %w", err)
	}
	return m, nil
}

// Delete 删除单条消息并同步 stats 计数。
func (s *Store) Delete(ctx context.Context, mid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE "+s.midExpr+" = ?", mid)
	if err != nil {
		return fmt.Errorf("删除消息: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	s.bumpStats(ctx, tx, -n)
	return tx.Commit()
}

// ClearTopic 清空某话题的全部消息。
func (s *Store) ClearTopic(ctx context.Context, topic string) (int64, error) {
	if strings.TrimSpace(topic) == "" {
		return 0, errors.New("话题不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE topic = ?", topic)
	if err != nil {
		return 0, fmt.Errorf("清空话题: %w", err)
	}
	n, _ := res.RowsAffected()
	s.bumpStats(ctx, tx, -n)
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// bumpStats 调整 stats 表中的消息计数（存在 stats 表时）。
func (s *Store) bumpStats(ctx context.Context, tx *sql.Tx, delta int64) {
	var hasStats bool
	_ = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='stats'`).Scan(&hasStats)
	if !hasStats {
		return
	}
	_, _ = tx.ExecContext(ctx,
		`UPDATE stats SET value = MAX(0, value + ?) WHERE key = 'messages'`, delta)
}

// Stats 返回缓存总体统计。
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&st.MessageCount); err != nil {
		return st, fmt.Errorf("统计消息: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT topic) FROM messages`).Scan(&st.TopicCount); err != nil {
		return st, fmt.Errorf("统计话题: %w", err)
	}
	if fi, err := os.Stat(s.path); err == nil {
		st.FileSize = fi.Size()
	}
	return st, nil
}
