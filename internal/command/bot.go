// Package command 实现"通过 Telegram 指令驱动"的能力：
// 管理员给 bot 发 /summary <网址>，服务调用总结 agent 产出中文总结，再推送到配置的频道。
//
// 与 notify 不同，这个包要**收** Telegram update（long-poll getUpdates），
// 所以它自己持有一个 *tgbotapi.BotAPI；推送结果时复用 notify.Notifier（同一个频道）。
package command

import (
	"context"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/cheedonghu/news-notify/internal/config"
	"github.com/cheedonghu/news-notify/internal/logx"
	"github.com/cheedonghu/news-notify/internal/notify"
)

// commandSummary 是本 bot 唯一认的命令名（对应 "/summary"）。
const commandSummary = "summary"

// summarizeTimeout 单条指令调 agent 的超时（agent 可能多轮调模型，给宽松点）。
const summarizeTimeout = 180 * time.Second

// Summarizer 是消费侧接口（"accept interfaces"）：本包只依赖"网址 → 总结"这一能力，
// 不直接 import agent 包。*agent.Agent 天然满足。
type Summarizer interface {
	Summarize(ctx context.Context, url string) (string, error)
}

// Bot 监听 Telegram 指令并驱动总结。
type Bot struct {
	bot      *tgbotapi.BotAPI // 收侧：自己的 bot 实例（notify 那个只发不收，互不冲突）
	agent    Summarizer       // 网址 → 中文总结
	notifier notify.Notifier  // 推送结果到配置频道（复用 tgClient）
	admins   map[int64]bool   // 白名单：允许下指令的 user id
}

// NewBot 构造函数。token 与 notify 用同一个 bot token；adminIDs 为允许的 user id 列表。
func NewBot(token string, agent Summarizer, notifier notify.Notifier, adminIDs []int64) (*Bot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	admins := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		admins[id] = true
	}
	return &Bot{bot: bot, agent: agent, notifier: notifier, admins: admins}, nil
}

// Run 阻塞式跑 long-poll 循环，签名匹配 monitor.Monitor，可塞进 main 的 monitors 表。
// ctx 取消时停止收 update 并返回 ctx.Err()（正常关停）。
func (b *Bot) Run(ctx context.Context, _ *config.Config) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30 // long-poll：每次最多挂 30s 等新消息，省请求
	updates := b.bot.GetUpdatesChan(u)
	defer b.bot.StopReceivingUpdates() // 退出时让 SDK 的收取 goroutine 停下来

	slog.Info("command bot 已启动，监听 /summary 指令")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case up, ok := <-updates:
			if !ok {
				// channel 被关闭（StopReceivingUpdates 之后），正常退出。
				return nil
			}
			b.handle(ctx, up)
		}
	}
}

// action 是 classify 对一条消息的判定结果。
type action int

const (
	actionIgnore       action = iota // 不是给我们的指令（非命令 / 非 /summary）
	actionUnauthorized               // 是 /summary，但发送者不在白名单
	actionUsage                      // 白名单用户，但参数不是合法网址
	actionSummarize                  // 白名单用户 + 合法网址，去总结
)

// classify 是纯函数式判定：只读 msg 和白名单，不做任何副作用（不发消息、不调 agent），
// 因此可以脱离真实 bot 单测整条决策树。
func (b *Bot) classify(msg *tgbotapi.Message) (act action, url string) {
	// 只处理消息类指令；其它 update（编辑、回调等）和非 /summary 命令一律忽略。
	if msg == nil || !msg.IsCommand() || msg.Command() != commandSummary {
		return actionIgnore, ""
	}
	var fromID int64
	if msg.From != nil {
		fromID = msg.From.ID
	}
	if !b.admins[fromID] {
		return actionUnauthorized, ""
	}
	url = strings.TrimSpace(msg.CommandArguments())
	if !strings.HasPrefix(url, "http") {
		return actionUsage, ""
	}
	return actionSummarize, url
}

// handle 根据 classify 的判定做副作用：记日志、回话、异步总结。
func (b *Bot) handle(ctx context.Context, up tgbotapi.Update) {
	msg := up.Message
	act, url := b.classify(msg)
	if act == actionIgnore {
		return
	}

	// 每条 /summary 命令一个 task_id：本条命令链路（收指令→提取→审计→推送/回复）的日志共享它。
	ctx = logx.WithTaskID(ctx, logx.NewTaskID())

	// 记录发送者 id：既便于审计，也方便用户从日志里抄自己的 id 去填白名单。
	var fromID int64
	if msg.From != nil {
		fromID = msg.From.ID
	}
	slog.InfoContext(ctx, "收到 /summary 指令", "from", fromID, "chat", msg.Chat.ID)

	switch act {
	case actionUnauthorized:
		// 非管理员不回话，避免被陌生人探测/刷量。
		slog.WarnContext(ctx, "非白名单用户触发 /summary，已忽略", "from", fromID)
	case actionUsage:
		b.reply(ctx, msg.Chat.ID, "用法：/summary <网址>（网址需以 http 开头）")
	case actionSummarize:
		// 异步处理：agent 总结较慢，别堵住 poll 循环。管理员量小，不设并发上限。
		go b.summarizeAndPush(ctx, msg.Chat.ID, url)
	}
}

// summarizeAndPush 调 agent 总结：结果推送到配置频道，反馈（成功 ack/失败）回给发指令的人。
func (b *Bot) summarizeAndPush(ctx context.Context, chatID int64, url string) {
	cctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
	defer cancel()

	summary, err := b.agent.Summarize(cctx, url)
	if err != nil {
		slog.ErrorContext(ctx, "command bot 总结失败", "url", url, "err", err)
		b.reply(ctx, chatID, "总结失败："+err.Error())
		return
	}
	// 总结结果推送到默认频道（notify 内部做转义）。
	if err := b.notifier.Notify(ctx, "网址总结 "+url+"\n\n"+summary); err != nil {
		slog.ErrorContext(ctx, "command bot 推送频道失败", "url", url, "err", err)
		b.reply(ctx, chatID, "已总结，但推送频道失败："+err.Error())
		return
	}
	b.reply(ctx, chatID, "已总结并推送到频道。")
}

// reply 复用 notify.Notifier 把文本回给发指令的会话。
// 转义交给 notify（NotifyTo 内部做 MarkdownV2 转义），业务代码不再关心。
func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.notifier.NotifyTo(ctx, chatID, text); err != nil {
		slog.ErrorContext(ctx, "command bot 回复失败", "chat", chatID, "err", err)
	}
}
