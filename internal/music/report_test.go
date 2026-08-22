package music

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEditor 记录所有调用，替代真实的 Telegram 客户端。
// 加锁是因为节流用的 time.AfterFunc 会在另一个 goroutine 里回调。
type fakeEditor struct {
	mu      sync.Mutex
	sends   []string // 每次 SendEditable 的内容
	edits   []string // 每次 Edit 的内容
	sendErr error    // 非 nil 时 SendEditable 返回它
}

func (f *fakeEditor) SendEditable(_ context.Context, _ int64, md string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.sends = append(f.sends, md)
	return 42, nil // 固定 msgID，方便断言后续 Edit 用的是它
}

func (f *fakeEditor) Edit(_ context.Context, _ int64, _ int, md string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, md)
	return nil
}

func (f *fakeEditor) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends), len(f.edits)
}

// TestReporterFirstUpdateSends 验证：首次 Update 走 SendEditable，之后走 Edit。
func TestReporterFirstUpdateSends(t *testing.T) {
	fe := &fakeEditor{}
	// 节流设为 0：本用例只关心"首发 vs 编辑"的切换，不关心节流。
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天", Stage: StageSearching, Searches: 1})
	r.Update(ctx, Status{Query: "晴天", Stage: StageSearching, Searches: 2})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Errorf("SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 1 {
		t.Errorf("Edit 调用 %d 次, want 1", edits)
	}
}

// TestReporterThrottleCoalesces 验证：节流窗口内的连续 Update 被合并，
// 只有最新那个快照会在窗口结束后补发出去。
func TestReporterThrottleCoalesces(t *testing.T) {
	fe := &fakeEditor{}
	const throttle = 80 * time.Millisecond
	r := newTGReporter(fe, 1, throttle)
	ctx := context.Background()

	// 第 1 次立即发出（lastSent 是零值，不受节流约束）。
	r.Update(ctx, Status{Query: "晴天", Searches: 1})
	// 紧接着 3 次都落在节流窗口内，应该被合并成一次补发。
	r.Update(ctx, Status{Query: "晴天", Searches: 2})
	r.Update(ctx, Status{Query: "晴天", Searches: 3})
	r.Update(ctx, Status{Query: "晴天", Searches: 4})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Fatalf("节流窗口内 SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 0 {
		t.Fatalf("节流窗口内 Edit 调用 %d 次, want 0（应被合并推迟）", edits)
	}

	// 等窗口结束 + 余量，补发应当发生且只发生一次。
	time.Sleep(throttle * 3)

	sends, edits = fe.counts()
	if edits != 1 {
		t.Fatalf("窗口结束后 Edit 调用 %d 次, want 1（补发最新快照）", edits)
	}
	// 补发的必须是**最新**快照（Searches=4），而不是被丢弃的中间态。
	fe.mu.Lock()
	last := fe.edits[0]
	fe.mu.Unlock()
	if !strings.Contains(last, "4") {
		t.Errorf("补发内容 %q 里没有最新的搜索次数 4", last)
	}
}

// TestReporterDoneIsImmediate 验证：Done 无条件立即发出，不受节流限制。
// 最后一条必须准确，这是整个节流设计里唯一不能妥协的地方。
func TestReporterDoneIsImmediate(t *testing.T) {
	fe := &fakeEditor{}
	r := newTGReporter(fe, 1, 10*time.Second) // 节流拉到 10s，正常情况下绝不会补发
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天", Searches: 1}) // 首发
	r.Update(ctx, Status{Query: "晴天", Searches: 2}) // 被节流丢弃
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone, Searches: 2})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Errorf("SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 1 {
		t.Fatalf("Edit 调用 %d 次, want 1（Done 必须立即发）", edits)
	}
}

// TestReporterUpdateAfterDoneIgnored 验证：Done 之后迟到的 Update 不再改动消息。
// 上传 goroutine 的进度回调可能晚于 Done 到达，不拦住会把终态覆盖成中间态。
func TestReporterUpdateAfterDoneIgnored(t *testing.T) {
	fe := &fakeEditor{}
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天"})
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone})
	_, before := fe.counts()

	r.Update(ctx, Status{Query: "晴天", Searches: 99}) // 迟到

	if _, after := fe.counts(); after != before {
		t.Errorf("Done 之后的 Update 不应再发送（Edit 从 %d 变成 %d）", before, after)
	}
}

// TestReporterSendErrorDoesNotPanic 验证：Editor 报错时不 panic、不影响后续调用。
// 进度是辅助信息，不该反过来把主流程搞挂。
func TestReporterSendErrorDoesNotPanic(t *testing.T) {
	fe := &fakeEditor{sendErr: errors.New("telegram 挂了")}
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天"})
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone})
	// 走到这里没 panic 就算通过。首发失败时 msgID 仍为 0，
	// 所以 Done 会再走一次 SendEditable（也失败），同样必须被吞掉。
}

// TestRenderStatus 锁死渲染内容：该出现的字段都出现，MarkdownV2 特殊字符被转义。
func TestRenderStatus(t *testing.T) {
	md := renderStatus(Status{
		Query:    "晴天 - 周杰伦",
		Stage:    StageDone,
		Searches: 2,
		Found:    12,
		Track: &Track{
			Artist: "周杰伦", Title: "晴天", Duration: "03:58",
			Bytes: 8_400_000, Source: "mp3pm", Tokens: 3184,
		},
		Targets: []TargetStatus{
			{Name: "阿里云盘", State: TargetOK},
			{Name: "OneDrive", State: TargetFailed, Err: "507 Insufficient Storage"},
		},
	})

	for _, want := range []string{"周杰伦", "晴天", "03:58", "阿里云盘", "OneDrive", "3184"} {
		if !strings.Contains(md, want) {
			t.Errorf("渲染结果里缺少 %q\n实际:\n%s", want, md)
		}
	}
	// MarkdownV2 里 '-' 是特殊字符，必须被转义成 '\-'，否则 Telegram 会 400。
	if strings.Contains(md, "晴天 - 周杰伦") {
		t.Errorf("查询串里的 '-' 未被转义:\n%s", md)
	}
	if !strings.Contains(md, `\-`) {
		t.Errorf("渲染结果里没有任何转义过的 '-':\n%s", md)
	}
}

// TestRenderStatusFailed 验证失败态把错误原因渲染出来。
func TestRenderStatusFailed(t *testing.T) {
	md := renderStatus(Status{
		Query: "不存在的歌",
		Stage: StageFailed,
		Err:   "模型未收敛",
	})
	if !strings.Contains(md, "模型未收敛") {
		t.Errorf("失败态应展示错误原因:\n%s", md)
	}
}
