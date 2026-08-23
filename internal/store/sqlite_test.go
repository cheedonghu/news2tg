package store

import (
	"context"       // 测试里用 context.Background()
	"path/filepath" // 拼临时目录下的库文件路径
	"testing"       // Go 官方测试框架
	"time"
)

// newTestStore 开一个指向临时目录的真实 SQLite 库。
// t.TempDir() 返回的目录会在测试结束后自动删除，不用手动清理。
// t.Cleanup 注册收尾函数，保证连接一定被关掉。
func newTestStore(t *testing.T) *SQLite {
	t.Helper() // 标记为辅助函数：失败时行号指向调用处而不是这里
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%q) 失败: %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAlreadyPushedAndMark(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 1) 空库：没推过
	ok, err := s.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("空库应返回 false")
	}

	// 2) 写入后：推过了
	rec := Record{
		Source:     "v2ex",
		ExternalID: "https://v2ex.com/t/1",
		Title:      "测试帖子",
		URL:        "https://v2ex.com/t/1",
		ChatID:     "-100123",
		PushedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("MarkPushed 报错: %v", err)
	}
	ok, err = s.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if !ok {
		t.Fatalf("写入后应返回 true")
	}
}

// 重复写入必须幂等：发送成功但写库失败的重试路径依赖这一点，
// 否则唯一约束冲突会在日志里刷 ERROR。
func TestMarkPushedIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := Record{
		Source:     "hackernews",
		ExternalID: "44556677",
		Title:      "Some HN Post",
		URL:        "https://news.ycombinator.com/item?id=44556677",
		ChatID:     "-100123",
		PushedAt:   time.Now(),
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("第一次 MarkPushed 报错: %v", err)
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("重复 MarkPushed 应幂等，却报错: %v", err)
	}
}

// 不同 source 下的相同 external_id 互不干扰（联合唯一，不是单列唯一）。
func TestSourcesAreIsolated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.MarkPushed(ctx, Record{
		Source: "v2ex", ExternalID: "42", Title: "t", URL: "u",
		ChatID: "c", PushedAt: time.Now(),
	}); err != nil {
		t.Fatalf("MarkPushed 报错: %v", err)
	}
	ok, err := s.AlreadyPushed(ctx, "hackernews", "42")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("另一个 source 的相同 id 不应被视作推过")
	}
}
