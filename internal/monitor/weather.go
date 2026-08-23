// weather.go 实现「每日天气定点推送」monitor。
// 与 v2ex/hackernews 不同：不是 ticker 轮询，而是每天定点触发一次。
package monitor

import (
	"context"       // 上下文（取消、超时）
	"encoding/json" // JSON 解析
	"fmt"           // 拼字符串
	"io"            // 读响应体
	"log/slog"      // 结构化日志
	"net/http"      // HTTP 请求/响应
	"strconv"       // 字符串转数字
	"strings"       // 分割 "HH:MM"
	"time"          // 时间/定时

	_ "time/tzdata" // 把时区库嵌进二进制，保证 Windows/容器都能 LoadLocation

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/logx"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/tools"
)

// 中国天气网 cityinfo 接口基址；按城市编码拼 "{base}/{code}.html"。
// 非官方接口：只取「城市名 + 状况 + 高/低温」，字段少但足够当日预报。
const weatherCityInfoBase = "http://www.weather.com.cn/data/cityinfo"

// 伪装成浏览器 UA；该非官方接口对无头请求（无 UA）可能拒绝响应。
const weatherUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// Advisor 是本 monitor 依赖的「穿衣建议」能力（本地接口，便于测试注入 fake）。
// ai.DeepSeek 实现了 Advise，所以 *ai.DeepSeek 自动满足 Advisor。
type Advisor interface {
	Advise(ctx context.Context, weatherText string) (string, error)
}

// cityInfoResp 对应 cityinfo 接口的 JSON：{"weatherinfo":{...}}。
type cityInfoResp struct {
	WeatherInfo struct {
		City    string `json:"city"`
		Temp1   string `json:"temp1"` // 最高温
		Temp2   string `json:"temp2"` // 最低温
		Weather string `json:"weather"`
	} `json:"weatherinfo"`
}

// Weather 是「每日天气」monitor 实例。字段全私有，只能经 NewWeather 构造。
type Weather struct {
	httpClient *http.Client     // 共享连接池
	notifier   notify.Notifier  // 推送渠道
	advisor    Advisor          // 穿衣建议（可注入 fake）
	mentions   []config.Mention // 每日 @ 的人
	baseURL    string           // cityinfo 基址；测试时替换为 httptest server
}

// NewWeather 构造函数，注入依赖。baseURL 用生产常量，测试时字面量构造覆盖。
func NewWeather(httpClient *http.Client, notifier notify.Notifier, advisor Advisor, mentions []config.Mention) *Weather {
	return &Weather{
		httpClient: httpClient,
		notifier:   notifier,
		advisor:    advisor,
		mentions:   mentions,
		baseURL:    weatherCityInfoBase,
	}
}

// fetchCity 拉单个城市的当日天气。失败返回零值 + error，由上层决定跳过。
func (w *Weather) fetchCity(ctx context.Context, code string) (cityWeather, error) {
	url := fmt.Sprintf("%s/%s.html", w.baseURL, code)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return cityWeather{}, err
	}
	req.Header.Set("User-Agent", weatherUserAgent) // 无 UA 可能被该非官方接口拒绝

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return cityWeather{}, fmt.Errorf("network: %w", err)
	}
	defer resp.Body.Close()

	// 非 2xx 直接当失败（如 404）。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cityWeather{}, fmt.Errorf("bad status %d for code %s", resp.StatusCode, code)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return cityWeather{}, fmt.Errorf("read body: %w", err)
	}

	var r cityInfoResp
	if err := json.Unmarshal(body, &r); err != nil {
		return cityWeather{}, fmt.Errorf("parse json: %w", err)
	}
	// city 为空说明接口没给有效数据，视作失败。
	if r.WeatherInfo.City == "" {
		return cityWeather{}, fmt.Errorf("empty weatherinfo for code %s", code)
	}

	return cityWeather{
		Name:    r.WeatherInfo.City,
		Weather: r.WeatherInfo.Weather,
		High:    r.WeatherInfo.Temp1,
		Low:     r.WeatherInfo.Temp2,
	}, nil
}

// parsePushTime 解析 "HH:MM" 推送时间。
// 任何不合法（空、缺段、越界、非数字）都回落到 07:00 —— 配置写错也不至于不推。
func parsePushTime(s string) (hh, mm int) {
	// strings.Split 按 ":" 切；合法应恰好 2 段。
	parts := strings.Split(s, ":")
	if len(parts) == 2 {
		h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		// 两段都能解析且在合法范围内才采用。
		if err1 == nil && err2 == nil && h >= 0 && h <= 23 && m >= 0 && m <= 59 {
			return h, m
		}
	}
	return 7, 0 // 回落默认 07:00
}

// nextRun 计算「下一个 hh:mm」的绝对时间。
//   - 先构造今天的 hh:mm；
//   - 若该时刻不晚于 now（已过或正好相等），则推到明天。
//
// loc 指定时区（生产用 Asia/Shanghai，测试用 UTC）。
func nextRun(now time.Time, hh, mm int, loc *time.Location) time.Time {
	y, m, d := now.Date() // 拆出年月日
	next := time.Date(y, m, d, hh, mm, 0, 0, loc)
	// !After 覆盖"已过"和"正好相等"两种情况，都视作今天已错过 → 次日。
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// cityWeather 是单个城市抓取后的当日天气（已从接口 JSON 抽出）。
type cityWeather struct {
	Name    string // 城市名（接口返回的 city 字段）
	Weather string // 天气状况，如 "多云"
	High    string // 最高温 temp1，如 "33℃"
	Low     string // 最低温 temp2，如 "24℃"
}

// buildMessage 把多城市天气 + 穿衣建议 + @ 提及拼成一条预渲染 MarkdownV2 消息。
//
// 转义约定：结构标记（*、[]()、换行）由代码构造、不转义；
// 动态片段（城市名、状况、温度、建议、显示名）逐个 EscapeMarkdownV2。
// 注意温度里的 '~' 是 MarkdownV2 特殊字符，所以把 "low~high" 整体转义（'~'→'\~'）。
func buildMessage(date string, items []cityWeather, advice string, mentions []config.Mention) string {
	var b strings.Builder

	// 标题行：🌤️ *每日天气 · 2026-07-19*
	b.WriteString("🌤️ *每日天气 · ")
	b.WriteString(tools.EscapeMarkdownV2(date))
	b.WriteString("*\n\n")

	// 城市明细：每城市一行「*城市*  状况  低~高」。
	for _, it := range items {
		it := it // 遵循 Go 1.22 循环变量语义的防御性拷贝
		b.WriteString("*")
		b.WriteString(tools.EscapeMarkdownV2(it.Name))
		b.WriteString("*  ")
		b.WriteString(tools.EscapeMarkdownV2(it.Weather))
		b.WriteString("  ")
		b.WriteString(tools.EscapeMarkdownV2(it.Low + "~" + it.High))
		b.WriteString("\n")
	}

	// 穿衣建议段（仅在有内容时）。
	if advice != "" {
		b.WriteString("\n👕 *穿衣建议*\n")
		b.WriteString(tools.EscapeMarkdownV2(advice))
		b.WriteString("\n")
	}

	// @ 提及段（仅在有提及时）：提醒：[老王](tg://user?id=234567) ...
	if len(mentions) > 0 {
		b.WriteString("\n提醒：")
		for i, m := range mentions {
			m := m // 遵循 Go 1.22 循环变量语义的防御性拷贝
			if i > 0 {
				b.WriteString(" ")
			}
			// 链接文字转义；tg://user?id=<数字> 里 id 是纯数字，URL 部分无需转义。
			b.WriteString(fmt.Sprintf("[%s](tg://user?id=%d)", tools.EscapeMarkdownV2(m.Name), m.ID))
		}
	}

	return b.String()
}

// pushOnce 执行一次完整推送：抓所有城市 → 生成建议 → 拼消息 → 发出。
// 单城市抓取失败只跳过；全部失败则不推送（返回 nil，等下一天）。
// date 由调用方（Run）按东八区算好传入，本函数不再自行取 time.Now，便于测试且避免时区错位。
func (w *Weather) pushOnce(ctx context.Context, cfg *config.Config, date string) error {
	var items []cityWeather
	for _, code := range cfg.Features.WeatherCities {
		cw, err := w.fetchCity(ctx, code)
		if err != nil {
			slog.ErrorContext(ctx, "天气抓取失败，跳过该城市", "code", code, "err", err)
			continue
		}
		items = append(items, cw)
	}
	if len(items) == 0 {
		slog.ErrorContext(ctx, "天气全部城市抓取失败，跳过本轮推送")
		return nil
	}

	// 给 LLM 的输入：每城市一行「城市 状况 低~高」。
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(fmt.Sprintf("%s %s %s~%s\n", it.Name, it.Weather, it.Low, it.High))
	}
	// 建议失败已在 Advise 内兜底（返回兜底串 + nil），这里 err 恒为 nil，忽略即可。
	advice, _ := w.advisor.Advise(ctx, sb.String())

	msg := buildMessage(date, items, advice, w.mentions)

	// 单条预渲染 MarkdownV2，正对应 NotifyMarkdown 的语义。
	return w.notifier.NotifyMarkdown(ctx, msg)
}

// Run 实现 monitor.Monitor：每天定点推送一次天气。
// 与 ticker 型 monitor 不同 —— 每轮算「下一个推送时刻」用 Timer 睡到点。
func (w *Weather) Run(ctx context.Context, cfg *config.Config) error {
	// 未启用：直接退出，不占用 goroutine 也不影响别的 monitor。
	if !cfg.Features.WeatherEnabled {
		slog.Info("天气推送未启用，weather monitor 退出")
		return nil
	}

	// 固定东八区；tzdata 已嵌入，LoadLocation 不依赖系统时区库。
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return fmt.Errorf("加载时区失败: %w", err)
	}
	hh, mm := parsePushTime(cfg.Features.WeatherPushTime)
	slog.Info("天气推送已启用", "push_time", fmt.Sprintf("%02d:%02d", hh, mm), "cities", cfg.Features.WeatherCities)

	for {
		// 算到下一个推送时刻的等待时长。
		next := nextRun(time.Now().In(loc), hh, mm, loc)
		timer := time.NewTimer(time.Until(next))

		select {
		case <-ctx.Done():
			timer.Stop() // 及时释放 timer
			return ctx.Err()
		case <-timer.C:
			// 到点了，往下执行本轮推送。
		}

		// 每个推送周期一个 task_id，链路日志用 *Context 变体。
		cctx := logx.WithTaskID(ctx, logx.NewTaskID())
		// 必须按 loc（东八区）取日期：推送时刻是 07:00 CST = 前一日 23:00 UTC，
		// 若用机器本地时间（容器多为 UTC），标题日期会显示成前一天。
		date := time.Now().In(loc).Format("2006-01-02")
		if err := w.pushOnce(cctx, cfg, date); err != nil {
			// pushOnce 里的 NotifyMarkdown 在 ctx 取消时会返回 ctx.Err() —— 那属于正常关闭。
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.ErrorContext(cctx, "天气推送失败", "err", err)
		}
	}
}
