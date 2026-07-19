// weather.go 实现「每日天气定点推送」monitor。
// 与 v2ex/hackernews 不同：不是 ticker 轮询，而是每天定点触发一次。
package monitor

import (
	"fmt"     // 拼字符串
	"strconv" // 字符串转数字
	"strings" // 分割 "HH:MM"
	"time"    // 时间/定时

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/tools"
)

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
