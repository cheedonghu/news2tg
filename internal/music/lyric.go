package music

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// errLyricUnsupported 是"该音源不提供歌词"的哨兵错误，供 errors.Is 判别。
//
// 为什么"不供词"要用一个专门的错误、而不是跟"站点没收录这首"一样返回
// ("", nil)：这两件事在进度消息里是不同的两句话 ——「mp3pm 不提供」
// 与「站点未收录」。合并成一句含糊的"无歌词"，代价是日后 musicso 改版
// 导致歌词永远解析不出来时，界面上跟"这首歌真的没词"长得一模一样，
// 没有人会发现有东西坏了。
//
// 风格与 agent.go 的 errTooLarge / errSourceUnavailable 一致：
// 音源可以用 %w 包任意层数，调用方一律用 errors.Is 判别。
var errLyricUnsupported = errors.New("该音源不提供歌词")

// Lyrics 是"取词并落盘"这一步。
//
// 它是**无状态**的：取词凭据全挂在 Track 上（src / cand），落盘位置由调用方给。
// 既然无状态，为什么还抽成一个类型而不是包级函数？为了 Runner 能通过一个
// 小接口注入 fake 来单测编排逻辑 —— 同 fetcher / uploader 那两个消费侧接口
// 的做法。包级函数是没法替换的。
type Lyrics struct{}

// Save 取词并落盘，返回 .lrc 的本地路径（没有歌词时是空串）与这一步的状态。
//
// filename 由调用方用 BuildLyricFilename 拼好，Save 不参与命名 ——
// .lrc 与 .mp3 必须逐字同名（含截断结果）播放器才配得上对，让两个名字
// 出自同一处（upload.go 的 buildBase）是这条约束唯一的保障点。
//
// **永不返回 error**，这是刻意的：歌词是附赠品，一首已经下好的歌不该因为
// 没配上词就传不上去。签名里干脆不给 error，调用方在类型层面就没有
// "把它当致命错误"的选项 —— 比靠注释约束可靠，同 ai.DeepSeek.Summarize
// 返回兜底字符串加 nil 的手法。失败信息通过 LyricStatus.Err 呈现。
func (Lyrics) Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus) {
	// 防御性的：正常路径下 agent 一定填了 src（见 agent.go 的 doDownload）。
	// 这里兜底只是不想让一条取词路径有 panic 掉整个进程的可能 ——
	// /music 那个 goroutine 是 detached 的、没有 recover。
	if t == nil || t.src == nil {
		return "", LyricStatus{State: LyricUnsupported}
	}

	lrc, err := t.src.Lyric(ctx, t.cand)
	if err != nil {
		// errors.Is 沿着 %w 包装链找哨兵，所以音源里包了几层都认得出来。
		if errors.Is(err, errLyricUnsupported) {
			return "", LyricStatus{State: LyricUnsupported}
		}
		slog.WarnContext(ctx, "取歌词失败", "source", t.Source, "id", t.cand.ID, "err", err)
		return "", LyricStatus{State: LyricFailed, Err: err.Error()}
	}

	// 空（或只有空白）= 站点没收录这首的词。不落盘：一个内容空白的 .lrc
	// 传上网盘只会让播放器显示一片空白，比没有歌词更糟。
	if strings.TrimSpace(lrc) == "" {
		return "", LyricStatus{State: LyricMissing}
	}

	path := filepath.Join(dir, filename)
	// 0o600 与 agent 下载临时文件的权限一致：临时目录里的东西不必给别人看。
	if wErr := os.WriteFile(path, []byte(lrc), 0o600); wErr != nil {
		// 删掉可能的半成品（WriteFile 可能已经建好文件才写失败）：
		// 残缺的 .lrc 传上网盘比没有更糟，同 doDownload 删下载半成品的理由。
		// os.IsNotExist 过滤掉"本来就没建成"这种正常情况，免得刷无用的告警。
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.WarnContext(ctx, "删除歌词半成品失败", "path", path, "err", rmErr)
		}
		slog.ErrorContext(ctx, "写歌词文件失败", "path", path, "err", wErr)
		return "", LyricStatus{State: LyricFailed, Err: wErr.Error()}
	}

	slog.InfoContext(ctx, "歌词已保存", "path", path, "bytes", len(lrc))
	return path, LyricStatus{State: LyricOK}
}
