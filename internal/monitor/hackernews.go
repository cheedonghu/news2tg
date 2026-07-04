package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv" // 字符串和数字互转
	"strings"
	"sync"
	"time"

	// 第三方包：HTML 解析（类似 jQuery 的 API）
	"github.com/PuerkitoBio/goquery"

	"github.com/cheedonghu/news-notify/internal/ai"
	"github.com/cheedonghu/news-notify/internal/config"
	"github.com/cheedonghu/news-notify/internal/digest"
	"github.com/cheedonghu/news-notify/internal/logx"
	"github.com/cheedonghu/news-notify/internal/model"
	"github.com/cheedonghu/news-notify/internal/notify"
	"github.com/cheedonghu/news-notify/internal/tools"
)

// Hacker News 相关端点说明：
//   - hnTopURL / hnNewURL  Firebase 官方 API，只返回 ID 数组
//   - hnItemURL            网页版帖子页，用来抓"几小时前发布"和"原文链接"（API 不带这些文案）
//
// 正文抽取的端点已抽到 digest 包（digest.Fetcher 接口 + Python 实现）。
const (
	hnTopURL    = "https://hacker-news.firebaseio.com/v0/topstories.json?print=pretty"
	hnNewURL    = "https://hacker-news.firebaseio.com/v0/newstories.json?print=pretty"
	hnItemURL   = "https://news.ycombinator.com/item?id=%s" // %s 是 Sprintf 的占位符
	hnUserAgent = "PostmanRuntime/7.37.3"
)

// HackerNews 结构体：比 V2EX 多了一个 ai.Helper（接口）用来做中文摘要。
// 字段全小写 = 包外不可见，外部只能通过 NewHackerNews + 方法操作。
type HackerNews struct {
	httpClient *http.Client
	notifier   notify.Notifier
	ai         ai.Helper      // 接口类型，运行时是 *ai.DeepSeek 的实例
	digest     digest.Fetcher // 接口类型，运行时是 *digest.Python 的实例（后续可换 agent 渠道）

	mu         sync.RWMutex
	pushedURLs map[string]string // id（不是 URL！HN 用 ID 标识）→ yyyymmdd
}

// NewHackerNews 构造函数。
// 参数 aiHelper / digestFetcher 都用接口类型，便于测试时传 mock、便于日后换实现；返回 *HackerNews 指针。
func NewHackerNews(httpClient *http.Client, notifier notify.Notifier, aiHelper ai.Helper, digestFetcher digest.Fetcher) *HackerNews {
	return &HackerNews{
		httpClient: httpClient,
		notifier:   notifier,
		ai:         aiHelper,
		digest:     digestFetcher,
		pushedURLs: make(map[string]string), // map 必须 make
	}
}

// Run 5 分钟一轮。比 V2EX 慢，因为 HN 每条都要抓正文 + 调 AI，开销大。
func (m *HackerNews) Run(ctx context.Context, cfg *config.Config) error {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop() // 退出时停 ticker，释放底层 goroutine

	for {

		// 本轮一个 task_id：这轮拉取/摘要/通知链路（含每帖的 digest、AI）日志都带上它。
		cctx := logx.WithTaskID(ctx, logx.NewTaskID())

		results, err := m.fetch(cctx, cfg)
		if err != nil {
			slog.ErrorContext(cctx, "获取hacker news信息失败", "err", err)
			return err
		}

		if len(results) > 0 {
			// AI 摘要单独一步，方便日后加开关时只摘掉这一段。
			processed, err := m.aiTransfer(cctx, results)
			if err != nil {
				slog.ErrorContext(cctx, "HN ai_transfer failed", "err", err)
				continue // continue：跳到下一轮 for 循环
			}
			contents := make([]string, 0, len(processed))
			for _, p := range processed {
				contents = append(contents, p.Content)
			}
			if err := m.notifier.NotifyBatch(cctx, contents); err != nil {
				slog.ErrorContext(cctx, "HN 通知失败", "err", err)
			}
		}

		m.cleanOldURLs(time.Now())

		// select 等"ctx 取消"或"ticker 到点"
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// fetch 拉 top + new 两个 ID 列表，截前 N 条，逐条 process。
// 串行执行（没用 goroutine），避免给本机 Python sidecar 打太狠。
func (m *HackerNews) fetch(ctx context.Context, cfg *config.Config) ([]model.NotifyBase, error) {
	var hotIDs, newIDs []string // 一次声明两个变量，类型一致

	if cfg.Features.HnFetchTop {
		ids, err := m.fetchIDs(ctx, hnTopURL)
		if err != nil {
			slog.ErrorContext(ctx, "hackernews fetch_hot error", "err", err)
		} else {
			hotIDs = takeN(ids, cfg.Features.HnFetchNum) // 调用泛型函数（下方定义）
		}
	}
	if cfg.Features.HnFetchLatest {
		ids, err := m.fetchIDs(ctx, hnNewURL)
		if err != nil {
			slog.ErrorContext(ctx, "hackernews fetch_latest error", "err", err)
		} else {
			newIDs = takeN(ids, cfg.Features.HnFetchNum)
		}
	}

	const hotTitle = "Hacker News 热帖推送"
	const newTitle = "Hacker News 新帖推送"

	var result []model.NotifyBase

	for _, id := range hotIDs {
		// process 返回 *model.NotifyBase（指针）；nil 表示"跳过这条"。
		if out := m.process(ctx, id, cfg.Features.HnFetchTimeGap); out != nil {
			// 注意：`if 变量 := ...; 条件 { ... }` 是 Go 的 if-init 语法，
			// 变量 out 的作用域只在这个 if 块内。
			out.Title = hotTitle          // 通过指针修改原值，不需要再赋回去
			result = append(result, *out) // *out 是"解引用"，把指针指向的结构体拷贝进切片
		}
	}
	for _, id := range newIDs {
		if out := m.process(ctx, id, cfg.Features.HnFetchTimeGap); out != nil {
			out.Title = newTitle
			result = append(result, *out)
		}
	}

	return result, nil
}

// fetchIDs 调 Firebase 端点，返回 uint64 数组，转成字符串方便后续拼 URL。
func (m *HackerNews) fetchIDs(ctx context.Context, fetchURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", hnUserAgent)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		// %w 包装：保留原错误，外层可以用 errors.Is/As 判断根因
		return nil, fmt.Errorf("network: %w", err)
	}
	defer resp.Body.Close() // 确保关闭响应体（很重要，不关会泄漏 TCP 连接）
	// 哪些操作需要主动调用defer关闭？
	// 1.看函数返回的类型有没有 Close / Stop / Cancel / Release / Unlock 这类方法
	// 2.看文档/godoc 第一句
	// 3.心里问"这东西底下是不是占了 OS 资源 / 网络连接 / 内存池"
	// 		文件描述符（fd）—— 文件、socket、管道
	// 		TCP 连接 / 连接池里的连接
	// 		OS 锁、信号量
	// 		数据库连接、prepared statement
	// 		goroutine（有些 API 内部开了 goroutine，关 = 让它退出）
	// 		后台定时器（time.Ticker / time.Timer）

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var ids []uint64 // uint64 = 无符号 64 位整数
	if err := json.Unmarshal(body, &ids); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	out := make([]string, 0, len(ids)) // 预分配容量
	for _, id := range ids {
		// strconv.FormatUint(值, 进制) → 字符串
		out = append(out, strconv.FormatUint(id, 10))
	}
	return out, nil
}

// process 处理单条 HN 帖子。
// 返回 *NotifyBase 还是 nil：nil 表示"跳过"（已推过 / 太新 / 解析失败）。
// 用指针返回的好处：调用方一个 nil 比较就能判断要不要处理。
func (m *HackerNews) process(ctx context.Context, id string, timeGap int) *model.NotifyBase {
	if m.alreadyPushed(id) {
		// fmt.Printf 直接打印到 stdout（这里没用 slog 是历史原因，可以统一）
		slog.InfoContext(ctx, "消息已经推送过", "id", id)
		return nil
	}

	slog.InfoContext(ctx, "消息开始解析", "id", id)
	//fmt.Printf("%s 开始解析id: %s\n", now.Format("2006年01月02日 15:04:05"), id)

	// fmt.Sprintf：拼字符串。%s 是占位符，对应后面的 id。
	pageURL := fmt.Sprintf(hnItemURL, id)
	body, err := m.httpGetText(ctx, pageURL)
	if err != nil {
		slog.ErrorContext(ctx, "获取hackernews帖子内容失败", "err", err)
		return nil
	}

	if !judgeNewsDate(ctx, body, timeGap) {
		return nil
	}

	originURL, err := getNewsOriginURL(ctx, body)
	if err != nil {
		return nil
	}

	// 这里用 &model.NotifyBase{...} 拿到指针，后面才能给 out.Content 赋值并返回出去。
	out := &model.NotifyBase{
		URL:       pageURL,
		OriginURL: originURL,
	}

	// 局部变量取名 content，避免和 import 进来的 digest 包名冲突。
	content, err := m.digest.Fetch(ctx, originURL)
	if err != nil {
		slog.ErrorContext(ctx, "网页摘要获取失败", "err", err)
		// flag 置 false：后续 aiTransfer 不会再调 AI，省一次失败调用。
		out.ContentTransferedByAIFlag = false
		out.Content = "网页摘要获取失败"
	} else {
		out.Content = content
		out.ContentTransferedByAIFlag = true
	}

	m.markPushed(id)
	return out
}

// httpGetText 一个简易 GET → string 工具，HN 内部用。
func (m *HackerNews) httpGetText(ctx context.Context, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err // 返回零值 "" + err
	}
	req.Header.Set("User-Agent", hnUserAgent)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// []byte → string 的显式转换
	return string(body), nil
}

// aiTransfer 给每条消息走一遍 DeepSeek 摘要，然后套上 Telegram 消息模板。
// 入参是切片，返回也是切片；中间修改是值拷贝（因为 range 出来的是拷贝），
// 所以不会污染调用方传进来的原切片元素。
func (m *HackerNews) aiTransfer(ctx context.Context, list []model.NotifyBase) ([]model.NotifyBase, error) {
	result := make([]model.NotifyBase, 0, len(list))
	for _, out := range list { // out 是值拷贝
		if !out.ContentTransferedByAIFlag {
			// 摘要失败的情况：直接走兜底模板，不调 AI。
			out.Content = formatHNMessage(
				out.Title,
				out.URL,
				tools.TruncateUTF8(out.Content, 2000),
				out.OriginURL,
			)
		} else {
			summary, err := m.ai.Summarize(ctx, out.Content)
			if err != nil {
				slog.ErrorContext(ctx, "HN AI 摘要失败", "err", err)
				summary = "AI摘要失败" // 失败兜底文案，照样推送
			}
			out.Content = formatHNMessage(out.Title, out.URL, summary, out.OriginURL)
		}
		result = append(result, out) // 把改完的拷贝放进结果切片
	}
	return result, nil
}

// formatHNMessage 与 Rust 旧版输出格式严格一致（包括空格和换行），便于对比迁移结果。
// 这是一个普通函数（不是方法，没有 receiver），可以包内任意位置调用。
// 模板：*<title>*: \n Comment Site:<url>\n\n AI总结: <summary>\n\n[源内容网页: ](<origin>)\n
func formatHNMessage(title, url, summary, origin string) string {
	return fmt.Sprintf(
		"*%s*: \n Comment Site:%s\n\n %s\n\n[%s](%s)\n",
		title,
		tools.EscapeMarkdownV2(url),
		"AI总结: "+tools.EscapeMarkdownV2(summary),
		"源内容网页: ",
		tools.EscapeMarkdownV2(origin),
	)
}

// 去重三件套：和 V2EX 同构，差别只在 cleanOldURLs 的窗口大小。

func (m *HackerNews) alreadyPushed(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.pushedURLs[id]
	return ok
}

func (m *HackerNews) markPushed(id string) {
	date := time.Now().Format("20060102")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushedURLs[id] = date
}

// HN 留 15 天（V2EX 只留 5 天），因为 HN 的 top 帖子在榜时间长，避免重复推。
func (m *HackerNews) cleanOldURLs(now time.Time) {
	cutoff := now.AddDate(0, 0, -15).Format("20060102")
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, date := range m.pushedURLs {
		if date < cutoff {
			delete(m.pushedURLs, id)
		}
	}
}

// judgeNewsDate 解析 HN 帖子页 HTML 的 "<n> hours ago" 文案。
// n > timeGap 才算"够老"，返回 true。
//
// 细节（和 Rust 版严格一致）：
//   - 只认 "hours" 单位；"minutes" / "days" 一律返回 false；
//   - 多个 span.age 时取第一个满足条件的，找不到就 false。
func judgeNewsDate(ctx context.Context, htmlBody string, timeGap int) bool {
	//now := time.Now()
	//fmt.Printf("%s 开始判断帖子日期是否在范围内\n", now.Format("2006年01月02日 15:04:05"))
	slog.InfoContext(ctx, "开始判断帖子日期是否在范围内")

	// goquery 从 io.Reader 解析；strings.NewReader 把字符串包成 Reader。
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		//fmt.Println("解析HN帖子HTML失败")
		slog.ErrorContext(ctx, "解析HN帖子HTML失败")
		return false
	}

	var lastTitleTime string
	matched := false
	// EachWithBreak：和 jQuery 的 each 类似，回调返回 false 中止迭代。
	// 第二个参数是闭包：`func(下标 int, 元素 *goquery.Selection) bool { ... }`
	doc.Find("tbody tr td.subtext span.age").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		// _ 表示丢弃这个参数（不需要下标）
		text := strings.TrimSpace(s.Text())
		// strings.Split("3 hours ago", " ") → ["3", "hours", "ago"]
		parts := strings.Split(text, " ")
		if len(parts) == 3 && strings.EqualFold(parts[1], "hours") {
			// strconv.Atoi: 字符串转 int，失败返回 err（这里直接忽略转失败的情况）
			if n, err := strconv.Atoi(parts[0]); err == nil && n > timeGap {
				matched = true
				return false // 闭包里修改外层变量 = 闭包捕获；返回 false 中止 each
			}
		}
		lastTitleTime = text
		return true // 继续下一次
	})

	if matched {
		return true
	}

	if lastTitleTime == "" {
		lastTitleTime = "帖子日期获取失败"
	}
	//fmt.Printf("帖子日期: %s 不符合推送要求\n", lastTitleTime)
	slog.InfoContext(ctx, "帖子日期不符合推送要求", "lastTitleTime", lastTitleTime)
	return false
}

// getNewsOriginURL 从 HN 帖子页提取原文链接。
// 返回 "" + nil 表示"没找到 / 格式异常"，调用方据此跳过。
func getNewsOriginURL(ctx context.Context, htmlBody string) (string, error) {
	//fmt.Printf("%s 开始解析源网址\n", time.Now().Format("2006年01月02日 15:04:05"))
	slog.InfoContext(ctx, "开始解析源网址")

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return "", err
	}

	// Find 返回的是匹配集合；First 取第一个
	sel := doc.Find("span.titleline a").First()
	if sel.Length() == 0 {
		//fmt.Println("span.titleline a 没找到对应内容")
		return "", errors.New("span.titleline a 没找到对应内容")
	}
	// Attr 返回"属性值, 是否存在"；ok==false 表示这个属性根本不存在
	href, ok := sel.Attr("href")
	if !ok {
		//fmt.Println("未找到源网址")
		return "", errors.New("未找到源网址")
	}
	//fmt.Printf("识别到的源网址为: %s\n", href)
	slog.InfoContext(ctx, "识别到的源网址为", "href", href)
	// 兜底：相对路径如 "item?id=..." 不算外链，跳过。
	if !strings.HasPrefix(href, "http") {
		//fmt.Println("识别到的源网址格式异常")
		return "", errors.New("识别到的源网址格式异常")
	}
	return href, nil
}

// takeN 是泛型函数：[T any] 表示 T 可以是任意类型。
// 返回切片前 n 个元素；n<=0 或 n>=len(s) 时返回原切片（不复制）。
//
// `s[:n]` 是切片操作（slicing）：得到一个新切片头，底层数组共享。
// 这意味着改 takeN 的返回值会影响原切片的同一块内存 —— 这里只是读，所以安全。
func takeN[T any](s []T, n int) []T {
	if n <= 0 || n >= len(s) {
		return s
	}
	return s[:n]
}
