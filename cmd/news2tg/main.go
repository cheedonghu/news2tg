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
	"net/url"   // 全局代理地址解析
	"os"        // 进程相关：os.Stderr / os.Exit / os.Interrupt
	"os/signal" // 监听系统信号（Ctrl-C 等）
	"strconv"   // 字符串 ↔ 数字
	"strings"   // 全局代理地址 TrimSpace
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
	"github.com/cheedonghu/news2tg/internal/music"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/store"
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
		slog.Error("failed to load config", "path", cli.Config, "err", err)
		os.Exit(1)
	}

	// 3) Telegram chat_id 在配置里是字符串，转成 int64。
	// strconv.ParseInt(s, 进制, 位宽)，返回 (int64, error)。
	chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
	if err != nil {
		slog.Error("invalid tg chat id", "err", err)
		os.Exit(1)
	}

	// 3.5) 全局代理：非空时对**所有** HTTP 出站生效。必须排在本函数里
	// 第一次构造任何 HTTP 客户端（下面第 4 步的 tgClient）**之前**。
	//
	// 为什么不能放到更后面、等共享 client 那一步再一起设（本来是这么写的，
	// 评审时被指出是 bug）：tgbotapi.NewBotAPIWithClient 在构造期间就会
	// 同步发一次 GetMe() 校验 token —— 也就是说"构造即请求"，不存在
	// "等第一次业务调用再触发"的余地。等 tgClient 构造完再设代理，等于让
	// 这次真实请求在代理设置生效之前就已经打出去了；如果代理是访问
	// Telegram 的唯一出路，GetMe() 会直连失败，下面第 4 步直接
	// os.Exit(1) —— 配了个完全合法的代理，进程反而起不来。
	//
	// 为什么只需要动这两处（这里 + 下面 8.1 的共享 client）、不必改任何
	// 构造函数签名：仓库里一共六个 HTTP 客户端构造点，其中五个
	// （notify.Telegram 的 &http.Client{Timeout: 30s}、tgbotapi.NewBotAPI
	// 内部的 &http.Client{}、go-openai DefaultConfig 的 &http.Client{} ×3）
	// Transport 字段都是 nil，net/http 会自动回落到 http.DefaultTransport ——
	// 覆盖它就等于同时改到那五个。剩下的第六个是 8.1 的共享 client，它自带
	// 显式 Transport，不吃 DefaultTransport，所以要单独把 Proxy 设一遍，
	// proxyFunc 在这里算好、留到 8.1 复用，不重复解析一遍配置。
	//
	// 注意：这一行改的是 http.DefaultTransport 这个包级全局可变变量，
	// 没有加锁保护；它的安全性完全依赖"跑在所有 goroutine 启动之前"这个
	// 时序保证——本函数此时还没有 go 出任何一个 monitor/bot goroutine，
	// 之后也不应该再有代码回来改它。谁要往前挪这段初始化顺序，得先确认
	// 这个前提仍然成立。
	var proxyFunc func(*http.Request) (*url.URL, error) // nil = 不走代理
	if p := strings.TrimSpace(cfg.Network.Proxy); p != "" {
		// 这里可以忽略 error：FromFile 已经校验过一遍，走到这儿必定合法。
		u, _ := url.Parse(p)
		// http.ProxyURL 返回的函数是**无条件**的：不管目标是谁，一律返回
		// 这个固定代理地址，不像 http.ProxyFromEnvironment 那样自带
		// loopback 豁免。这里必须自己补上，否则一个具体的场景会被打挂：
		// HN 摘要走的 Python sidecar（127.0.0.1:50051）和常见部署里跑
		// 在本机的 WebDAV（如 alist，127.0.0.1:5244）都在回环地址上——
		// 一旦启用代理，给它们的请求也会被送进代理，代理再去连它自己
		// 那侧的 127.0.0.1，必然连不上或连到不相干的东西。为救某个
		// 被墙的音源站而开的代理开关，会反过来把本机服务打挂。
		// 这不是引入一套 no_proxy 规则表——只豁免"回环"这一种情况，
		// 语义明确，不需要额外配置。
		proxied := http.ProxyURL(u)
		proxyFunc = func(req *http.Request) (*url.URL, error) {
			if host := req.URL.Hostname(); host == "localhost" || isLoopback(host) {
				return nil, nil // nil URL = 不走代理，直连
			}
			return proxied(req)
		}
		// 类型断言：DefaultTransport 的静态类型是 http.RoundTripper 接口，
		// 要拿到 Proxy 字段得先断言回具体的 *http.Transport。
		http.DefaultTransport.(*http.Transport).Proxy = proxyFunc
		// 日志里不能打代理地址原文：带认证的代理常写成
		// http://user:pass@proxy.example:7890，直接打印会把密码写进
		// stderr（进而进 Docker 日志）。u.Redacted() 是标准库为此而生的方法，
		// 会把密码部分替换成 xxxxx，主机、端口、用户名仍然保留、足够排查。
		slog.Info("全局 HTTP 代理已启用", "proxy", u.Redacted())
	}

	// 4) 初始化 Telegram 客户端（内部会真正去连一次 bot API 验证 token，
	// 这也是上面那段代理设置必须排在它之前的原因）
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

	// 7.5) 打开推送记录数据库。
	// 打不开就退出：去重是核心功能，静默降级会让人在不知情的情况下重复刷屏。
	// 这条同时兜住"忘了在 docker-compose 里加 ./data:/data 挂载"的场景。
	pushStore, err := store.OpenSQLite(cfg.Storage.DBPath)
	if err != nil {
		slog.Error("failed to open push record store", "path", cfg.Storage.DBPath, "err", err)
		os.Exit(1)
	}
	// defer 在 main 正常返回时执行；注意上面那些 os.Exit 路径不会触发 defer，
	// 但那时进程已经要死了，OS 会回收 fd，不影响。
	defer pushStore.Close()
	slog.Info("push record store opened", "path", cfg.Storage.DBPath)

	// 8.1) 共享 HTTP 客户端：连接池、超时配置全集中在这里。
	// proxyFunc 在 3.5 步就算好了（必须早于 tgClient 构造，见那里的注释），
	// 这里直接复用，不重复解析一遍配置。
	// &http.Client{...} 取地址：拿到 *http.Client 指针，方便共享同一个连接池。
	httpClient := &http.Client{
		//Timeout: 5 * time.Minute, // 整个请求总超时
		Transport: &http.Transport{
			Proxy: proxyFunc, // nil 时等价于不走代理

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
	hnMon := monitor.NewHackerNews(httpClient, tgClient, aiClient, digestFetcher, pushStore)
	v2exMon := monitor.NewV2EX(httpClient, tgClient, pushStore)

	// 9.1) 组装总结 agent：python（优先）+ jina（回退）两个提取器，复用 DeepSeek key。
	jinaFetcher := digest.NewJina(httpClient, cfg.Jina.APIToken)
	summaryAgent := agent.NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, digestFetcher, jinaFetcher)

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

	// 9.2.2) 音乐 agent：/music 指令用。
	//
	// ★ 关键陷阱：musicRunner 必须声明为**接口类型** command.MusicRunner，
	// 不能声明成 *music.Runner 再传进去。
	// 原因：Go 里"值为 nil 的具体类型指针"塞进接口变量后，接口本身不是 nil
	// （接口 = 类型 + 值，类型那格非空）。若写成 var mr *music.Runner 然后
	// 在 [music] 未配置时把这个 nil 指针传给 NewBot，command 包里的
	// `b.music == nil` 判断会为 false，/music 会被当成"已配置"，
	// 真跑起来直接 nil 指针解引用 panic。
	// 声明成接口类型、只在配置齐全时赋值，就能避开这个坑。
	var musicRunner command.MusicRunner
	if cfg.Music.Configured() {
		// 音源工厂表：名字 → 怎么造。加音源时在这里多一项，
		// 同时在 config 的 knownMusicSources / DefaultMusicSources 里登记。
		// 用工厂函数而不是直接建实例：没被启用的音源不必白白构造。
		musicSourceFactory := map[string]func() music.Source{
			"musicso": func() music.Source { return music.NewMusicSo(httpClient) },
			"mp3pm":   func() music.Source { return music.NewMp3PM(httpClient) },
		}
		// 按配置顺序构造 —— 这个切片的顺序会一路传到系统提示词里，
		// 成为模型"先试哪个源"的依据，所以顺序本身就是配置语义。
		enabled := cfg.Music.EffectiveSources()
		sources := make([]music.Source, 0, len(enabled))
		for _, name := range enabled {
			// 配置校验已经保证每个名字都在工厂表里，这里的 ok 判断
			// 只为防止将来改动时两处失配（漏了在工厂表登记新音源）。
			newSource, ok := musicSourceFactory[name]
			if !ok {
				slog.Error("音源已在配置中启用但没有对应实现，请检查 main 里的工厂表", "source", name)
				os.Exit(1)
			}
			sources = append(sources, newSource())
		}
		// 复用 DeepSeek 的 key 和 agent_model（同样需要 function calling 能力）。
		musicAgent := music.NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, sources)
		// 上传目标：只收**完整**的那些。
		// 配置校验已经保证"至少有一个完整目标、且不存在半个目标"，
		// 所以这里只需判空，不必再处理半配置的情况。
		// Uploader 内部按切片循环，1 个和 2 个走的是同一条代码路径。
		targets := make([]music.Target, 0, 2)
		if strings.TrimSpace(cfg.Music.WebdavURL1) != "" {
			targets = append(targets, music.Target{Name: cfg.Music.WebdavName1, URL: cfg.Music.WebdavURL1})
		}
		if strings.TrimSpace(cfg.Music.WebdavURL2) != "" {
			targets = append(targets, music.Target{Name: cfg.Music.WebdavName2, URL: cfg.Music.WebdavURL2})
		}
		uploader := music.NewUploader(httpClient, targets, cfg.Music.WebdavUser, cfg.Music.WebdavPass)
		// tgClient 同时是 notify.Notifier 和 notify.Editor，这里用的是后者。
		musicRunner = music.NewRunner(musicAgent, uploader, tgClient)
		slog.Info("音乐功能已启用", "targets", len(targets), "sources", enabled)
	} else {
		slog.Warn("[music] 未配置，/music 指令不可用")
	}

	// 9.3) 命令 bot：收 /summary <网址> 和 /music <歌曲描述>。
	cmdBot, err := command.NewBot(cfg.Telegram.APIToken, summaryAgent, tgClient, adminIDs, musicRunner)
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

// isLoopback 判断一个 host 字符串是否是回环地址（127.0.0.0/8 或 ::1）。
//
// 只处理已经是 IP 字面量的情况：net.ParseIP 对域名（如 "localhost"）返回
// nil，所以 "localhost" 这个常见写法要在调用方单独比较字符串兜底——
// 见上面 proxyFunc 里 host == "localhost" 那一判断。
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
