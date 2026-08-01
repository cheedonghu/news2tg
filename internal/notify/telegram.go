package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	// 重命名 import：原包名是 tgbotapi，这里显式写出来强调（其实它本身就叫这个名）。
	// 用法：`import 别名 "导入路径"`，可以解决包名冲突或缩短调用。
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/cheedonghu/news2tg/internal/tools"
)

// Telegram 是 Notifier 接口的一个实现。
// 字段全小写 = 包外不可见，外部只能用 NewTelegram 构造、用接口方法操作。
type Telegram struct {
	bot    *tgbotapi.BotAPI // SDK 的 bot 客户端实例
	chatID int64            // 目标聊天/频道 ID（Telegram 用 int64，可能是负数）
}

// NewTelegram 构造函数。第一次调用时 SDK 会发请求验 token，所以可能返回 error。
// 返回 (*Telegram, error)：成功 = 指针 + nil，失败 = nil + error。
func NewTelegram(token string, chatID int64) (*Telegram, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		// fmt.Errorf + %w：包装原错误，保留底层信息便于 errors.Is/As 判断。
		return nil, fmt.Errorf("init telegram bot: %w", err)
	}
	// 字面量构造结构体并取地址，一步到位返回。
	return &Telegram{bot: bot, chatID: chatID}, nil
}

// Notify 发一条消息到默认目标（构造时配置的 chatID）。
// 这个方法签名匹配 Notifier.Notify，所以 *Telegram 隐式实现了 Notifier 接口。
func (t *Telegram) Notify(ctx context.Context, content string) error {
	return t.NotifyTo(ctx, t.chatID, content)
}

// NotifyTo 发一条消息到指定 chatID（如回复发指令的用户）。
// content 当**纯文本**：MarkdownV2 转义在这里统一做，调用方不用再 EscapeMarkdownV2。
func (t *Telegram) NotifyTo(ctx context.Context, chatID int64, content string) error {
	// NewMessage 构造一个 MessageConfig 值（不是指针）。转义后再发。
	msg := tgbotapi.NewMessage(chatID, tools.EscapeMarkdownV2(content))
	msg.ParseMode = tgbotapi.ModeMarkdownV2 // 用 MarkdownV2 解析（所以上面统一转义）
	msg.DisableWebPagePreview = false       // 允许链接预览

	// bot.Send 返回 (Message, error)；这里不关心成功的 Message，用 _ 丢弃。
	if _, err := t.bot.Send(msg); err != nil {
		slog.ErrorContext(ctx, "telegram单笔信息推送失败", "err", err)
		return fmt.Errorf("telegram单笔信息推送失败: %w", err)
	}
	return nil
}

// NotifyBatch 发一批消息。
// 实现策略：串行发，每条之间休息 1.5s，避免触发 Telegram 频率限制。
// 注意：单条失败只记日志、不中断；ctx 取消才返回。
func (t *Telegram) NotifyBatch(ctx context.Context, contents []string) error {
	for _, content := range contents {
		//fmt.Println(content) // 顺便也打印到 stdout，调试用
		slog.InfoContext(ctx, content)
		msg := tgbotapi.NewMessage(t.chatID, content)
		msg.ParseMode = tgbotapi.ModeMarkdownV2
		msg.DisableWebPagePreview = false
		if _, err := t.bot.Send(msg); err != nil {
			slog.ErrorContext(ctx, "telegram批量信息推送失败", "err", err)
			// 注意：这里没 return，继续发下一条
		}

		// 限速 sleep，但要支持被 ctx 取消。
		// 直接 time.Sleep(1500ms) 也行，但 cancel 时还得睡完才能停 —— 不优雅。
		// 用 select 同时等"ctx 取消"和"1.5s 到点"，谁先到走谁。
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1500 * time.Millisecond):
			// time.After(d) 返回一个 d 后会被写入的 channel；用完即丢弃，比 NewTimer 简单
			// （注意：ctx 取消时 time.After 创建的 timer 还会跑完，有微小内存浪费 —— 高频场景才需要换 NewTimer）。
		}
	}
	return nil
}
