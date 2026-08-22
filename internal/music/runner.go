package music

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cheedonghu/news2tg/internal/notify"
)

// agentTimeout 是 agent 循环（搜索 + 下载）的超时，与 /summary 的 180s 对齐。
// 模型最多 6 轮调用 + 一次下载，正常在 30s 内跑完，180s 是宽松兜底。
// 上传的超时在 Uploader 内部按目标各自计（300s），总预算 600s 由 command.Bot 套。
const agentTimeout = 180 * time.Second

// fetcher / uploader 是**消费侧接口**（"accept interfaces"）：
// Runner 只依赖这两件能力，不直接绑死 *Agent / *Uploader 的具体类型。
// 好处是编排逻辑能脱离 LLM 和网络单测 —— 塞两个 fake 就够了。
type fetcher interface {
	Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error)
}

type uploader interface {
	Upload(ctx context.Context, localPath, filename string, onProgress func([]TargetStatus)) ([]TargetStatus, error)
}

// Runner 把 agent、上传、进度上报串成一次完整的 /music 任务。
type Runner struct {
	fetcher  fetcher
	uploader uploader
	editor   notify.Editor // 用来给每次任务新建一个 Reporter
}

// NewRunner 构造函数。收具体类型、存接口，是仓库里一贯的注入风格。
func NewRunner(a *Agent, u *Uploader, e notify.Editor) *Runner {
	return &Runner{fetcher: a, uploader: u, editor: e}
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
	filename := BuildFilename(track.Artist, track.Title)
	st.Track, st.Stage = track, StageUploading
	rep.Update(ctx, *st)

	slog.InfoContext(ctx, "开始上传音乐到 WebDAV", "file", filename, "bytes", track.Bytes)

	targets, upErr := r.uploader.Upload(ctx, track.LocalPath, filename, func(ts []TargetStatus) {
		// 每个目标状态一变就刷进度。Reporter 内部会节流，这里放心调。
		st.Targets = ts
		rep.Update(ctx, *st)
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
