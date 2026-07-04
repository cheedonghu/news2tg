// Package notify 定义"通知器"的统一接口，并在子文件实现具体推送渠道（telegram.go）。
//
// 抽接口的好处：
//  1. 业务代码（monitor 包）只依赖接口，不依赖 telegram 实现，将来加邮件/钉钉只需新加一个实现；
//  2. 测试时可以传 mock Notifier，不真发消息。
package notify

import "context"

// Notifier 是所有通知渠道要实现的接口。
// 任何类型只要有这些方法，就自动是一个 Notifier，不用写 implements。
//
// 方法语义：
//   - Notify       发单条消息到默认目标（配置的 chat/频道）
//   - NotifyTo     发单条消息到指定 chatID（如回复发指令的用户）
//   - NotifyBatch  发一批消息（实现可以做限速/合并/分组）
//
// 转义约定（重要）：
//   - Notify / NotifyTo 把 content 当**纯文本**，由实现内部负责 MarkdownV2 转义，
//     调用方不要再自己 EscapeMarkdownV2。
//   - NotifyBatch 发的是**已经拼好的 MarkdownV2** 文本（如 monitor 里带 *加粗*/[链接] 的消息），
//     实现不做整体转义；这类消息在构建时自行转义动态片段。
//
// 所有方法都接收 ctx：调用方取消时能立刻停下来。返回 error 让调用方决定继续还是退出。
type Notifier interface {
	Notify(ctx context.Context, content string) error
	NotifyTo(ctx context.Context, chatID int64, content string) error
	NotifyBatch(ctx context.Context, contents []string) error
}
