package monitor

import (
	"context"
	"database/sql" // 直接查库用：Store 接口只暴露 AlreadyPushed(bool)，读不出字段值
	"errors"
	"fmt"
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
// 顺带把库文件路径也返回：Store 接口只暴露 AlreadyPushed（返回 bool）/MarkPushed，
// 读不出具体字段值，个别测试需要绕开接口直接对文件发 SQL 查询（见 queryStoredTitle）。
func newTestStore(t *testing.T) (*store.SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

// queryStoredTitle 绕开 Store 接口，直接开一条独立连接查某条记录的 title 字段。
// 不为了测试给 Store 接口加"读字段"的方法 —— 那是拿测试便利污染生产接口；
// "sqlite" 驱动名已经在 store 包的 blank import（modernc.org/sqlite）里注册过一次，
// 整个测试进程共享同一份驱动注册表，这里直接复用即可，不用再 import 一次驱动包。
func queryStoredTitle(t *testing.T, dbPath, source, externalID string) string {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开查询连接失败: %v", err)
	}
	defer db.Close()

	var title string
	const q = `SELECT title FROM pushed_posts WHERE source = ? AND external_id = ?`
	if err := db.QueryRow(q, source, externalID).Scan(&title); err != nil {
		t.Fatalf("查询 title 失败: %v", err)
	}
	return title
}

func testItem() model.NotifyBase {
	return model.NotifyBase{
		Source:     "v2ex",
		ExternalID: "https://v2ex.com/t/1",
		Title:      "V2EX 热帖推送", // 消息抬头：写库时不该用这个字段
		PostTitle:  "帖子真实标题",    // 帖子真实标题：应该落进 pushed_posts.title
		URL:        "https://v2ex.com/t/1",
		Content:    "*热帖推送*: [标题](https://v2ex.com/t/1)\n",
	}
}

// 发送成功 → 必须留下记录，且记录的 title 是 PostTitle（帖子真实标题），
// 不是 Title（消息抬头）—— 这是本任务被点名强调的要求：HN 的 Title 是分类抬头
// "Hacker News 热帖推送"，如果写库时误用 Title，历史记录里每行标题都会一样。
func TestDeliverRecordsOnSuccess(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{}
	st, dbPath := newTestStore(t)

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

	gotTitle := queryStoredTitle(t, dbPath, "v2ex", "https://v2ex.com/t/1")
	if gotTitle != testItem().PostTitle {
		t.Fatalf("记录里的 title 应为 PostTitle %q，实际是 %q（说明写库时误用了 Title）",
			testItem().PostTitle, gotTitle)
	}
}

// 发送失败 → 不能留下记录，否则在永久去重下这条会永远丢失。
func TestDeliverSkipsRecordOnSendFailure(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{err: errors.New("telegram 挂了")}
	st, _ := newTestStore(t)

	deliver(ctx, n, st, "-100123", []model.NotifyBase{testItem()})

	ok, err := st.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("发送失败时不应写入记录（否则下轮不会重试）")
	}
}

// ctx 在调用前已经被取消（模拟进程正在关闭）→ deliver 必须正常返回、不 panic，
// 且这条因 ctx 取消而失败的消息不能被记账。
//
// 这个提前 return 分支不在最初的 brief 里，是审查追加的要求，必须单独覆盖 ——
// 上面两个用 context.Background() 的测试 ctx.Err() 恒为 nil，从没走到这条分支。
func TestDeliverReturnsSilentlyWhenCtxAlreadyCancelled(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // 提前取消，模拟"发送时进程正在关闭"

	// 真实场景里 ctx 取消后 NotifyMarkdown（内部会往 ctx 挂 http 请求）大概率也会
	// 因为 ctx 取消而报错，这里直接固定返回 context.Canceled 模拟这个结果。
	n := &stubNotifier{err: context.Canceled}
	st, _ := newTestStore(t)

	// 如果 deliver 在提前 return 分支里 panic 了，testing 框架会直接判这个测试失败，
	// 不需要额外用 recover 断言"没 panic"。
	deliver(cancelCtx, n, st, "-100123", []model.NotifyBase{testItem()})

	// 查库要用一个没被取消的 ctx：如果用 cancelCtx 发查询，QueryRowContext 会直接
	// 因为 ctx.Err() 报错退出，那样测的是"查询请求被拒绝"，不是我们要验证的
	// "记录确实没被写入"。
	ok, err := st.AlreadyPushed(context.Background(), "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("ctx 取消导致的发送失败不应写入记录")
	}
}
