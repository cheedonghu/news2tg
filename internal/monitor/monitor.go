// Package monitor 定义抓取任务的统一抽象。
// ↑ Go 规范：包注释写在 package 行的正上方，第一句必须以 "Package <名字>" 开头，
//
//	`go doc` 会把这段当作包的官方说明展示。
package monitor

// import 块：括号包起来可以一次导入多个包。
// 这是分组写法，比写多行 `import "xxx"` 更常见。
import (
	"context"  // 标准库：上下文，用来传 cancel 信号、超时、请求级数据
	"log/slog" // 标准库：结构化日志
	"time"     // 记录推送成功的时刻

	// 第三方包：导入路径就是 go.mod 里 module 名 + 子目录路径。
	// 空行用来把"标准库"和"本项目/第三方"分组，gofmt 不会动这种分组。
	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/store"
)

// Monitor 是一个"接口"（interface）。Go 的接口是隐式实现的：
// 任何类型只要有名为 Run、签名一致的方法，就自动"实现"了 Monitor，不需要写 implements。
//
// 这里只声明一个方法 Run：
//   - 入参 ctx context.Context  约定俗成的第一个参数名 ctx；
//   - 入参 cfg *config.Config   指针类型（前面有 *），意味着传"地址"而不是"拷贝整个结构体"；
//   - 返回 error                Go 的错误是普通值，nil 表示成功。
type Monitor interface {
	Run(ctx context.Context, cfg *config.Config) error
}

// 实现者约定（写在注释里，让别的开发者知道怎么实现）：
//   - Run 是阻塞的：调用后会一直执行，直到 ctx 被取消或不可恢复错误才返回；
//   - 返回 ctx.Err() 表示"正常退出"（被外部 cancel）；
//   - 其它错误视作异常；
//   - 实现需要自己处理"临时"错误（log 后继续），不要把可重试错误返回出来。

// deliver 逐条推送并在**发送成功之后**记账。V2EX 和 HackerNews 共用。
//
// 为什么先发后记：去重是永久的，如果像改造前那样在抓取阶段就记账，
// 一旦 Telegram 发送失败，这条帖子就再也不会被推送了 —— 永久丢失。
// 先发后记把语义从"至多一次"变成"至少一次"：发成功但写库失败（或两者之间崩溃）时，
// 下一轮会重推一次。用一次可能的重复换掉永久丢失，是划算的。
//
// 不返回 error：单条失败属于瞬时问题，就地 log 消化即可，
// 符合 Monitor 接口"瞬时错误内部处理，不要往 Run 的返回值上冒"的契约。
func deliver(ctx context.Context, n notify.Notifier, st store.Store, chatID string, items []model.NotifyBase) {
	for _, item := range items {
		// 发送失败：不写库，下一轮 fetch 时 AlreadyPushed 仍返回 false，自然重试。
		if err := n.NotifyMarkdown(ctx, item.Content); err != nil {
			// ctx 被取消 = 正常关闭，不是推送故障：直接结束本轮，
			// 避免给剩余的每一条都刷一行 ERROR 日志。
			if ctx.Err() != nil {
				return
			}
			slog.ErrorContext(ctx, "推送失败，本条不记账",
				"source", item.Source, "external_id", item.ExternalID, "err", err)
			continue
		}
		rec := store.Record{
			Source:     item.Source,
			ExternalID: item.ExternalID,
			Title:      item.PostTitle, // 用帖子真实标题，不是消息抬头
			URL:        item.URL,
			ChatID:     chatID,
			PushedAt:   time.Now(),
		}
		// 写库失败：消息已经发出去了，只能告警。下一轮会重推一次。
		if err := st.MarkPushed(ctx, rec); err != nil {
			slog.ErrorContext(ctx, "推送记录写入失败，下轮会重推",
				"source", item.Source, "external_id", item.ExternalID, "err", err)
		}
	}
}
