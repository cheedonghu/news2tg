package music

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cheedonghu/news2tg/internal/notify"
)

// agentTimeout 是 agent 循环（搜索 + 下载）的超时。
//
// 这个数字是按 agent.go 的 defaultMaxSteps 最坏路径估出来的：双源场景下
// 搜→换词重搜→换源搜→换词重搜→下载→下载失败改选→再下载→输出最终 JSON
// 共 8 轮，每轮是一次带 tools、上下文逐轮变长的 DeepSeek 调用，忙时
// 15-25s 很常见，8 轮就是 120-200s，再加下载本身的传输时间，
// 300s 才有足够余量；180s 曾经卡在这条最坏路径上，把"未收敛"变成了
// "超时失败"，白烧前面几轮的 token。
// ⚠️ 改 agent.go 的 defaultMaxSteps 时必须回头检查这个值是否还够用——
// 两个常量是耦合的，只调一个就会重演上面这个问题。
// 外层 command.Bot 的 musicTimeout 是 600s（bot.go），其中还包含 Uploader
// 按目标各自计的上传超时（300s/目标），300s 在这个总预算里仍有余量。
const agentTimeout = 300 * time.Second

// lyricTimeout 是取歌词那一步的超时。
//
// 它就是一次 JSON 请求（musicso 的 play.php），几百毫秒的事，不该有资格
// 去啃外层 command.Bot 那 600s 预算里 agent 和上传要用的部分。
// 同 agentTimeout / targetUploadTimeout 的套路：每一步各管各的时限，
// 一步卡住不拖垮后面的步骤。
const lyricTimeout = 30 * time.Second

// fetcher / uploader 是**消费侧接口**（"accept interfaces"）：
// Runner 只依赖这两件能力，不直接绑死 *Agent / *Uploader 的具体类型。
// 好处是编排逻辑能脱离 LLM 和网络单测 —— 塞两个 fake 就够了。
type fetcher interface {
	Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error)
}

type uploader interface {
	Upload(ctx context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error)
}

// lyricSaver 同 fetcher / uploader，也是消费侧接口：Runner 只依赖
// "取词并落盘"这一件能力，测试塞个 fake 就能脱离网络验编排。
//
// 注意它**不返回 error** —— 歌词是附赠品，编排层在类型层面就没有
// "把它当致命错误"的选项（见 lyric.go 对 Save 的说明）。
type lyricSaver interface {
	Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)
}

// Runner 把 agent、取词、上传、进度上报串成一次完整的 /music 任务。
type Runner struct {
	fetcher  fetcher
	uploader uploader
	lyrics   lyricSaver
	editor   notify.Editor // 用来给每次任务新建一个 Reporter
}

// NewRunner 构造函数。收具体类型、存接口，是仓库里一贯的注入风格。
//
// lyrics 不出现在参数里、而是在这里装配默认实现：Lyrics 是无状态的，
// 没有任何依赖需要从外面注入，让 main 去 new 一个空结构体传进来纯属噪音。
// 测试要替换它时在同包内直接改这个私有字段即可 —— 同 Uploader.timeout /
// Agent.maxBytes / tgReporter.terminalTimeout 那套"抽成字段以便测试"的做法。
// 这也是本次改动不用碰 cmd/news2tg/main.go 的原因。
func NewRunner(a *Agent, u *Uploader, e notify.Editor) *Runner {
	return &Runner{fetcher: a, uploader: u, lyrics: Lyrics{}, editor: e}
}

// Run 执行一次完整任务：建临时目录 → agent 搜+下 → 并发上传 → 收尾。
//
// 进度与最终结果全部通过原地编辑**同一条**消息回报，所以这里除了返回 error
// 给调用方记日志之外，不再往 Telegram 发任何额外消息。
func (r *Runner) Run(ctx context.Context, chatID int64, query string) error {
	// 每次任务一个 Reporter：它内部持有那条消息的 msgID 和节流状态，
	// 多个并发 /music 各用各的，互不干扰。
	rep := NewTelegramReporter(r.editor, chatID)

	// 这份 Status 是全程唯一的进度真相：agent 改搜索/下载部分，
	// 下面的上传回调改 Targets，Reporter 每次拿到的都是完整快照。
	st := &Status{Query: query, Stage: StageSearching}
	rep.Update(ctx, *st)

	// os.MkdirTemp 在系统临时目录下建一个带随机后缀的目录，"*" 是随机部分的占位。
	dir, err := os.MkdirTemp("", "news2tg-music-*")
	if err != nil {
		st.Stage, st.Err = StageFailed, "创建临时目录失败: "+err.Error()
		rep.Done(ctx, *st)
		return err
	}
	// 成败都删：容器里攒 mp3 只会撑爆磁盘，歌已经在网盘里了。
	// defer 在函数返回时执行，所以下面每条 return 路径都会走到它。
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			slog.ErrorContext(ctx, "清理音乐临时目录失败", "dir", dir, "err", rmErr)
		}
	}()

	// agent 单独套一层超时：它慢在模型多轮调用上，和上传的慢是两回事。
	actx, cancel := context.WithTimeout(ctx, agentTimeout)
	track, err := r.fetcher.Fetch(actx, st, dir, rep)
	cancel() // 不用 defer：后面的上传不该再受 agent 那层超时约束
	if err != nil {
		st.Stage, st.Err = StageFailed, err.Error()
		rep.Done(ctx, *st)
		return err
	}

	// 文件名用模型规范化后的歌手/歌名，清洗掉危险字符。
	// .lrc 与 .mp3 共用同一个主体（见 buildBase），播放器才配得上对。
	filename := BuildFilename(track.Artist, track.Title)
	lrcName := BuildLyricFilename(track.Artist, track.Title)

	// 先把选定的曲目显示出来，此刻还没进入上传阶段。
	st.Track = track
	rep.Update(ctx, *st)

	// 取歌词。单独套一层短超时（它只是一次 JSON 请求），且**永不阻断** ——
	// Save 的签名里根本没有 error，拿不到词照样把歌传上去。
	lctx, lcancel := context.WithTimeout(ctx, lyricTimeout)
	lrcPath, lyricSt := r.lyrics.Save(lctx, track, dir, lrcName)
	lcancel() // 不用 defer：后面的上传不该再受取词那层超时约束
	st.Lyric = &lyricSt

	st.Stage = StageUploading
	rep.Update(ctx, *st)

	slog.InfoContext(ctx, "开始上传音乐到 WebDAV",
		"file", filename, "bytes", track.Bytes, "lyric", lyricSt.State)

	// stMu 只保护上传阶段：Uploader 内部给每个目标起一个 goroutine，
	// onProgress（也就是这里的回调）在 wg.Wait() 返回之前会被这些 goroutine
	// 并发调用 —— 两个目标前后脚完成时，回调会在不同 goroutine 里同时跑，
	// 此时并发读写同一个 st.Targets / *st 是未同步的数据竞争。
	// agent 阶段（上面）和 Upload 返回之后（下面的 st.Stage/st.Err）都在
	// wg.Wait() 建立的 happens-before 之外/之后发生，不会跟这把锁保护的
	// 并发窗口重叠，所以不需要锁 —— 别把锁的范围"顺手"扩大到整个函数。
	// 锁只护住共享字段的读写，不覆盖 rep.Update 的网络 IO：Update 里还有
	// Telegram 的节流/发送逻辑，锁着它会让并发的另一次回调白等一次 IO。
	var stMu sync.Mutex
	// mp3 排在第一个：Uploader 按顺序传，ctx 中途断掉时先落地的是要紧那个。
	files := []UploadFile{{
		LocalPath:   track.LocalPath,
		Filename:    filename,
		ContentType: "audio/mpeg",
	}}
	if lrcPath != "" {
		// Optional：歌词传不上去不该把这个目标判成失败
		// （见 upload.go 里 UploadFile.Optional 的说明）。
		files = append(files, UploadFile{
			LocalPath:   lrcPath,
			Filename:    lrcName,
			ContentType: "text/plain; charset=utf-8",
			Optional:    true,
		})
	}

	targets, upErr := r.uploader.Upload(ctx, files, func(ts []TargetStatus) {
		// 每个目标状态一变就刷进度。Reporter 内部会节流，这里放心调。
		stMu.Lock()
		st.Targets = ts
		snapshot := *st // 锁内拷贝完整快照，锁外再发，IO 不占锁
		stMu.Unlock()
		rep.Update(ctx, snapshot)
	})
	st.Targets = targets

	if upErr != nil {
		st.Stage, st.Err = StageFailed, upErr.Error()
		rep.Done(ctx, *st)
		return upErr
	}

	st.Stage = StageDone
	rep.Done(ctx, *st)
	slog.InfoContext(ctx, "音乐任务完成", "file", filename)
	return nil
}
