package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/store"
)

// stubNotifier 可配置成"永远失败"，用来验证发送失败时不写库。
// 注意不要和 weather_test.go 里的 fakeNotifier 重名 —— 它们在同一个包里。
type stubNotifier struct {
	sent []string
	err  error // 非 nil 时 NotifyMarkdown 一律返回它
}

func (s *stubNotifier) Notify(ctx context.Context, content string) error { return nil }
func (s *stubNotifier) NotifyTo(ctx context.Context, chatID int64, content string) error {
	return nil
}
func (s *stubNotifier) NotifyMarkdown(ctx context.Context, content string) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, content)
	return nil
}

// newTestStore 开一个指向临时目录的真实 SQLite 库（不用 fake，直接测真实行为）。
func newTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testItem() model.NotifyBase {
	return model.NotifyBase{
		Source:     "v2ex",
		ExternalID: "https://v2ex.com/t/1",
		Title:      "标题",
		PostTitle:  "标题",
		URL:        "https://v2ex.com/t/1",
		Content:    "*热帖推送*: [标题](https://v2ex.com/t/1)\n",
	}
}

// 发送成功 → 必须留下记录。
func TestDeliverRecordsOnSuccess(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{}
	st := newTestStore(t)

	deliver(ctx, n, st, "-100123", []model.NotifyBase{testItem()})

	if len(n.sent) != 1 {
		t.Fatalf("应发出 1 条消息，实际 %d 条", len(n.sent))
	}
	ok, err := st.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if !ok {
		t.Fatalf("发送成功后应写入记录")
	}
}

// 发送失败 → 不能留下记录，否则在永久去重下这条会永远丢失。
func TestDeliverSkipsRecordOnSendFailure(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{err: errors.New("telegram 挂了")}
	st := newTestStore(t)

	deliver(ctx, n, st, "-100123", []model.NotifyBase{testItem()})

	ok, err := st.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("发送失败时不应写入记录（否则下轮不会重试）")
	}
}
