package store

import (
	"context"
	"database/sql" // 标准库的 SQL 抽象层；具体驱动通过匿名 import 注册
	"errors"       // errors.Is 判断哨兵错误
	"fmt"
	"time"

	// 匿名 import（下划线别名）：只为触发包的 init()，把驱动注册进 database/sql。
	// modernc.org/sqlite 注册的驱动名是 "sqlite"（注意不是 "sqlite3"）。
	// 它是把 SQLite 的 C 源码机器翻译成的纯 Go 实现，所以 CGO_ENABLED=0 也能编译。
	_ "modernc.org/sqlite"
)

// SQLite 是 Store 接口的 SQLite 实现。
type SQLite struct {
	db *sql.DB // *sql.DB 本身是连接池，并发安全，不要拷贝
}

// schemaSQL 建表语句。用 IF NOT EXISTS，所以每次启动执行都是安全的 ——
// 这就是本项目全部的"迁移"机制。
//
// (source, external_id) 联合唯一：两个来源的 id 空间不同（URL vs 数字），
// 加上 source 前缀就不会撞车。这个唯一索引同时充当去重查询的索引，不用另建。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS pushed_posts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL,
    external_id TEXT NOT NULL,
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    pushed_at   TEXT NOT NULL,
    UNIQUE(source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_pushed_at ON pushed_posts(pushed_at);
`

// OpenSQLite 打开（或创建）库文件并建表。
//
// DSN 里的 _pragma 是 modernc 驱动的扩展语法，每个连接建立时都会执行：
//   - busy_timeout(5000)  锁冲突时最多等 5 秒再报错，而不是立刻失败。
//     必须写在 DSN 里 —— 它是"每连接"设置，连接池里每条连接都要有。
//   - journal_mode(WAL)   写前日志模式，读不阻塞写。这个设置会持久化进库文件。
func OpenSQLite(path string) (*SQLite, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)

	// sql.Open 只是准备好连接池，并不会真正连接，所以下面还要 Ping 一次。
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite %q 失败: %w", path, err)
	}
	// Ping 才会真正建连：路径不可写、目录不存在等问题在这里暴露。
	if err := db.Ping(); err != nil {
		db.Close() // 失败路径要记得关，否则连接池泄漏
		return nil, fmt.Errorf("连接 sqlite %q 失败: %w", path, err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}
	return &SQLite{db: db}, nil
}

// AlreadyPushed 查这条记录是否已存在。
// SELECT 1 ... LIMIT 1：只关心"有没有"，不取实际字段，最省事。
func (s *SQLite) AlreadyPushed(ctx context.Context, source, externalID string) (bool, error) {
	const q = `SELECT 1 FROM pushed_posts WHERE source = ? AND external_id = ? LIMIT 1`
	var one int
	// QueryRowContext + Scan：期望最多一行。没有行时 Scan 返回 sql.ErrNoRows。
	err := s.db.QueryRowContext(ctx, q, source, externalID).Scan(&one)
	// errors.Is 而不是 ==：驱动可能包装过这个哨兵错误。
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // 没查到 = 没推过，这不是错误
	}
	if err != nil {
		return false, fmt.Errorf("查询 pushed_posts 失败: %w", err)
	}
	return true, nil
}

// MarkPushed 写一条记录。
//
// ON CONFLICT ... DO NOTHING 让重复写入变成静默的空操作。
// 这很重要：发送成功但写库失败时，条目下一轮会被重推并再次写入，
// 若这里报唯一约束冲突，日志会被无意义的 ERROR 刷屏。
func (s *SQLite) MarkPushed(ctx context.Context, rec Record) error {
	const q = `INSERT INTO pushed_posts (source, external_id, title, url, chat_id, pushed_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(source, external_id) DO NOTHING`
	// 时间统一存 RFC3339 字符串：它的字典序等于时间序，
	// 所以 WHERE pushed_at > '2026-07-01' 这类区间查询能直接用。
	_, err := s.db.ExecContext(ctx, q,
		rec.Source, rec.ExternalID, rec.Title, rec.URL, rec.ChatID,
		rec.PushedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("写入 pushed_posts 失败: %w", err)
	}
	return nil
}

// Close 关闭连接池。main 里 defer 调用。
func (s *SQLite) Close() error {
	return s.db.Close()
}

// 编译期断言：如果 *SQLite 没有完整实现 Store 接口，这行会编译失败。
// 这是 Go 里检查"实现了某接口"的标准写法（变量名用 _ 表示不占用符号）。
var _ Store = (*SQLite)(nil)
