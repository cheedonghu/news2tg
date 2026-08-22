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
//   - Notify          发单条消息到默认目标（配置的 chat/频道）
//   - NotifyTo        发单条消息到指定 chatID（如回复发指令的用户）
//   - NotifyMarkdown  发单条**已渲染好的 MarkdownV2** 到默认目标
//
// 转义约定（重要）：
//   - Notify / NotifyTo 把 content 当**纯文本**，由实现内部负责 MarkdownV2 转义，
//     调用方不要再自己 EscapeMarkdownV2。
//   - NotifyMarkdown 发的是**已经拼好的 MarkdownV2**（如 monitor 里带 *加粗*/[链接] 的消息），
//     实现不做整体转义；这类消息在构建时自行转义动态片段。
//
// 限速：由实现内部保证（Telegram 实现里任意两次发送间隔 ≥1.5s），
// 调用方直接循环调用即可，不需要自己 sleep。
//
// 所有方法都接收 ctx：调用方取消时能立刻停下来。返回 error 让调用方决定继续还是退出。
type Notifier interface {
	Notify(ctx context.Context, content string) error
	NotifyTo(ctx context.Context, chatID int64, content string) error
	NotifyMarkdown(ctx context.Context, content string) error
}

// Editor 是"可原地编辑的消息通道"。
//
// 为什么不并进 Notifier？
// Notifier 的语义是"发出去就不管了"，monitor 那些只推不改的调用方压根不需要 msgID；
// 把编辑方法塞进去会逼它们全都认识这个概念。分成两个接口后，
// 只有真正要改消息的调用方（music 的进度上报）才依赖 Editor。
//
// 转义约定：与 NotifyMarkdown 一致 —— md 是**已渲染好的 MarkdownV2**，
// 实现不做整体转义，调用方自己转义动态片段。
//
// 限速：由实现内部保证（与 Notifier 共用同一把全局节流锁）。
type Editor interface {
	// SendEditable 发一条新消息，返回它的 message id，供后续 Edit 使用。
	SendEditable(ctx context.Context, chatID int64, md string) (msgID int, err error)
	// Edit 用新内容覆盖已有消息。
	Edit(ctx context.Context, chatID int64, msgID int, md string) error
}
