// Package music 实现 /music 指令背后的整条链路：
// LLM 理解歌曲描述 → 从音源搜索并挑选 → 下载到本地 → 并发上传到多个 WebDAV 目标，
// 全程通过 Reporter 把进度回报出去。
//
// 包内分工：
//
//	source.go  音源的统一形状（扩展点），无任何站点逻辑
//	mp3pm.go   Source 的 mp3.pm 实现，唯一含站点特有逻辑的文件
//	agent.go   LLM 工具调用循环
//	upload.go  WebDAV 并发上传
//	report.go  进度快照与上报
//	runner.go  把上面几件事串起来的编排层
package music

import (
	"context"
	"io"
)

// Candidate 是一条搜索结果。
//
// dlURL 小写 = 包外不可见。这是刻意的：那个直链带一长串 token（200+ 字符），
// 既不该出包，更不该进模型上下文 —— 20 条候选就是 4000+ 字符白烧 token，
// 而且模型很容易把它改坏。模型只看得到 ID，Go 侧凭 ID 查表拿真直链。
type Candidate struct {
	ID       string // 源内唯一 id（mp3.pm 用 data-sound-id）
	Artist   string // 站点原始歌手名，可能是罗马化的（"Jay Chou"）
	Title    string // 站点原始歌名，可能是罗马化的（"Kai Bu Liao Kou."）
	Duration string // "04:44"
	dlURL    string // 下载直链
}

// Source 是"一个音源"的统一形状，也是本包唯一的扩展点。
//
// 实现独立、形状统一：每个音源自己一个文件、自己一套解析逻辑，
// 但都长成这个样子，于是 agent 能为它自动生成 search_<名> / download_<名>
// 两个工具，加新音源时 agent 循环一行都不用改。
//
// Download 收 io.Writer 而不是文件路径：让"写到哪里"归调用方决定，
// 音源只负责把字节流吐出来。大小上限由调用方在这个 Writer 上强制（见 agent.go
// 的 limitWriter），不下放给各音源 —— 否则每加一个源都要重复实现一遍同样的防御。
type Source interface {
	// Name 返回源标识，用于拼工具名，必须是合法的函数名片段（小写字母/数字/下划线）。
	Name() string
	// Hint 是给模型看的站点特性说明书，会拼进工具 description。
	// "某个站怎么搜才有效"这条知识跟着站点实现走，不散落进全局 system prompt。
	Hint() string
	// Search 按关键词搜索，返回候选列表。
	// 零结果返回空切片 + nil error —— "没搜到"是正常路径，不是错误。
	Search(ctx context.Context, query string) ([]Candidate, error)
	// Download 把候选的音频字节写进 w，返回实际写入字节数。
	Download(ctx context.Context, c Candidate, w io.Writer) (int64, error)
}
