package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	// 重命名 import：原包名是 tgbotapi，这里显式写出来强调（其实它本身就叫这个名）。
	// 用法：`import 别名 "导入路径"`，可以解决包名冲突或缩短调用。
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/cheedonghu/news2tg/internal/tools"
)

// sendInterval 是任意两次发送之间的最小间隔，用来躲开 Telegram 的频率限制。
const sendInterval = 1500 * time.Millisecond

// Telegram 是 Notifier 接口的一个实现。
// 字段全小写 = 包外不可见，外部只能用 NewTelegram 构造、用接口方法操作。
type Telegram struct {
	bot    *tgbotapi.BotAPI // SDK 的 bot 客户端实例
	chatID int64            // 目标聊天/频道 ID（Telegram 用 int64，可能是负数）

	// 限速状态。以前这个 sleep 埋在 NotifyBatch 里，只保护批量路径；
	// 下沉到客户端后，weather、/summary 回复等所有发送路径共享同一个节流阀。
	mu       sync.Mutex // 保护 lastSend，同时让发送整体串行化
	lastSend time.Time  // 上次发送完成的时刻；零值表示"从没发过"
}

// NewTelegram 构造函数。第一次调用时 SDK 会发请求验 token，所以可能返回 error。
// 返回 (*Telegram, error)：成功 = 指针 + nil，失败 = nil + error。
func NewTelegram(token string, chatID int64) (*Telegram, error) {
	// 这里必须显式传一个带超时的 *http.Client，不能用 tgbotapi.NewBotAPI 默认的 &http.Client{}（零 Timeout）。
	// 原因：send 方法把 t.mu 一路持有到 t.bot.Send(msg) 这次网络调用返回为止（见下方 send 的注释），
	// 而 bot.Send 又不接受 ctx、无法从外部取消。如果 HTTP 客户端没有超时，一次卡住的 TCP 连接
	// 就会让这把锁永远拿不到，冻结进程里所有发送路径（weather、v2ex、hackernews、/summary 回复……）。
	// 30s 是一个足够宽松、不会误伤正常请求，又能兜底网络异常的超时值。
	bot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, &http.Client{Timeout: 30 * time.Second})
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

// send 是所有发送路径的唯一出口：先取得限速许可，再真正发。
//
// 整个方法持有 mu，所以发送是全局串行的 —— 这正是想要的：
// Telegram 的限额是按 bot 算的，不是按调用点算的。
// 代价是 HN 推 20 条期间，/summary 的回复会排队等待。
func (t *Telegram) send(ctx context.Context, msg tgbotapi.MessageConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// time.Since(零值) 是一个巨大的正数，所以第一次发送 wait <= 0，不等待。
	if wait := sendInterval - time.Since(t.lastSend); wait > 0 {
		// 用 Timer 而不是 time.Sleep：这样等待期间可以被 ctx 取消。
		timer := time.NewTimer(wait)
		defer timer.Stop() // 提前返回时释放 timer，避免泄漏
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

	_, err := t.bot.Send(msg)
	// 无论成功失败都更新时间戳：失败往往也是被服务端限速了，更该等。
	t.lastSend = time.Now()
	if err != nil {
		return fmt.Errorf("telegram 消息推送失败: %w", err)
	}
	return nil
}

// NotifyTo 发一条消息到指定 chatID（如回复发指令的用户）。
// content 当**纯文本**：MarkdownV2 转义在这里统一做，调用方不用再 EscapeMarkdownV2。
func (t *Telegram) NotifyTo(ctx context.Context, chatID int64, content string) error {
	msg := tgbotapi.NewMessage(chatID, tools.EscapeMarkdownV2(content))
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.DisableWebPagePreview = false
	if err := t.send(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "telegram单笔信息推送失败", "err", err)
		return err
	}
	return nil
}

// NotifyMarkdown 发一条**已渲染好的 MarkdownV2** 到默认 chat。
// 与 Notify 的区别：不做整体转义 —— 调用方（monitor）已经把动态片段各自转义好了，
// 再转一次会把 *加粗* 和 [链接](url) 的标记本身也转义掉。
func (t *Telegram) NotifyMarkdown(ctx context.Context, content string) error {
	slog.InfoContext(ctx, content)
	msg := tgbotapi.NewMessage(t.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.DisableWebPagePreview = false
	if err := t.send(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "telegram预渲染消息推送失败", "err", err)
		return err
	}
	return nil
}
