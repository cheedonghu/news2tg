// 这是程序入口。
// Go 规则：可执行程序的入口包必须叫 `package main`，而且必须有一个 `func main()`。
// 编译时 `go build ./cmd/news2tg` 会以"目录名"news2tg 作为二进制名。
package main

import (
	"context"  // 上下文：传 cancel/超时
	"fmt"      // 格式化输出
	"log/slog" // Go 1.21+ 官方结构化日志
	"net"
	"net/http"  // HTTP 客户端
	"os"        // 进程相关：os.Stderr / os.Exit / os.Interrupt
	"os/signal" // 监听系统信号（Ctrl-C 等）
	"strconv"   // 字符串 ↔ 数字
	"sync"      // sync.WaitGroup 等待多个 goroutine
	"syscall"   // SIGTERM 等系统信号常量
	"time"

	"github.com/cheedonghu/news2tg/internal/agent"
	"github.com/cheedonghu/news2tg/internal/ai"
	"github.com/cheedonghu/news2tg/internal/command"
	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/digest"
	"github.com/cheedonghu/news2tg/internal/logx"
	"github.com/cheedonghu/news2tg/internal/monitor"
	"github.com/cheedonghu/news2tg/internal/notify"
)

// 初始化slog：JSON handler 外面再包一层 logx.Handler，
// 这样链路里用 slog.*Context(ctx, ...) 打的日志会自动带上 ctx 里的 task_id。
func init() {
	base := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false,
	})
	slog.SetDefault(slog.New(logx.New(base)))
}

// main 是 Go 程序的唯一入口。无参数、无返回值。
// 程序退出有两种方式：main 自然返回 / 调 os.Exit(code)。
// 注意：os.Exit 不会触发 defer！只有正常 return 才会。
func main() {
	// 1) 解析命令行参数（-c config.toml）
	cli := config.ParseCli()

	// 2) 加载 TOML 配置；失败直接打印 + 退出码 1
	cfg, err := config.FromFile(cli.Config)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// 3) Telegram chat_id 在配置里是字符串，转成 int64。
	// strconv.ParseInt(s, 进制, 位宽)，返回 (int64, error)。
	chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
	if err != nil {
		slog.Error("invalid tg chat id", "err", err)
		os.Exit(1)
	}

	// 4) 初始化 Telegram 客户端（内部会真正去连一次 bot API 验证 token）
	tgClient, err := notify.NewTelegram(cfg.Telegram.APIToken, chatID)
	if err != nil {
		slog.Error("failed to init telegram", "err", err)
		os.Exit(1)
	}

	// 5) 拼一条启动通知
	startupText := fmt.Sprintf(
		"news2tg启动完成，监控任务开始投递内容。\n启动时间：[%s]\n项目地址：https://github.com/cheedonghu/news2tg",
		time.Now().Format("2006-01-02 15:04"), // Go 的"魔法时间格式"，固定写这串数字
	)
	slog.Info(startupText)

	// 6) 创建一个能被信号取消的 ctx：
	//    - signal.NotifyContext 监听 Interrupt（Ctrl-C）和 SIGTERM
	//    - 收到信号后自动调 cancel()，所有持有这个 ctx 的子任务都会被通知
	//    - 返回的 cancel 也保留下来，供 monitor 出错时手动触发关闭
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// defer cancel()：即使 main 正常退出，也调一次 cancel；防止信号处理器泄漏。
	defer cancel()

	// 7) 把启动文案推给 Telegram。转义由 notify 内部统一处理，这里直接传纯文本。
	if err := tgClient.Notify(ctx, startupText); err != nil {
		slog.Error("failed to send startup notification")
	}

	// 8) 共享 HTTP 客户端：连接池、超时配置全集中在这里。
	// &http.Client{...} 取地址：拿到 *http.Client 指针，方便共享同一个连接池。
	httpClient := &http.Client{
		//Timeout: 5 * time.Minute, // 整个请求总超时
		Transport: &http.Transport{
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 5, // 仍然要配，复用连接省握手
			IdleConnTimeout:     150 * time.Second,

			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second, // 建连仍然短！故障快速失败
				KeepAlive: 30 * time.Second,
			}).DialContext,

			ResponseHeaderTimeout: 180 * time.Second, // 看是否流式调整：流式可短，非流式要长
			ForceAttemptHTTP2:     false,
		},
	}

	// 9) 构造各个组件
	aiClient := ai.NewDeepSeek(cfg.DeepSeek.APIToken, cfg.DeepSeek.Model)
	digestFetcher := digest.NewPython(httpClient) // 当前用 Python sidecar；后续可换 agent 渠道
	hnMon := monitor.NewHackerNews(httpClient, tgClient, aiClient, digestFetcher)
	v2exMon := monitor.NewV2EX(httpClient, tgClient)

	// 9.1) 组装总结 agent：python（优先）+ jina（回退）两个提取器，复用 DeepSeek key。
	jinaFetcher := digest.NewJina(httpClient, cfg.Jina.APIToken)
	summaryAgent := agent.NewAgent(cfg.DeepSeek.APIToken, digestFetcher, jinaFetcher)

	// 9.2) 解析白名单 user id（字符串 → int64，坏值仅 log 跳过）。
	adminIDs := make([]int64, 0, len(cfg.Telegram.AdminIDs))
	for _, s := range cfg.Telegram.AdminIDs {
		id, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			slog.Warn("跳过非法 admin_id", "value", s, "err", perr)
			continue
		}
		adminIDs = append(adminIDs, id)
	}

	// 9.2.1) 解析每日天气 @ 提及列表（"id" 或 "id:显示名"，与 admin_ids 独立）。
	mentions := make([]config.Mention, 0, len(cfg.Features.WeatherMention))
	for _, s := range cfg.Features.WeatherMention {
		m, perr := config.ParseMention(s)
		if perr != nil {
			slog.Warn("跳过非法 weather_mention", "value", s, "err", perr)
			continue
		}
		mentions = append(mentions, m)
	}

	// aiClient 已实现 Advise，天然满足 monitor.Advisor。
	weatherMon := monitor.NewWeather(httpClient, tgClient, aiClient, mentions)

	// 9.3) 命令 bot：收 /summary <网址>，调 agent 总结后推送到频道（tgClient）。
	cmdBot, err := command.NewBot(cfg.Telegram.APIToken, summaryAgent, tgClient, adminIDs)
	if err != nil {
		slog.Error("failed to init command bot", "err", err)
		os.Exit(1)
	}

	// 10) 把所有 monitor 收到一张表里，便于循环启动 goroutine。
	// 这里用匿名结构体切片：临时只在 main 里用一下，不值得起名字。
	monitors := []struct {
		name string
		m    monitor.Monitor // 接口类型；HackerNews / V2EX / command bot 都实现了它
	}{
		{"hackernews", hnMon},
		{"v2ex", v2exMon},
		{"weather", weatherMon}, // 新增：每日天气定点推送
		{"command-bot", cmdBot},
	}

	// 11) 启动所有 monitor，等它们全部退出后 main 才返回。
	// sync.WaitGroup 是计数器：Add(n) 加，Done() 减 1，Wait() 阻塞到归零。
	var wg sync.WaitGroup
	for _, item := range monitors {
		wg.Add(1)
		// ★ 经典陷阱（Go 1.22 之前必须 shadow）：
		// for-range 里 `item` 是被复用的同一个变量地址；如果不重新声明，
		// 下面 goroutine 闭包里所有 item 都指向最后一次循环的值。
		item := item

		// `go func() { ... }()`：启动一个新 goroutine 跑这个闭包；末尾的 () 是立即调用。
		go func() {
			// defer wg.Done()：goroutine 退出时计数减 1，Wait 才能返回。
			defer wg.Done()
			if err := item.m.Run(ctx, cfg); err != nil {
				// ctx.Err() == nil 说明不是被取消的；那就是真出错了，记录 + 主动 cancel 关掉所有人。
				if ctx.Err() == nil {
					slog.Error("monitor exited unexpectedly", "name", item.name, "err", err)
					cancel() // 触发其它 monitor 的 ctx.Done()，连锁退出
				} else {
					// 被外部取消的正常退出，仅打印
					slog.Error("monitor stopped", "name", item.name, "err", err)
				}
			}
		}()
	}

	// 12) 主 goroutine 阻塞在 ctx.Done() 上，直到收到信号或 cancel。
	<-ctx.Done()
	slog.Info("received shutdown signal, terminating...")
	// 等所有 monitor goroutine 退出
	wg.Wait()
}
