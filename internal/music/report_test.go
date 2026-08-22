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
// 加锁是因为节流用的 time.AfterFunc 会在另一个 goroutine 里回调，
// 并发测试里还会有多个业务 goroutine 直接并发调用 Update/Done。
type fakeEditor struct {
	mu      sync.Mutex
	sends   []string // 每次 SendEditable 的内容
	edits   []string // 每次 Edit 的内容
	editIDs []int    // 每次 Edit 用的 msgID，跟 edits 按下标一一对应；
	// 用来验证不会有消息被"孤儿化"（连续两次 SendEditable 各开一条新消息，
	// 之后 Edit 却只认得其中一条 msgID，另一条从此没人再编辑）。
	calls []string // sends 和 edits 的合并时间线，按真实发生顺序追加；
	// 单独两个切片看不出"谁在谁后面"，判断"终态是不是最后一条真实传输"
	// 必须靠这个统一时间线。
	sendErr error // 非 nil 时 SendEditable 返回它
}

// 两个方法都先查 ctx —— 这是**刻意**照抄 notify.Telegram.sendRaw 的行为：
// 它的第一句就是 `if err := ctx.Err(); err != nil { return ... }`，ctx 一死
// 请求根本不会发出。fake 若忽略 ctx，就测不出"终态被已取消的 ctx 悄悄吞掉"
// 这类缺陷 —— 假实现比真实现宽容，是这类回归测试最常见的失效方式。
func (f *fakeEditor) SendEditable(ctx context.Context, _ int64, md string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.sends = append(f.sends, md)
	f.calls = append(f.calls, md)
	return 42, nil // 固定 msgID，方便断言后续 Edit 用的是它
}

func (f *fakeEditor) Edit(ctx context.Context, _ int64, msgID int, md string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, md)
	f.editIDs = append(f.editIDs, msgID)
	f.calls = append(f.calls, md)
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

// TestReporterDoneSurvivesCancelledCtx 是补给 Important 3 的回归测试。
//
// 场景：运维在下载中途重启服务（SIGTERM）→ ctx 被取消 → Fetch 返回
// context.Canceled → Runner 置 StageFailed 并调 Done，可它手上只有那个
// **已经死掉**的 ctx。notify.Telegram.sendRaw 的第一句就是 ctx.Err() 检查，
// 于是终态压根不会被发出去；偏偏 send() 已经把 terminalSent 置真，
// 之后没有任何一帧能再纠正它 —— 用户那条消息永久冻结在中间态，
// 看不出任务已经失败了。这违反设计里唯一不能让步的那条硬约束
// （"Done 无条件立即发出……最后一条必须准确"）。
//
// 判别力：fakeEditor 的两个方法都照 sendRaw 的样子先查 ctx（见其注释），
// 所以修复前（Done 直接把调用方的 ctx 透传给 send）这个用例必然失败。
func TestReporterDoneSurvivesCancelledCtx(t *testing.T) {
	// 子用例一：首帧都还没发出去，ctx 就死了 —— 终态得走 SendEditable。
	t.Run("首帧尚未发出", func(t *testing.T) {
		fe := &fakeEditor{}
		r := newTGReporter(fe, 1, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		r.Done(ctx, Status{Query: "晴天", Stage: StageFailed, Err: "context canceled"})

		fe.mu.Lock()
		defer fe.mu.Unlock()
		if len(fe.calls) != 1 {
			t.Fatalf("ctx 已取消时终态仍必须发出，实际发生 %d 次传输", len(fe.calls))
		}
		if !strings.Contains(fe.calls[0], "context canceled") {
			t.Errorf("终态里应带上失败原因，实际: %s", fe.calls[0])
		}
	})

	// 子用例二：进度消息已经存在，任务中途被取消 —— 终态得把那条消息改掉。
	t.Run("中途取消", func(t *testing.T) {
		fe := &fakeEditor{}
		r := newTGReporter(fe, 1, 0)
		ctx, cancel := context.WithCancel(context.Background())

		r.Update(ctx, Status{Query: "晴天", Stage: StageSearching, Searches: 1}) // 首帧，此时 ctx 还活着
		cancel()                                                               // 收到 SIGTERM
		r.Done(ctx, Status{Query: terminalMarker, Stage: StageFailed, Err: "context canceled"})

		fe.mu.Lock()
		defer fe.mu.Unlock()
		if len(fe.edits) != 1 {
			t.Fatalf("ctx 已取消时终态仍必须改掉那条进度消息，实际 Edit %d 次", len(fe.edits))
		}
		if !strings.Contains(fe.calls[len(fe.calls)-1], terminalMarker) {
			t.Errorf("最后一次传输不是终态内容: %s", fe.calls[len(fe.calls)-1])
		}
	})
}

// slowEditor 的**第一次**调用会阻塞 block 时长，之后的调用立即返回。
// 用来模拟"另一次传输正卡在 Telegram 的网络 IO 上、把 sendMu 攥着不放"。
// 同 fakeEditor，两个方法都先查 ctx —— 照抄 notify.Telegram.sendRaw 的行为。
type slowEditor struct {
	mu    sync.Mutex
	block time.Duration
	calls int
	edits []string
	sends []string
}

// wait 决定这次调用要不要阻塞：只有第一次阻塞。
// 阻塞发生在锁外，否则会把并发调用挡在 slowEditor 自己的 mu 上，
// 测的就不是 sendMu 了。
func (s *slowEditor) wait() {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		time.Sleep(s.block)
	}
}

func (s *slowEditor) SendEditable(ctx context.Context, _ int64, md string) (int, error) {
	s.wait()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, md)
	return 42, nil
}

func (s *slowEditor) Edit(ctx context.Context, _ int64, _ int, md string) error {
	s.wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits = append(s.edits, md)
	return nil
}

// TestReporterDoneBudgetStartsAfterLockAcquired 是补给"修复波自身引入的回归"的用例。
//
// 缺陷（I3 修复的副作用）：Done 在**入口**就用 WithTimeout 起了终态预算，可
// send() 随后还要在 sendMu 上排队。持锁的那次传输不是 1.5s 封顶 —— 它的上界是
// 「最多 1.5s 全局限速 + notify 客户端的 30s HTTP 超时」，接近 31.5s。
// 排队一旦超过预算，轮到终态时 dctx 已经过期，sendRaw 首句直接返回，
// 而 terminalSent 照样置真 —— 消息永久冻结在中间态，正是 I3 要防的症状。
// 注意这条在修复前是**能正常发出**的（活着的 ctx 上没有 deadline），
// 所以它是修复引入的回归，而不是原有缺口。
//
// 时序：Update 先抢到 sendMu 并卡在 200ms 的 IO 里；Done 随后进来，
// 在锁上排队约 200ms —— 远超压到 50ms 的终态预算。
// 修复后预算从"拿到锁"才起算，终态照常发出。
func TestReporterDoneBudgetStartsAfterLockAcquired(t *testing.T) {
	se := &slowEditor{block: 200 * time.Millisecond}
	r := newTGReporter(se, 1, 0)
	// 压到 50ms：远小于上面 200ms 的排队时间。用真实的 10s 就得真等 10s 才测得出来。
	r.terminalTimeout = 50 * time.Millisecond

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 首帧：拿到 sendMu 后卡在 SendEditable 里 200ms。
		r.Update(ctx, Status{Query: "晴天", Searches: 1})
	}()

	// 给上面那个 goroutine 一点时间真正抢到 sendMu 并进入 IO。
	time.Sleep(40 * time.Millisecond)

	// 终态：会在 sendMu 上排队约 160ms，远超 50ms 的预算。
	r.Done(ctx, Status{Query: terminalMarker, Stage: StageDone})
	wg.Wait()

	se.mu.Lock()
	defer se.mu.Unlock()
	if len(se.edits) != 1 {
		t.Fatalf("终态在 sendMu 上排队期间把预算烧光了，没能发出：Edit %d 次, want 1", len(se.edits))
	}
	if !strings.Contains(se.edits[0], terminalMarker) {
		t.Errorf("发出的不是终态内容: %s", se.edits[0])
	}
}

// TestReporterFlushUsesLatestCtx 是补给 Minor A 的回归测试。
//
// 缺陷：time.AfterFunc 的闭包捕获的是**第一个开窗的那次 Update** 的 ctx。
// Runner 里 agent 那层 context.WithTimeout 的 cancel() 是非 defer 的
// （上传不该再受 agent 超时约束），所以进入上传阶段的瞬间那个 ctx 就死了；
// 补发时若还拿着它，notify.Telegram.sendRaw 首句的 ctx.Err() 会把整帧丢掉 ——
// 丢的是一帧真实进度（通常正是第一帧 "⬆️ 上传"），而且 lastSent 照样前进，
// 下一帧还得再等一个完整的节流窗口。
//
// 这里就是照着那条时序摆的：ctx1 开窗 → ctx1 被 cancel（模拟 agent 阶段结束）
// → 用还活着的 ctx2 再灌一帧 → 等补发。补发必须真的发出去。
func TestReporterFlushUsesLatestCtx(t *testing.T) {
	fe := &fakeEditor{}
	const throttle = 60 * time.Millisecond
	r := newTGReporter(fe, 1, throttle)

	base := context.Background()
	r.Update(base, Status{Query: "晴天", Searches: 1}) // 首帧，立即发出

	// agent 阶段那个带超时的 ctx：它开了节流窗口，随后就被 cancel 了。
	actx, cancel := context.WithCancel(base)
	r.Update(actx, Status{Query: "晴天", Searches: 2}) // 落进窗口，开窗并排上补发
	cancel()                                         // Runner 里那句非 defer 的 cancel()

	// 上传阶段用的是外层还活着的 ctx，再灌一帧最新状态。
	r.Update(base, Status{Query: "晴天", Searches: 2, Targets: []TargetStatus{
		{Name: "阿里云盘", State: TargetPending},
	}})

	time.Sleep(throttle * 3) // 等补发跑完

	fe.mu.Lock()
	defer fe.mu.Unlock()
	if len(fe.edits) != 1 {
		t.Fatalf("补发的那一帧被已取消的 ctx 丢掉了：Edit %d 次, want 1", len(fe.edits))
	}
	if !strings.Contains(fe.edits[0], "上传") {
		t.Errorf("补发的应是最新那帧（含上传区），实际: %s", fe.edits[0])
	}
}

// TestRenderStatusFailedDuringSearch 是补给 Minor C(a) 的回归测试：
// 搜索阶段就失败的任务，搜索行绝不能打勾。
// 旧写法 stageIcon(s.Stage > StageSearching) 对 StageFailed(=4) 恒为真，
// 会渲染出 "✅ 搜索 1 次 · 0 个候选" 紧跟一个 ❌ 的自相矛盾画面。
func TestRenderStatusFailedDuringSearch(t *testing.T) {
	md := renderStatus(Status{
		Query:    "不存在的歌",
		Stage:    StageFailed,
		Searches: 1,
		Err:      "模型未收敛",
	})
	if strings.Contains(md, "✅") {
		t.Errorf("搜索阶段就失败时不该有任何 ✅:\n%s", md)
	}
	if !strings.Contains(md, "⏳") {
		t.Errorf("搜索行应显示为进行中（⏳）:\n%s", md)
	}

	// 反面：曲目已经下好、栽在上传上的失败，搜索行仍然该打勾 ——
	// 修 (a) 不能矫枉过正把这种情况也改成 ⏳。
	md = renderStatus(Status{
		Query:    "晴天",
		Stage:    StageFailed,
		Searches: 1,
		Found:    12,
		Track:    &Track{Artist: "周杰伦", Title: "晴天", Duration: "03:58", Bytes: 100},
		Err:      "全部 2 个 WebDAV 目标上传失败",
	})
	if !strings.Contains(md, "✅") {
		t.Errorf("搜索成功、上传失败时搜索行仍应打勾:\n%s", md)
	}
}

// TestRenderStatusPendingTarget 是补给 Minor D 的回归测试：
// 排队中的目标渲染成 ⬜。这个分支原先永远走不到（Upload 把所有目标
// 直接初始化成 TargetRunning），TargetPending 是死代码。
func TestRenderStatusPendingTarget(t *testing.T) {
	md := renderStatus(Status{
		Query:   "晴天",
		Stage:   StageUploading,
		Targets: []TargetStatus{{Name: "阿里云盘", State: TargetPending}},
	})
	if !strings.Contains(md, "⬜") {
		t.Errorf("排队中的目标应渲染成 ⬜:\n%s", md)
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

// terminalMarker 是下面两个并发测试里终态快照专用的可识别标记：放进 Query
// 字段，跟并发 Update 用的固定 Query 区分开，这样才能从渲染结果里认出
// "这一条究竟是不是 Done 发的那条"，而不用去猜并发的具体调度顺序。
const terminalMarker = "终态标记ZZ"

// TestReporterConcurrentDoneNeverOverwritten 是补给 Critical 1 的回归测试：
// 一批 Update 跟 Done 真正并发地对同一个 tgReporter 灌快照（不是顺序调用），
// 覆盖"goroutine A 的 Update 通过了 finished 检查、还没来得及真正发送，
// goroutine B 的 Done 就已经把终态发出去了"这种交错。
//
// 断言点分两块：
//  1. 不管调度怎么交错，真正落地的最后一次传输永远是终态内容——这是设计上
//     唯一不能退让的不变式，靠 send() 里的 sendMu + terminalSent 保证，
//     而不是靠"运气好没撞上"。
//  2. 不会有消息被孤儿化：SendEditable 全程只应该被调用一次（否则出现第二条
//     没人再编辑的孤儿消息），后续所有 Edit 用的都是同一个 msgID。
func TestReporterConcurrentDoneNeverOverwritten(t *testing.T) {
	// 多跑几轮：单次并发测试可能靠运气就通过，多轮 + 较大的 goroutine 数量
	// 才能有把握真的把交错窗口踩到。
	for round := 0; round < 20; round++ {
		fe := &fakeEditor{}
		// 节流拉到 0：让每个并发 Update 都走"立即发"分支，
		// 而不是被节流吞进 pending，这样才能最大化跟 Done 抢跑的窗口。
		r := newTGReporter(fe, 1, 0)
		ctx := context.Background()

		const n = 100
		var wg sync.WaitGroup
		wg.Add(n + 1)
		start := make(chan struct{}) // 关闭它当发令枪，让所有 goroutine 尽量同时起跑
		for i := 0; i < n; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				r.Update(ctx, Status{Query: "晴天", Searches: i})
			}()
		}
		go func() {
			defer wg.Done()
			<-start
			r.Done(ctx, Status{Query: terminalMarker, Stage: StageDone})
		}()
		close(start)
		wg.Wait()

		fe.mu.Lock()
		if len(fe.sends) != 1 {
			fe.mu.Unlock()
			t.Fatalf("round %d: SendEditable 调用 %d 次, want 1（多出来的是孤儿消息）", round, len(fe.sends))
		}
		if len(fe.calls) == 0 {
			fe.mu.Unlock()
			t.Fatalf("round %d: 没有任何调用被记录", round)
		}
		last := fe.calls[len(fe.calls)-1]
		if !strings.Contains(last, terminalMarker) {
			timeline := strings.Join(fe.calls, "\n---\n")
			fe.mu.Unlock()
			t.Fatalf("round %d: 最后一次真实传输不是终态内容:\n%s\n完整时间线:\n%s", round, last, timeline)
		}
		for i, id := range fe.editIDs {
			if id != 42 {
				fe.mu.Unlock()
				t.Fatalf("round %d: 第 %d 次 Edit 用的 msgID=%d, want 42（说明消息被孤儿化了）", round, i, id)
			}
		}
		fe.mu.Unlock()
	}
}

// TestReporterConcurrentUpdatesCoalesce 是补给 Critical 2 的回归测试：
// 同一节流窗口内并发涌入的 Update，只应该产生"首发已经发过的那一次"真实
// 传输，不应该因为并发而让节流闸门被同时绕过好几次。
//
// 关键：不能先顺序发一条把 lastSent "焐热"再起并发批次——哪怕在旧代码上，
// 那一条顺序调用也会完整跑完（它本来就没有任何并发对手），等它返回时
// lastSent 早已经写回，后面的并发批次天然安全，测的是 pending/timer 的
// 合并逻辑（这部分从来没坏过），根本碰不到 bug 真正的触发条件。
//
// 真正会触发旧 bug 的窗口是"冷启动"：lastSent 还是零值、第一次真实发送
// 还在飞的那一小段时间。旧实现里 lastSent 只在 IO 完成后才写回，于是这段
// 时间内所有并发到达的 Update 都用同一份"零值/过期" lastSent 判定
// "该发了"，各自真的调一次 SendEditable——多出来的消息永远没人再编辑
// （孤儿）。这里直接从冷启动开始并发灌 N 个 Update，不做任何顺序铺垫，
// 验证修复后（gate 通过瞬间在锁内占位 lastSent + send() 内 sendMu 把
// "判断该不该发+真正发送"串成原子操作）整个过程里 SendEditable 只会
// 被调用一次，其余全部落地为对同一个 msgID 的 Edit。
func TestReporterConcurrentUpdatesCoalesce(t *testing.T) {
	for round := 0; round < 20; round++ {
		fe := &fakeEditor{}
		// 节流拉到 0：不依赖 pending/timer 分支，让每个 goroutine 都直接
		// 走 gate 判断这条路径，最大化触发 bug 的窗口。
		r := newTGReporter(fe, 1, 0)
		ctx := context.Background()

		const n = 50
		var wg sync.WaitGroup
		wg.Add(n)
		start := make(chan struct{}) // 发令枪：所有 goroutine 从冷启动状态一起起跑
		for i := 0; i < n; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				r.Update(ctx, Status{Query: "晴天", Searches: i})
			}()
		}
		close(start)
		wg.Wait()

		fe.mu.Lock()
		if len(fe.sends) != 1 {
			timeline := strings.Join(fe.calls, "\n---\n")
			fe.mu.Unlock()
			t.Fatalf("round %d: SendEditable 调用 %d 次, want 1（冷启动并发下出现了不止一次首发，说明有消息被孤儿化）\n完整时间线:\n%s",
				round, len(fe.sends), timeline)
		}
		for i, id := range fe.editIDs {
			if id != 42 {
				fe.mu.Unlock()
				t.Fatalf("round %d: 第 %d 次 Edit 用的 msgID=%d, want 42（跟唯一一次 SendEditable 返回的不一致，说明消息被孤儿化）",
					round, i, id)
			}
		}
		fe.mu.Unlock()
	}
}
