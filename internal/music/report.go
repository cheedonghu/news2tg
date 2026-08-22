package music

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/tools"
)

// throttleInterval 是两次**真正发出**编辑请求之间的最小间隔。
//
// 为什么要节流：编辑请求和普通推送共用 notify.Telegram 里那把全局发送锁，
// 每发一次就多占一次 1.5s 的锁，会挤占 HN/V2EX 的推送。3s 是实测下
// "看起来仍然实时"和"别太吵"之间的折中；觉得挤就把这个常量调大。
const throttleInterval = 3 * time.Second

// Stage 是任务所处的阶段。
type Stage int

const (
	StageSearching   Stage = iota // 正在搜索（含换词重搜）
	StageDownloading              // 已选定候选，正在下载
	StageUploading                // 正在上传到 WebDAV 目标
	StageDone                     // 成功收尾
	StageFailed                   // 失败收尾
)

// TargetState 是单个上传目标的状态。
type TargetState int

const (
	TargetPending TargetState = iota // 排队中
	TargetRunning                    // 上传中
	TargetOK                         // 成功
	TargetFailed                     // 失败
)

// TargetStatus 是单个上传目标的快照。
type TargetStatus struct {
	Name  string      // 展示名，如 "阿里云盘"
	State TargetState //
	Err   string      // 失败原因，成功时为空
}

// Track 是 agent 产出的"已下载好的曲目"。
//
// 定义在这里而不是 agent.go：Status 要引用它，而 Status 属于进度模型，
// 让进度模型去依赖 agent 会把依赖方向搞反。
type Track struct {
	LocalPath string // 临时目录下的 mp3 文件绝对路径
	Artist    string // 模型规范化后的中文歌手名（用于拼文件名）
	Title     string // 模型规范化后的中文歌名
	Duration  string // 站点给的时长，如 "03:58"
	Bytes     int64  // 实际写入字节数（不信任 Content-Length）
	Source    string // 源标识，如 "mp3pm"
	Tokens    int    // 本次任务累计消耗的 token
}

// Status 是一次任务的**完整快照**。
//
// 为什么是覆盖式完整快照而不是增量事件？
// 因为每个快照都自洽，所以节流时丢弃中间态是安全的 —— 这是整个节流设计成立的前提。
// 增量事件一旦丢一条，后面的状态就全错了。
type Status struct {
	Query    string         // 用户原始输入
	Stage    Stage          //
	Searches int            // 已搜索次数（含换词重搜）
	Found    int            // 最近一次搜索的候选数
	Track    *Track         // 选定并下载后才非 nil
	Targets  []TargetStatus // 上传目标状态，上传阶段才非空
	Err      string         // 失败原因
}

// Reporter 是一次任务的进度出口。
//
// 实现方负责渲染与节流；调用方（agent / uploader / runner）只管灌快照，
// 不知道背后是 Telegram 还是别的什么。单测时塞个记录所有快照的 fake 即可。
type Reporter interface {
	// Update 报告一个中间快照。实现可以丢弃它（节流），调用方不应假设它一定发出去了。
	Update(ctx context.Context, s Status)
	// Done 报告终态。**必须立即发出**，不受节流约束。
	Done(ctx context.Context, s Status)
}

// 编译期断言。
var _ Reporter = (*tgReporter)(nil)

// tgReporter 把 Status 渲染成 MarkdownV2，通过 notify.Editor 原地编辑同一条消息。
type tgReporter struct {
	editor   notify.Editor
	chatID   int64
	throttle time.Duration

	// mu 保护下面这组状态。time.AfterFunc 的回调在另一个 goroutine 里跑，
	// 上传阶段也有多个 goroutine 并发灌快照，所以必须加锁。
	mu       sync.Mutex
	msgID    int         // 0 表示还没发过首条消息
	lastSent time.Time   // 上次**真正发出**的时刻
	pending  *Status     // 被节流丢弃的最新快照
	timer    *time.Timer // 节流窗口结束时补发用；nil 表示当前没有待补发
	finished bool        // Done 之后为 true，之后的 Update 一律忽略（早退优化，见下）

	// sendMu 把"判断该不该发 + 真正发请求"整段串成原子操作。
	//
	// 只用 mu 不够：Update/flush 的"检查 finished/terminalSent → 释放 mu →
	// 调 send"之间有空隙，一个中间态快照可能在这个空隙里被 Done 的终态反超，
	// 之后仍然把网络请求发出去，把终态覆盖成中间态（且永远没有下一条纠正它）。
	// sendMu 保证任意时刻只有一个 send() 在“判断 + 传输”，谁先摸到它，
	// 它的传输就必定发生在后来者的判断之前，从而让终态永远是最后一条。
	//
	// 加锁顺序恒定为 sendMu → mu，绝不能反过来；Update/Done/flush 调用
	// send 之前必须已经释放 mu，否则会死锁或打破这个顺序。
	sendMu       sync.Mutex
	terminalSent bool // mu 保护；一旦为 true，任何非终态的 send 直接被丢弃
}

// NewTelegramReporter 构造一个走 Telegram 原地编辑的 Reporter。
func NewTelegramReporter(e notify.Editor, chatID int64) Reporter {
	return newTGReporter(e, chatID, throttleInterval)
}

// newTGReporter 是带节流参数的内部构造函数，让测试能把节流调到毫秒级。
// 导出的 NewTelegramReporter 固定用 throttleInterval。
func newTGReporter(e notify.Editor, chatID int64, throttle time.Duration) *tgReporter {
	return &tgReporter{editor: e, chatID: chatID, throttle: throttle}
}

// Update 报告中间快照，受节流约束。
func (r *tgReporter) Update(ctx context.Context, s Status) {
	r.mu.Lock()
	if r.finished {
		// Done 之后迟到的快照（比如上传 goroutine 的回调）不能覆盖终态。
		r.mu.Unlock()
		return
	}
	// time.Since(零值) 是个巨大的正数，所以首次调用 wait 必为负，直接发出。
	wait := r.throttle - time.Since(r.lastSent)
	if wait > 0 {
		// 落在节流窗口内：只留最新快照，并安排一次窗口结束后的补发。
		// timer 非 nil 说明补发已经排上了，不用再排一次 —— 它会取到最新的 pending。
		r.pending = &s
		if r.timer == nil {
			r.timer = time.AfterFunc(wait, func() { r.flush(ctx) })
		}
		r.mu.Unlock()
		return
	}
	// 立即占住节流位再解锁：如果等 send() 里 IO 完成后才写 lastSent，
	// 另一个跟这次并发的 Update 在 IO 还没做完时算出的 wait 会用同一份
	// "过期" lastSent，同样判定 <=0 并跟着直接发一次真实请求 —— 节流窗口
	// 被并发绕过，两条真实传输谁的响应后到谁就抢到 msgID，另一条消息
	// 从此再没人编辑（孤儿）。这里提前占位，让并发的另一次调用改走上面
	// 的 pending 分支，从而只有一次真实传输在飞。
	r.lastSent = time.Now()
	r.mu.Unlock()
	r.send(ctx, s, false)
}

// Done 报告终态，无条件立即发出。
func (r *tgReporter) Done(ctx context.Context, s Status) {
	r.mu.Lock()
	// 停掉待补发：终态发出后，任何中间态补发都是倒退。
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.pending = nil
	r.finished = true
	r.mu.Unlock()

	// finished 只挡未来的 Update（见 Update 顶部的早退检查），挡不住已经
	// 在飞的并发调用（它们的早退检查在 finished=true 之前就已经通过了）。
	// 真正的“终态不会被覆盖”保证在 send() 里的 sendMu + terminalSent。
	r.send(ctx, s, true)
}

// flush 是节流窗口结束时的补发：把最新的 pending 快照发出去。
func (r *tgReporter) flush(ctx context.Context) {
	r.mu.Lock()
	r.timer = nil
	if r.finished || r.pending == nil {
		r.mu.Unlock()
		return
	}
	s := *r.pending // 解引用拷一份，锁外用
	r.pending = nil
	// 跟 Update 里的理由一样：先占住节流位，避免跟这一刻恰好并发进来的
	// Update 一起绕过节流窗口，同时触发两次真实传输。
	r.lastSent = time.Now()
	r.mu.Unlock()

	r.send(ctx, s, false)
}

// send 真正发请求：首次走 SendEditable 拿 msgID，之后走 Edit。
//
// terminal 标记这条是不是 Done 的终态快照。send 内部靠它 + terminalSent
// 做两件事：① 终态发出后，任何非终态的 send 直接被吞掉，不再落地；
// ② 终态自己永远无条件执行（哪怕 terminalSent 已经是 true —— 正常情况下
// Done 只会调一次，这个分支只是防御性的，不依赖 Done 不会被重复调用）。
//
// 整个函数体在 sendMu 内执行，包括网络 IO：这把“判断该不该发”和“真正
// 发出去”捏成一个不可分割的操作，是修掉终态被中间态覆盖这个问题的关键——
// 没有它，判断和发送之间的空隙足够另一个 goroutine 插进来抢跑。
// mu 依然只在读写共享字段的瞬间持有，IO 期间不持有 mu，这样其它 goroutine
// 的只读操作（比如另一个 Update 判断节流）不会被网络延迟卡住。
//
// 失败只记日志、不返回错误 —— 进度是辅助信息，不该反过来把主流程搞挂。
func (r *tgReporter) send(ctx context.Context, s Status, terminal bool) {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()

	r.mu.Lock()
	if r.terminalSent && !terminal {
		// 终态已经落地：这条迟到的中间态绝不能再改动消息，直接吞掉。
		r.mu.Unlock()
		return
	}
	msgID := r.msgID
	r.mu.Unlock()

	md := renderStatus(s)

	var err error
	if msgID == 0 {
		var id int
		id, err = r.editor.SendEditable(ctx, r.chatID, md)
		if err == nil {
			r.mu.Lock()
			r.msgID = id
			r.mu.Unlock()
		}
	} else {
		err = r.editor.Edit(ctx, r.chatID, msgID, md)
	}

	// 无论成败都更新时间戳：失败往往也是被限速了，更该等。
	r.mu.Lock()
	r.lastSent = time.Now()
	if terminal {
		r.terminalSent = true
	}
	r.mu.Unlock()

	if err != nil {
		slog.ErrorContext(ctx, "音乐进度上报失败", "chat", r.chatID, "err", err)
	}
}

// renderStatus 把快照渲染成 MarkdownV2。
//
// 转义约定：整条消息是**预渲染的 MarkdownV2**，Editor 不会再整体转义，
// 所以这里必须自己把每一段动态文本用 EscapeMarkdownV2 处理掉，
// 只留下我们主动写的 * 等标记不转义。
func renderStatus(s Status) string {
	esc := tools.EscapeMarkdownV2

	// strings.Builder 比反复 += 拼字符串高效（不产生中间字符串）。
	var b strings.Builder
	b.WriteString("🎵 *" + esc(s.Query) + "*\n")

	// 搜索行：搜过才显示。阶段已经越过搜索就打勾，否则显示进行中。
	if s.Searches > 0 {
		b.WriteString(stageIcon(s.Stage > StageSearching) + " " +
			esc(fmt.Sprintf("搜索 %d 次 · %d 个候选", s.Searches, s.Found)) + "\n")
	}

	// 曲目行：选定后就有（此时 Bytes 可能还是 0，下载完才有值）。
	if s.Track != nil {
		line := fmt.Sprintf("%s - %s · %s", s.Track.Artist, s.Track.Title, s.Track.Duration)
		if s.Track.Bytes > 0 {
			line += fmt.Sprintf(" · %s", humanSize(s.Track.Bytes))
		}
		if s.Track.Source != "" {
			line += " · " + s.Track.Source
		}
		b.WriteString(stageIcon(s.Track.Bytes > 0) + " " + esc(line) + "\n")
	}

	// 上传区：每个目标一行。
	if len(s.Targets) > 0 {
		b.WriteString("⬆️ 上传\n")
		for _, t := range s.Targets {
			line := "   " + targetIcon(t.State) + " " + esc(t.Name)
			if t.Err != "" {
				line += esc(": " + tools.TruncateUTF8(t.Err, 80))
			}
			b.WriteString(line + "\n")
		}
	}

	if s.Stage == StageFailed && s.Err != "" {
		b.WriteString("❌ " + esc(tools.TruncateUTF8(s.Err, 300)) + "\n")
	}

	// token 统计只在收尾时展示，中途刷它没意义。
	if s.Track != nil && s.Track.Tokens > 0 && (s.Stage == StageDone || s.Stage == StageFailed) {
		b.WriteString(esc(fmt.Sprintf("本次消耗 tokens: %d", s.Track.Tokens)))
	}

	return b.String()
}

// stageIcon 已完成打勾，进行中显示沙漏。
func stageIcon(done bool) string {
	if done {
		return "✅"
	}
	return "⏳"
}

// targetIcon 把上传目标状态映射成图标。
func targetIcon(st TargetState) string {
	switch st {
	case TargetOK:
		return "✅"
	case TargetFailed:
		return "❌"
	case TargetRunning:
		return "⏳"
	default:
		return "⬜"
	}
}

// humanSize 把字节数渲染成人看得懂的大小。
// 1<<20 = 1048576，即 1 MiB；mp3 一般几 MB，用 MB 粒度就够。
func humanSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
