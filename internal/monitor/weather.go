// weather.go 实现「每日天气定点推送」monitor。
// 与 v2ex/hackernews 不同：不是 ticker 轮询，而是每天定点触发一次。
package monitor

import (
	"strconv" // 字符串转数字
	"strings" // 分割 "HH:MM"
	"time"    // 时间/定时
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
