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

// terminalSendTimeout 是终态那一帧**自己这次传输**的预算。
//
// Done 会用 context.WithoutCancel 摘掉调用方的取消信号（见 Done 的注释），
// 但不能就此变成一次没有任何时限的发送，所以另配一个自己的超时。
//
// ⚠️ 这个预算是在 send() 里**拿到 sendMu 之后**才开始计时的，不是在 Done 入口
// 就开始烧。这个区别很要命，别"顺手"把它挪回 Done 里去：
//
// sendMu 可能正被另一次 Update/flush 的传输持有，而那次传输的时长上界**不是
// 1.5s** —— 是「最多 1.5s 全局限速等待 + notify 客户端自己的 30s HTTP 超时」，
// 也就是接近 31.5s（bot.Send 不收 ctx，进去之后只有那个 HTTP 超时管得住它）。
// 若预算从 Done 入口起算，光是排队等锁就足够把它耗光；轮到自己时 ctx 已经死了，
// sendRaw 首句的 ctx.Err() 直接返回，而 terminalSent 照样被置真 ——
// 消息永久冻结在中间态，正是 I3 存在的意义所在。所以计时必须从"轮到我了"算起。
//
// 10s 对**自己这一次**传输是足够宽裕的：真正能被 ctx 打断的只有最多 1.5s 的
// 限速等待，进了 bot.Send 之后由客户端的 30s HTTP 超时兜底。
const terminalSendTimeout = 10 * time.Second

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

// LyricState 是取歌词这一步的结局。
type LyricState int

const (
	LyricUnsupported LyricState = iota // 该音源不提供歌词（如 mp3pm）
	LyricMissing                       // 音源供词，但站点没收录这首
	LyricOK                            // 拿到并落盘了
	LyricFailed                        // 请求/解析/落盘出错
)

// String 让 LyricState 在日志里打出人话，而不是裸整数。
//
// 起因：runner.go 的 slog.InfoContext(..., "lyric", lyricSt.State) 若没有
// 这个方法，JSON 日志里就是 "lyric":0，排障时完全认不出对应哪个状态。
// slog 遇到实现了 fmt.Stringer 的值会自动调用它，不需要在调用点做任何改动。
func (s LyricState) String() string {
	switch s {
	case LyricUnsupported:
		return "不供词"
	case LyricMissing:
		return "未收录"
	case LyricOK:
		return "已获取"
	case LyricFailed:
		return "获取失败"
	default:
		// 兜底带上数字，方便对照上面的 iota 定义排查是不是漏改了这个方法。
		return fmt.Sprintf("未知(%d)", int(s))
	}
}

// LyricStatus 是取歌词这一步的快照。
//
// 为什么是四态而不是一个 bool：见 lyric.go 里 errLyricUnsupported 的注释 ——
// "音源不供词"和"站点没这首的词"在界面上必须是两句不同的话。
type LyricStatus struct {
	State LyricState
	Err   string // 仅 LyricFailed 时非空
}

// TargetStatus 是单个上传目标的快照。
type TargetStatus struct {
	Name  string      // 展示名，如 "阿里云盘"
	State TargetState //
	Err   string      // 失败原因，成功时为空
	// Warn 是**可选文件**（目前只有歌词）没传上去时的说明。
	// 它跟 Err 是两回事：Err 非空意味着这个目标失败了，Warn 非空时目标
	// 仍然是成功的 —— 不能因为一个附赠的 .lrc 没传上去，就把已经传好的
	// 歌判成失败（延续"不为一个网盘挂掉丢弃已下好的歌"那条既有约定）。
	Warn string
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

	// src / cand 是包内私有的取词凭据，由 agent 在下载成功那一刻填好
	// （见 agent.go 的 doDownload），此后再没人改过。
	//
	// 为什么把音源本身带在 Track 上，而不是让 Runner 另持一张
	// map[string]Source 注册表按 Source 名字去查：那样会多出一个
	// "查不到"的分支 —— 一个本不该发生、却必须写代码应付的状态。
	// 带着走，这个状态从根上就不存在。
	//
	// Candidate 里的 dlURL / dlCookie 本来就是包内可见、绝不出包、
	// 绝不进模型上下文的，挂在这里不改变那条约束。
	src  Source
	cand Candidate
}

// Status 是一次任务的**完整快照**。
//
// 为什么是覆盖式完整快照而不是增量事件？
// 因为每个快照都自洽，所以节流时丢弃中间态是安全的 —— 这是整个节流设计成立的前提。
// 增量事件一旦丢一条，后面的状态就全错了。
//
// ⚠️ "自洽"这条不是白来的，Track 是**指针**：Status 按值传给 Reporter，
// 拷进去的只是指针本身，快照和生产者仍然共享同一个 *Track。节流把快照存进
// pending 之后，flush 会在 time.AfterFunc 的另一个 goroutine 里读它 ——
// 此刻生产者若还在原地改那个 Track，就是货真价实的数据竞争（字符串是
// ptr+len 两个字，撕裂读能让 renderStatus 拿到越界的长度直接 panic，
// 而 /music 那个 goroutine 是 detached 的、没有 recover，等于把进程干掉）。
//
// 所以本包的硬约定是：**一个 *Track 一旦被塞进 Status 交出去，就永远不再改；
// 要改就造一个新的**（见 agent.go 的 finalize —— 它正是因此才不原地改 r.track）。
// 之所以不干脆改成值类型：nil 在这里是有语义的（"还没选定曲目"），换成值类型
// 就得再引入一个 HasTrack 之类的布尔量，把一个编译器帮你看着的状态
// 降级成一个需要人肉维持一致的状态，得不偿失。
type Status struct {
	Query    string         // 用户原始输入
	Stage    Stage          //
	Searches int            // 已搜索次数（含换词重搜）
	Found    int            // 最近一次搜索的候选数
	Track    *Track         // 选定并下载后才非 nil
	Targets  []TargetStatus // 上传目标状态，上传阶段才非空
	// Lyric 是取歌词那一步的结果。nil = 还没跑到这一步（用指针的理由同 Track：
	// nil 在这里是有语义的，换成值类型就得再引入一个布尔量去人肉维持一致）。
	Lyric *LyricStatus
	Err   string // 失败原因
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
	// terminalTimeout 是终态那一帧自己的发送预算（见 terminalSendTimeout）。
	// 抽成字段而非直接用常量，理由同 Uploader.timeout / Agent.maxBytes：
	// 测试要把它压到毫秒级，否则验证"预算不该在等锁期间烧掉"就得真等 10s。
	terminalTimeout time.Duration

	// mu 保护下面这组状态。time.AfterFunc 的回调在另一个 goroutine 里跑，
	// 上传阶段也有多个 goroutine 并发灌快照，所以必须加锁。
	mu       sync.Mutex
	msgID    int         // 0 表示还没发过首条消息
	lastSent time.Time   // 上次**真正发出**的时刻
	pending  *Status     // 被节流丢弃的最新快照
	timer    *time.Timer // 节流窗口结束时补发用；nil 表示当前没有待补发
	finished bool        // Done 之后为 true，之后的 Update 一律忽略（早退优化，见下）

	// pendingCtx 与 pending 成对存放，而不是让 time.AfterFunc 的闭包去捕获 ctx。
	//
	// 捕获闭包会把**第一个开窗的那次 Update** 的 ctx 一直用到补发为止。
	// Runner 里 agent 那层 context.WithTimeout 的 cancel() 是非 defer 的
	// （上传不该再受 agent 超时约束），所以那个 ctx 在进入上传阶段的瞬间就死了；
	// 于是补发的那一帧（通常恰恰是第一帧 "⬆️ 上传"）会被 notify.Telegram.sendRaw
	// 首句的 ctx.Err() 整条丢掉 —— 丢的是一帧真实进度，而且 lastSent 照样前进，
	// 下一帧还得再等一个完整的节流窗口。存最新的 ctx 就没这个问题。
	//
	// 往结构体里塞 ctx 通常是反模式，这里是有意为之：pending 本来就是"被推迟的
	// 那一次调用"，ctx 是那次调用的组成部分，理应跟着一起被推迟。
	pendingCtx context.Context

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
	return &tgReporter{
		editor:          e,
		chatID:          chatID,
		throttle:        throttle,
		terminalTimeout: terminalSendTimeout,
	}
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
		// 落在节流窗口内：只留最新快照（连同它的 ctx），并安排一次窗口结束后的补发。
		// timer 非 nil 说明补发已经排上了，不用再排一次 —— 它会取到最新的 pending。
		r.pending = &s
		r.pendingCtx = ctx
		if r.timer == nil {
			// 注意闭包里不捕获 ctx：补发时用的必须是最新那次 Update 的 ctx，
			// 理由见 pendingCtx 字段上的注释。
			r.timer = time.AfterFunc(wait, r.flush)
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
	r.pendingCtx = nil
	r.finished = true
	r.mu.Unlock()

	// 终态必须挣脱调用方的 ctx。
	//
	// Runner 的四条 Done 路径里，好几条传进来的 ctx **恰恰已经被取消了**：
	// 运维在下载中途重启服务（SIGTERM）→ ctx 取消 → Fetch 返回 context.Canceled
	// → Runner 置 StageFailed 并 Done；600s 总预算到点同理。而
	// notify.Telegram.sendRaw 的第一句就是 ctx.Err() 检查，于是这条终态
	// 根本不会被尝试发出 —— 偏偏 send() 已经把 terminalSent 置真，之后
	// 再没有任何一帧能纠正它，用户那条消息就永久冻结在
	// "⏳ 搜索 1 次 · 12 个候选" 这种中间态上，看不出任务已经失败了。
	//
	// 设计里唯一不能让步的一条就是 "Done 无条件立即发出……最后一条必须准确"，
	// 所以这里用 context.WithoutCancel 摘掉取消信号（**值照留**，logx 的
	// task_id 因此不会丢）。
	//
	// 这里**只**摘取消信号，不在这里套超时：那个超时必须等拿到 sendMu 之后
	// 才开始计时，否则排队等锁的时间会把预算烧光（详见 terminalSendTimeout
	// 的注释）。所以它被放在 send() 里。
	//
	// 为什么修在这里而不是 Runner 的四个调用点：这条保证是 Reporter 接口
	// 契约的一部分（"Done 必须立即发出"），属于实现方的责任。放在调用点
	// 等于把同一段防御抄四遍，将来多一条 Done 路径就会漏掉一次；而且别的
	// Reporter 实现也拿不到这份保证。
	//
	// 注意 WithoutCancel 之后没有 deadline 也不会让 Done 永久卡住：它顶多在
	// sendMu 上排队，而每个持锁者的传输都被 notify 客户端的 30s HTTP 超时封顶。
	dctx := context.WithoutCancel(ctx)

	// finished 只挡未来的 Update（见 Update 顶部的早退检查），挡不住已经
	// 在飞的并发调用（它们的早退检查在 finished=true 之前就已经通过了）。
	// 真正的“终态不会被覆盖”保证在 send() 里的 sendMu + terminalSent。
	r.send(dctx, s, true)
}

// flush 是节流窗口结束时的补发：把最新的 pending 快照发出去。
//
// 不收 ctx 参数：要用的 ctx 存在 pendingCtx 里，跟 pending 一起取，
// 这样补发用的永远是**最新**那次 Update 的 ctx（理由见 pendingCtx 的注释）。
func (r *tgReporter) flush() {
	r.mu.Lock()
	r.timer = nil
	if r.finished || r.pending == nil {
		r.mu.Unlock()
		return
	}
	s := *r.pending // 解引用拷一份，锁外用
	ctx := r.pendingCtx
	r.pending = nil
	r.pendingCtx = nil
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

	// 终态的发送预算从**这里**开始计时，而不是从 Done 入口 —— 上面那把
	// sendMu 可能刚被另一次传输占了几十秒（见 terminalSendTimeout 的注释），
	// 预算若在排队期间就烧完，终态会在真正轮到它时被 sendRaw 首句直接丢弃，
	// 而 terminalSent 还是会被置真，消息永久停在中间态。
	// 中间态的 send 不套预算：它本来就是可丢弃的，由调用方的 ctx 管着即可。
	if terminal {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.terminalTimeout)
		defer cancel()
	}

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

	// 搜索行：搜过才显示。搜索这一步走完了就打勾，否则显示进行中。
	if s.Searches > 0 {
		b.WriteString(stageIcon(searchDone(s)) + " " +
			esc(fmt.Sprintf("搜索 %d 次 · %d 个候选", s.Searches, s.Found)) + "\n")
	}

	// 曲目行：选定后就有（此时 Bytes 可能还是 0，下载完才有值）。
	if s.Track != nil {
		line := fmt.Sprintf("%s - %s", s.Track.Artist, s.Track.Title)
		// 时长是"有才拼"：musicso 这类音源不提供时长，硬拼会在行尾留下一个
		// 没有下文的 " · "，看起来像渲染坏了。下面 Bytes / Source 两段
		// 本来就是这个写法，这里只是把漏掉的一处补齐。
		if s.Track.Duration != "" {
			line += fmt.Sprintf(" · %s", s.Track.Duration)
		}
		if s.Track.Bytes > 0 {
			line += fmt.Sprintf(" · %s", humanSize(s.Track.Bytes))
		}
		if s.Track.Source != "" {
			line += " · " + s.Track.Source
		}
		b.WriteString(stageIcon(s.Track.Bytes > 0) + " " + esc(line) + "\n")
	}

	// 歌词行：跑到取词这一步才有。nil 表示还没轮到它，整行不出现 ——
	// 提前显示一个"无歌词"会误导用户以为已经查过了。
	if s.Lyric != nil {
		b.WriteString(renderLyricLine(*s.Lyric, s.Track) + "\n")
	}

	// 上传区：每个目标一行。
	if len(s.Targets) > 0 {
		b.WriteString("⬆️ 上传\n")
		for _, t := range s.Targets {
			line := "   " + targetIcon(t.State) + " " + esc(t.Name)
			if t.Err != "" {
				line += esc(": " + tools.TruncateUTF8(t.Err, 80))
			}
			// Warn 与 Err 互不排斥：目标成功但歌词没传上去时只有 Warn。
			if t.Warn != "" {
				line += esc(" ⚠️ 歌词未传上: " + tools.TruncateUTF8(t.Warn, 80))
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

// renderLyricLine 渲染歌词那一行。
//
// 抽成独立函数是因为四个分支各有各的措辞，塞进 renderStatus 会把那个
// 已经不短的函数再撑大一截、盖住主线。
//
// 转义约定同 renderStatus：动态文本必须自己 EscapeMarkdownV2，
// 只有我们主动写的标记才留着不转义。这里的 emoji 和固定汉字不需要转义，
// 但源名和错误原因是动态的，必须过一遍。
func renderLyricLine(l LyricStatus, t *Track) string {
	esc := tools.EscapeMarkdownV2

	switch l.State {
	case LyricOK:
		return "✅ 歌词"
	case LyricUnsupported:
		// 源名取自 Track。理论上走到这一步 Track 一定非空（取词发生在下载之后），
		// 兜个底只是不想让一个渲染函数有 panic 的可能。
		src := "该音源"
		if t != nil && t.Source != "" {
			src = t.Source
		}
		return "➖ " + esc("歌词："+src+" 不提供")
	case LyricMissing:
		return "➖ " + esc("歌词：站点未收录")
	case LyricFailed:
		return "⚠️ " + esc("歌词获取失败: "+tools.TruncateUTF8(l.Err, 80))
	default:
		// 显式列出四个已知状态，default 只兜将来新增的状态（比如某天多一个
		// LyricXxx）——写成 `default: // LyricFailed` 会让新状态被静默地
		// 渲染成"歌词获取失败"，界面上看不出任何异常，只有本该看到的新文案
		// 消失了。这里用带数字的兜底文案，日志/截图排障时至少能定位到
		// 是哪个未知枚举值，而不是伪装成一个已知状态。
		// 不 panic：这个函数跑在 /music 那个 detached goroutine 上，没有 recover。
		return "❓ " + esc(fmt.Sprintf("歌词：未知状态 %d", l.State))
	}
}

// searchDone 判断"搜索这一步是否已经走完"，决定搜索行打勾还是显示进行中。
//
// 不能图省事写成 s.Stage > StageSearching：StageFailed 的枚举值是 4，
// 比 StageSearching 大，于是一个**在搜索阶段就失败**的任务会渲染成
// "✅ 搜索 1 次 · 0 个候选" 紧跟一个 "❌ ..."，自己打自己的脸。
// 失败态必须单独判断：只有已经选定并下载了曲目（Track != nil）才说明
// 搜索确实走完了 —— 那种"搜索成功、上传全挂"的失败仍然应该打勾。
func searchDone(s Status) bool {
	if s.Stage == StageFailed {
		return s.Track != nil
	}
	return s.Stage > StageSearching
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
