// Package command 实现"通过 Telegram 指令驱动"的能力：
// 管理员给 bot 发指令，服务执行对应任务。目前认两条：
//
//	/summary <网址>      调总结 agent 产出中文总结，推送到配置的频道
//	/music   <歌曲描述>  调音乐 agent 下载歌曲并上传到 WebDAV，进度私聊回报
//
// 与 notify 不同，这个包要**收** Telegram update（long-poll getUpdates），
// 所以它自己持有一个 *tgbotapi.BotAPI；推送结果时复用 notify.Notifier。
package command

import (
	"context"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/logx"
	"github.com/cheedonghu/news2tg/internal/notify"
)

// 本 bot 认的两条命令名。常量与 classify 的判定保持单一来源，避免拼写漂移。
const (
	commandSummary = "summary"
	commandMusic   = "music"
)

// summarizeTimeout 单条 /summary 指令调 agent 的超时（agent 可能多轮调模型，给宽松点）。
const summarizeTimeout = 180 * time.Second

// musicTimeout 是一次 /music 任务的**总**预算：
// 内部还有 agent 循环 180s、每个 WebDAV 目标 300s 两层更细的超时，
// 这一层是兜底，防止某条任务永久挂着占资源。
const musicTimeout = 600 * time.Second

// Summarizer 是消费侧接口（"accept interfaces"）：本包只依赖"网址 → 总结"这一能力，
// 不直接 import agent 包。*agent.Agent 天然满足。
type Summarizer interface {
	Summarize(ctx context.Context, url string) (string, error)
}

// MusicRunner 是消费侧接口（"accept interfaces"）：本包只依赖"一句歌曲描述 → 跑完整条链路"
// 这一能力，不直接 import music 包。*music.Runner 天然满足。
//
// 导出（而非像 Summarizer 那样也导出即可）是因为 main 需要声明这个类型的变量 ——
// 见 main.go 里关于"接口里塞 typed nil"的注释。
type MusicRunner interface {
	Run(ctx context.Context, chatID int64, query string) error
}

// Bot 监听 Telegram 指令并驱动对应任务。
type Bot struct {
	bot      *tgbotapi.BotAPI // 收侧：自己的 bot 实例（notify 那个只发不收，互不冲突）
	agent    Summarizer       // 网址 → 中文总结
	music    MusicRunner      // 歌曲描述 → 下载并上传；[music] 未配置时为 nil
	notifier notify.Notifier  // 推送结果到配置频道（复用 tgClient）
	admins   map[int64]bool   // 白名单：允许下指令的 user id
}

// NewBot 构造函数。token 与 notify 用同一个 bot token；adminIDs 为允许的 user id 列表。
// music 可以为 nil（[music] 未配置），此时 /music 不注册到命令清单，收到也只回一句未配置。
func NewBot(token string, agent Summarizer, notifier notify.Notifier, adminIDs []int64, music MusicRunner) (*Bot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	admins := make(map[int64]bool, len(adminIDs))
	for _, id := range adminIDs {
		admins[id] = true
	}
	return &Bot{bot: bot, agent: agent, music: music, notifier: notifier, admins: admins}, nil
}

// Run 阻塞式跑 long-poll 循环，签名匹配 monitor.Monitor，可塞进 main 的 monitors 表。
// ctx 取消时停止收 update 并返回 ctx.Err()（正常关停）。
func (b *Bot) Run(ctx context.Context, _ *config.Config) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30 // long-poll：每次最多挂 30s 等新消息，省请求
	updates := b.bot.GetUpdatesChan(u)
	defer b.bot.StopReceivingUpdates() // 退出时让 SDK 的收取 goroutine 停下来

	// 向 Telegram 注册命令清单：客户端键入 "/" 的自动补全弹窗就来自这份清单。
	// 只 long-poll 收命令不会注册，必须显式调 setMyCommands。
	cmds := []tgbotapi.BotCommand{
		{Command: commandSummary, Description: "总结网址并推送到频道"}, // 3-256 字符，展示在候选项里
	}
	// 音乐功能没配就不注册，免得补全里挂着一条点了只会报错的命令。
	if b.music != nil {
		cmds = append(cmds, tgbotapi.BotCommand{Command: commandMusic, Description: "下载歌曲并上传到网盘"})
	}
	cmdCfg := tgbotapi.NewSetMyCommands(cmds...)
	// 注册失败不影响收指令（命令仍可手动键入执行），遵循本仓库"失败不中断"约定：仅告警。
	if _, err := b.bot.Request(cmdCfg); err != nil {
		slog.Warn("注册 Telegram 命令清单失败（不影响指令执行）", "err", err)
	}

	slog.Info("command bot 已启动", "commands", len(cmds))
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
	actionIgnore       action = iota // 不是给我们的指令（非命令 / 不认识的命令）
	actionUnauthorized               // 是我们的指令，但发送者不在白名单
	actionUnavailable                // 指令对应的功能没配置（如 [music] 缺失）
	actionUsage                      // 白名单用户，但参数不合法
	actionRun                        // 白名单用户 + 合法参数，去执行
)

// intent 是一条消息的完整判定：做什么、是哪条命令、参数是什么。
//
// 为什么不像以前那样返回 (action, url)？
// 因为 action 枚举里的 actionSummarize 把 summary 语义焊死在决策树里，
// 加第二条命令就没处挂。拆成 act/cmd/args 三元组后，加第三条命令只需在
// 下面的 switch 里多一个 case，判定层的形状不用再动。
type intent struct {
	act  action
	cmd  string // commandSummary | commandMusic；actionIgnore 时为空
	args string // 命令参数（已 TrimSpace）
}

// classify 是纯函数式判定：只读 msg 和 Bot 上的白名单/依赖，不做任何副作用
// （不发消息、不调 agent），因此可以脱离真实 bot 单测整条决策树。
func (b *Bot) classify(msg *tgbotapi.Message) intent {
	// 只处理消息类指令；其它 update（编辑、回调等）和不认识的命令一律忽略。
	if msg == nil || !msg.IsCommand() {
		return intent{act: actionIgnore}
	}
	cmd := msg.Command()
	if cmd != commandSummary && cmd != commandMusic {
		return intent{act: actionIgnore}
	}

	var fromID int64
	if msg.From != nil {
		fromID = msg.From.ID
	}
	if !b.admins[fromID] {
		return intent{act: actionUnauthorized, cmd: cmd}
	}

	args := strings.TrimSpace(msg.CommandArguments())

	switch cmd {
	case commandSummary:
		if !strings.HasPrefix(args, "http") {
			return intent{act: actionUsage, cmd: cmd}
		}
	case commandMusic:
		// 依赖缺失优先于参数校验：没配功能时，纠结参数格式没有意义。
		if b.music == nil {
			return intent{act: actionUnavailable, cmd: cmd}
		}
		// 歌曲描述是自由格式（"晴天 - 周杰伦"/"周杰伦 晴天"/"晴天"都行），
		// 交给模型去理解，这里只要求非空。
		if args == "" {
			return intent{act: actionUsage, cmd: cmd}
		}
	}

	return intent{act: actionRun, cmd: cmd, args: args}
}

// usageText 按命令给出对应的用法提示。
func usageText(cmd string) string {
	if cmd == commandMusic {
		return "用法：/music <歌曲描述>，如 /music 晴天 - 周杰伦（格式随意，我会自己理解）"
	}
	return "用法：/summary <网址>（网址需以 http 开头）"
}

// handle 根据 classify 的判定做副作用：记日志、回话、异步执行任务。
func (b *Bot) handle(ctx context.Context, up tgbotapi.Update) {
	msg := up.Message
	in := b.classify(msg)
	if in.act == actionIgnore {
		return
	}

	// 每条指令一个 task_id：本条命令链路的日志共享它。
	ctx = logx.WithTaskID(ctx, logx.NewTaskID())

	// 记录发送者 id：既便于审计，也方便用户从日志里抄自己的 id 去填白名单。
	var fromID int64
	if msg.From != nil {
		fromID = msg.From.ID
	}
	slog.InfoContext(ctx, "收到指令", "cmd", in.cmd, "from", fromID, "chat", msg.Chat.ID)

	switch in.act {
	case actionUnauthorized:
		// 非管理员不回话，避免被陌生人探测/刷量。
		slog.WarnContext(ctx, "非白名单用户触发指令，已忽略", "cmd", in.cmd, "from", fromID)
	case actionUnavailable:
		b.reply(ctx, msg.Chat.ID, "音乐功能未配置（缺少 [music] 段），该指令不可用。")
	case actionUsage:
		b.reply(ctx, msg.Chat.ID, usageText(in.cmd))
	case actionRun:
		// 异步处理：两条任务都慢，别堵住 poll 循环。管理员量小，不设并发上限。
		switch in.cmd {
		case commandSummary:
			go b.summarizeAndPush(ctx, msg.Chat.ID, in.args)
		case commandMusic:
			go b.runMusic(ctx, msg.Chat.ID, in.args)
		}
	}
}

// runMusic 跑一次音乐任务。
//
// 注意这里**不发任何消息**：进度和最终结果都由 music.Runner 通过原地编辑
// 它自己那条消息来回报，这里再回一句只会重复刷屏。失败也只记日志 ——
// 用户已经能在那条进度消息的终态里看到失败原因了。
func (b *Bot) runMusic(ctx context.Context, chatID int64, query string) {
	cctx, cancel := context.WithTimeout(ctx, musicTimeout)
	defer cancel()

	if err := b.music.Run(cctx, chatID, query); err != nil {
		slog.ErrorContext(ctx, "音乐任务失败", "query", query, "err", err)
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
