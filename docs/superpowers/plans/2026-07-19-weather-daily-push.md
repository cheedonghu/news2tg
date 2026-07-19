# 每日天气定点推送 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 每天定点（默认 07:00）把配置城市的当日天气 + DeepSeek 穿衣建议推送到 Telegram 频道，并 @ 配置的提及列表。

**Architecture:** 新增 `monitor.Weather`，实现现有 `monitor.Monitor` 接口，作为第四个 monitor 在 `main` 注册。调度不用 ticker，而是每轮算「下一个推送时刻」用 `time.Timer` 睡到点。天气取自中国天气网免费 JSON 接口；穿衣建议复用 `ai.DeepSeek`（新增 `Advise` 方法），经本地 `Advisor` 接口注入以便单测。

**Tech Stack:** Go 1.22、`net/http`、`encoding/json`、`time`（`time/tzdata` 嵌入时区）、`BurntSushi/toml`、`go-telegram-bot-api`、`go-openai`（DeepSeek）。

## Global Constraints

- **Go ≥ 1.22**（沿用现有 `go.mod`；依赖 1.22 loop-variable 语义，测试中仍防御性 `c := c`）。
- **module 路径**：`github.com/cheedonghu/news2tg`。
- **双语教学注释**：每个新函数/关键行配中文注释，匹配现有文件风格与密度，不要精简。
- **AI/抓取失败不阻断推送**：`Advise` 失败返回兜底字符串 + `nil`；单城市抓取失败只 log + 跳过；全部失败才跳过本轮。返回 `ctx.Err()` 表示正常关闭。
- **转义约定**：`NotifyBatch` 发**预渲染 MarkdownV2**（不整体转义）；动态片段（城市名、状况、温度、建议、@ 显示名）用 `tools.EscapeMarkdownV2` 转义；`*bold*`、`[..](..)` 结构标记由代码构造、不转义。
- **task_id**：每个推送周期入口 `logx.WithTaskID(ctx, logx.NewTaskID())`，链路日志用 `slog.*Context`。
- **测试风格**：表驱动、`package monitor` / `package config`（白盒，可访问私有符号），`c := c` shadow。
- **提交信息尾行**：`Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`（提交正文用项目风格前缀如 `#feat` / `#test`）。

---

## File Structure

- `internal/config/config.go`（改）— `Features` 加 4 字段；新增 `Mention` 类型 + `ParseMention` 纯函数。
- `internal/config/config_test.go`（新）— `ParseMention` 表驱动测试。
- `internal/ai/deepseek.go`（改）— 新增 `Advise` 方法（穿衣建议 prompt）。
- `internal/monitor/weather.go`（新）— `Weather` monitor：纯函数 `parsePushTime`/`nextRun`/`buildMessage`，`cityWeather`/`cityInfoResp` DTO，`Advisor` 接口，`fetchCity`/`pushOnce`/`Run`。
- `internal/monitor/weather_test.go`（新）— 纯函数 + `fetchCity`（httptest）+ `pushOnce`（fake 注入）测试。
- `cmd/news2tg/main.go`（改）— 解析 `weather_mention` → `[]config.Mention`，构造并注册 weather monitor。
- `config.toml`（改）— 天气配置模板示例。

---

## Task 1: config — Mention 类型 + ParseMention + Features 字段

**Files:**
- Modify: `internal/config/config.go`（`Features` 结构体，约 47-56 行；import 块 4-10 行）
- Test: `internal/config/config_test.go`（新建）

**Interfaces:**
- Produces:
  - `type Mention struct { ID int64; Name string }`
  - `func ParseMention(s string) (Mention, error)` — `"123"`→`{123,"管理员"}`；`"123:老王"`→`{123,"老王"}`；id 非法→`error`。
  - `Features` 新增字段：`WeatherEnabled bool`、`WeatherPushTime string`、`WeatherCities []string`、`WeatherMention []string`。

- [ ] **Step 1: 写失败测试**

创建 `internal/config/config_test.go`：

```go
package config

import "testing"

func TestParseMention(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantID  int64
		wantN   string
		wantErr bool
	}{
		{"纯 id，显示名回落管理员", "123456", 123456, "管理员", false},
		{"id:显示名", "234567:老王", 234567, "老王", false},
		{"显示名带空格前后被 trim", " 345 : 小李 ", 345, "小李", false},
		{"冒号后为空 → 回落管理员", "456:", 456, "管理员", false},
		{"非法 id → 报错", "abc", 0, "", true},
		{"空串 → 报错", "", 0, "", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseMention(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseMention(%q) 期望报错，却成功返回 %+v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMention(%q) 意外报错: %v", c.in, err)
			}
			if got.ID != c.wantID || got.Name != c.wantN {
				t.Fatalf("ParseMention(%q) = {%d,%q}, want {%d,%q}", c.in, got.ID, got.Name, c.wantID, c.wantN)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/config -run TestParseMention -v`
Expected: 编译失败 —— `undefined: ParseMention` / `Mention`。

- [ ] **Step 3: 加 Mention 类型与 ParseMention**

在 `internal/config/config.go` 的 import 块补充（当前只有 `flag` 与 toml）：

```go
import (
	"flag"
	"fmt"     // 新增：错误包装
	"strconv" // 新增：字符串转 int64
	"strings" // 新增：按冒号分割 / TrimSpace

	"github.com/BurntSushi/toml"
)
```

在文件末尾追加：

```go
// Mention 是「每日天气要 @ 的人」的解析结果。
// 它是 weather_mention 配置（[]string）的派生形态：raw 是 "id" 或 "id:显示名"，
// 解析成结构化的 ID + 显示名。放 config 包是因为它属于配置派生数据，
// 且 monitor 已经 import config，直接用不产生导入环。
type Mention struct {
	ID   int64  // Telegram 用户数字 id
	Name string // @ 时显示的文字；缺省回落 "管理员"
}

// ParseMention 把一条 weather_mention 配置解析成 Mention。
//   - "123456"       → {123456, "管理员"}
//   - "234567:老王"  → {234567, "老王"}
// 规则：按第一个 ':' 分割；冒号后为空 / 无冒号 → 显示名回落 "管理员"；id 非法返回 error。
func ParseMention(s string) (Mention, error) {
	idPart := s     // 冒号左边（或整串）当 id
	name := "管理员" // 默认显示名
	// strings.Index 找第一个 ':' 的下标，找不到返回 -1。
	if i := strings.Index(s, ":"); i >= 0 {
		idPart = s[:i]
		// TrimSpace 去掉两端空白；非空才覆盖默认显示名。
		if n := strings.TrimSpace(s[i+1:]); n != "" {
			name = n
		}
	}
	// ParseInt(串, 进制, 位宽)：解析失败返回 error，交给调用方 log 跳过。
	id, err := strconv.ParseInt(strings.TrimSpace(idPart), 10, 64)
	if err != nil {
		return Mention{}, fmt.Errorf("非法 mention %q: %w", s, err)
	}
	return Mention{ID: id, Name: name}, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/config -run TestParseMention -v`
Expected: PASS（6 个子测试全绿）。

- [ ] **Step 5: 给 Features 加 4 个字段**

在 `internal/config/config.go` 的 `Features` 结构体末尾（`HnFetchTimeGap` 之后、结构体右括号之前）追加：

```go
	// —— 每日天气推送 ——
	WeatherEnabled  bool     `toml:"weather_enabled"`   // 总开关；false 时 weather monitor 直接退出
	WeatherPushTime string   `toml:"weather_push_time"` // "HH:MM"，空/非法回落 "07:00"
	WeatherCities   []string `toml:"weather_cities"`    // 城市编码列表，如 "101010100"=北京
	WeatherMention  []string `toml:"weather_mention"`   // @ 提及列表，"id" 或 "id:显示名"；与 admin_ids 无关
```

- [ ] **Step 6: 编译确认**

Run: `go build ./... && go vet ./internal/config`
Expected: 无输出（成功）。

- [ ] **Step 7: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
#feat config 增加 Mention 类型与天气相关配置字段

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: ai.DeepSeek 增加 Advise 方法

**Files:**
- Modify: `internal/ai/deepseek.go`（在 `Summarize` 方法之后追加）

**Interfaces:**
- Consumes: 现有 `DeepSeek` 结构体的 `d.client`（`*openai.Client`）。
- Produces: `func (d *DeepSeek) Advise(ctx context.Context, weatherText string) (string, error)` — 输入多城市天气文本，返回中文穿衣建议；失败返回兜底串 `"穿衣建议获取失败"` + `nil`（沿用项目约定）。

> 说明：`DeepSeek` 直连真实 API，项目现有约定是不对它写联网单测（参见 `Summarize` 无单测）。本方法同理，靠 Task 6 的 `Advisor` fake 覆盖调用路径；本任务以编译 + vet 为验收。

- [ ] **Step 1: 追加 Advise 方法**

在 `internal/ai/deepseek.go` 末尾（`Summarize` 方法之后）追加。无需新增 import（`context`/`fmt`/`slog`/`openai` 均已在用）：

```go
// Advise 根据多城市天气文本，用 DeepSeek 生成中文穿衣建议。
//
// 与 Summarize 一样是"失败兜底不抛错"：任何异常都返回固定文案 + nil，
// 让天气推送照常发出去（AI 只是锦上添花，不能阻断主流程）。
// 一次调用处理所有城市：把各城市天气拼进 prompt，让模型按城市各给一句。
func (d *DeepSeek) Advise(ctx context.Context, weatherText string) (string, error) {
	slog.InfoContext(ctx, "利用大模型生成穿衣建议")

	// prompt 约束输出格式：每城市一行「城市：建议」，避免模型发挥太长。
	prompt := fmt.Sprintf(
		"下面是今天几个城市的天气，请用中文为每个城市各写一句简短的穿衣建议，"+
			"每个城市一行，格式「城市：建议」，不要多余解释：\n%s",
		weatherText,
	)

	resp, err := d.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: "deepseek-v4-flash", // 与 Summarize 同款轻量模型
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	})
	if err != nil {
		slog.ErrorContext(ctx, "穿衣建议大模型返回异常", "err", err)
		return "穿衣建议获取失败", nil // 兜底，不抛错
	}
	if len(resp.Choices) == 0 {
		return "穿衣建议获取失败", nil
	}
	return resp.Choices[0].Message.Content, nil
}
```

- [ ] **Step 2: 编译 + vet 确认**

Run: `go build ./... && go vet ./internal/ai`
Expected: 无输出（成功）。

- [ ] **Step 3: 提交**

```bash
git add internal/ai/deepseek.go
git commit -m "$(cat <<'EOF'
#feat deepseek 增加穿衣建议 Advise 方法

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: weather.go 纯函数 parsePushTime + nextRun

**Files:**
- Create: `internal/monitor/weather.go`
- Test: `internal/monitor/weather_test.go`（新建）

**Interfaces:**
- Produces:
  - `func parsePushTime(s string) (hh, mm int)` — 解析 "HH:MM"，非法/越界回落 `7, 0`。
  - `func nextRun(now time.Time, hh, mm int, loc *time.Location) time.Time` — 返回今天 hh:mm；若该时刻 `!After(now)`（已过或正好相等）则加一天。

- [ ] **Step 1: 写失败测试**

创建 `internal/monitor/weather_test.go`：

```go
package monitor

import (
	"testing"
	"time"
)

func TestParsePushTime(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantHH int
		wantMM int
	}{
		{"正常 07:00", "07:00", 7, 0},
		{"正常 23:59", "23:59", 23, 59},
		{"空串回落 07:00", "", 7, 0},
		{"缺分钟回落", "7", 7, 0},
		{"小时越界回落", "25:00", 7, 0},
		{"分钟越界回落", "08:70", 7, 0},
		{"非数字回落", "aa:bb", 7, 0},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			hh, mm := parsePushTime(c.in)
			if hh != c.wantHH || mm != c.wantMM {
				t.Fatalf("parsePushTime(%q) = %d:%d, want %d:%d", c.in, hh, mm, c.wantHH, c.wantMM)
			}
		})
	}
}

func TestNextRun(t *testing.T) {
	loc := time.UTC // 测试用 UTC，避免依赖机器时区
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "当天时刻未到 → 今天",
			now:  time.Date(2026, 7, 19, 6, 0, 0, 0, loc),
			want: time.Date(2026, 7, 19, 7, 0, 0, 0, loc),
		},
		{
			name: "当天时刻已过 → 次日",
			now:  time.Date(2026, 7, 19, 8, 0, 0, 0, loc),
			want: time.Date(2026, 7, 20, 7, 0, 0, 0, loc),
		},
		{
			name: "正好等于该时刻 → 次日（!After 为真）",
			now:  time.Date(2026, 7, 19, 7, 0, 0, 0, loc),
			want: time.Date(2026, 7, 20, 7, 0, 0, 0, loc),
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := nextRun(c.now, 7, 0, loc)
			if !got.Equal(c.want) {
				t.Fatalf("nextRun(%v) = %v, want %v", c.now, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/monitor -run 'TestParsePushTime|TestNextRun' -v`
Expected: 编译失败 —— `undefined: parsePushTime` / `nextRun`。

- [ ] **Step 3: 写最小实现**

创建 `internal/monitor/weather.go`：

```go
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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/monitor -run 'TestParsePushTime|TestNextRun' -v`
Expected: PASS（全部子测试绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/monitor/weather.go internal/monitor/weather_test.go
git commit -m "$(cat <<'EOF'
#feat weather 定点调度纯函数 parsePushTime/nextRun

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: buildMessage 消息拼装（纯函数）

**Files:**
- Modify: `internal/monitor/weather.go`（追加类型与函数、补 import）
- Test: `internal/monitor/weather_test.go`（追加测试）

**Interfaces:**
- Consumes: `config.Mention`（Task 1）、`tools.EscapeMarkdownV2`。
- Produces:
  - `type cityWeather struct { Name, Weather, High, Low string }`
  - `func buildMessage(date string, items []cityWeather, advice string, mentions []config.Mention) string` — 拼一条预渲染 MarkdownV2 消息。`advice==""` 时省略穿衣建议段；`mentions` 为空时省略提醒段。

- [ ] **Step 1: 写失败测试**

在 `internal/monitor/weather_test.go` 追加（并把顶部 import 补上 `strings` 与 config）：

```go
// 顶部 import 块改为：
// import (
// 	"strings"
// 	"testing"
// 	"time"
//
// 	"github.com/cheedonghu/news2tg/internal/config"
// )

func TestBuildMessage(t *testing.T) {
	items := []cityWeather{
		{Name: "北京", Weather: "多云", High: "33℃", Low: "24℃"},
		{Name: "广州", Weather: "雷阵雨", High: "34℃", Low: "27℃"},
	}
	mentions := []config.Mention{{ID: 234567, Name: "老王"}}

	t.Run("完整：城市/建议/提及都在", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "北京：加件外套", mentions)
		wants := []string{
			"每日天气",                          // 标题
			"*北京*",                          // 城市加粗
			"多云",                            // 状况
			`24℃\~33℃`,                     // 温度：low~high，~ 被转义为 \~
			"*广州*",
			"👕",                            // 穿衣建议段标记
			"北京：加件外套",                       // 建议正文
			"[老王](tg://user?id=234567)",    // @ 提及链接
		}
		for _, w := range wants {
			if !strings.Contains(msg, w) {
				t.Fatalf("消息缺少 %q\n完整消息:\n%s", w, msg)
			}
		}
	})

	t.Run("无建议：省略穿衣建议段", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "", mentions)
		if strings.Contains(msg, "👕") {
			t.Fatalf("advice 为空时不应出现穿衣建议段:\n%s", msg)
		}
	})

	t.Run("无提及：省略提醒段", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "建议", nil)
		if strings.Contains(msg, "tg://user?id=") {
			t.Fatalf("mentions 为空时不应出现提及链接:\n%s", msg)
		}
	})
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/monitor -run TestBuildMessage -v`
Expected: 编译失败 —— `undefined: cityWeather` / `buildMessage`。

- [ ] **Step 3: 写实现**

在 `internal/monitor/weather.go` 中：把 import 块改为（新增 `fmt`、config、tools）：

```go
import (
	"fmt"     // 拼字符串
	"strconv"
	"strings"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/tools"
)
```

追加类型与函数：

```go
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
			if i > 0 {
				b.WriteString(" ")
			}
			// 链接文字转义；tg://user?id=<数字> 里 id 是纯数字，URL 部分无需转义。
			b.WriteString(fmt.Sprintf("[%s](tg://user?id=%d)", tools.EscapeMarkdownV2(m.Name), m.ID))
		}
	}

	return b.String()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/monitor -run TestBuildMessage -v`
Expected: PASS（3 个子测试绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/monitor/weather.go internal/monitor/weather_test.go
git commit -m "$(cat <<'EOF'
#feat weather 消息拼装 buildMessage（含 @ 提及/穿衣建议段）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Weather 结构体 + Advisor 接口 + fetchCity（抓取）

**Files:**
- Modify: `internal/monitor/weather.go`（追加类型、构造函数、抓取方法、补 import）
- Test: `internal/monitor/weather_test.go`（追加 httptest 测试）

**Interfaces:**
- Consumes: `notify.Notifier`、`config.Mention`、共享 `*http.Client`。
- Produces:
  - `type Advisor interface { Advise(ctx context.Context, weatherText string) (string, error) }`
  - `type Weather struct{ ... }`（私有字段 `httpClient`、`notifier`、`advisor`、`mentions`、`baseURL`）
  - `func NewWeather(httpClient *http.Client, notifier notify.Notifier, advisor Advisor, mentions []config.Mention) *Weather`
  - `func (w *Weather) fetchCity(ctx context.Context, code string) (cityWeather, error)` — GET `{baseURL}/{code}.html`，解析 cityinfo JSON；`city` 为空视作失败。

- [ ] **Step 1: 写失败测试**

在 `internal/monitor/weather_test.go` 追加（顶部 import 再补 `context`、`net/http`、`net/http/httptest`）：

```go
// 顶部 import 补齐为：
// import (
// 	"context"
// 	"net/http"
// 	"net/http/httptest"
// 	"strings"
// 	"testing"
// 	"time"
//
// 	"github.com/cheedonghu/news2tg/internal/config"
// )

func TestFetchCity(t *testing.T) {
	// 模拟中国天气网 cityinfo 接口返回。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/101010100.html") {
			w.Write([]byte(`{"weatherinfo":{"city":"北京","cityid":"101010100","temp1":"33℃","temp2":"24℃","weather":"多云"}}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	// 直接用字面量构造，注入测试 baseURL 与 httptest client。
	wm := &Weather{httpClient: srv.Client(), baseURL: srv.URL}

	t.Run("正常解析", func(t *testing.T) {
		got, err := wm.fetchCity(context.Background(), "101010100")
		if err != nil {
			t.Fatalf("fetchCity 意外报错: %v", err)
		}
		if got.Name != "北京" || got.Weather != "多云" || got.High != "33℃" || got.Low != "24℃" {
			t.Fatalf("解析结果不对: %+v", got)
		}
	})

	t.Run("404 → 报错", func(t *testing.T) {
		if _, err := wm.fetchCity(context.Background(), "999999999"); err == nil {
			t.Fatalf("非 200 响应应报错")
		}
	})
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/monitor -run TestFetchCity -v`
Expected: 编译失败 —— `undefined: Weather` / 字段 `baseURL` / 方法 `fetchCity`。

- [ ] **Step 3: 写实现**

在 `internal/monitor/weather.go` 中：import 块补充为（新增 `context`、`encoding/json`、`io`、`net/http`、`notify`）：

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/tools"
)
```

追加常量、类型、构造函数、抓取方法（放在 `parsePushTime` 之前的位置，风格与 v2ex.go 一致）：

```go
// 中国天气网 cityinfo 接口基址；按城市编码拼 "{base}/{code}.html"。
// 非官方接口：只取「城市名 + 状况 + 高/低温」，字段少但足够当日预报。
const weatherCityInfoBase = "http://www.weather.com.cn/data/cityinfo"

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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/monitor -run TestFetchCity -v`
Expected: PASS（2 个子测试绿）。

- [ ] **Step 5: 全包测试确认没弄坏别的**

Run: `go test ./internal/monitor`
Expected: `ok`（含 v2ex 既有测试）。

- [ ] **Step 6: 提交**

```bash
git add internal/monitor/weather.go internal/monitor/weather_test.go
git commit -m "$(cat <<'EOF'
#feat weather Weather 结构体/Advisor 接口/城市抓取 fetchCity

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: pushOnce 编排（抓取→建议→拼装→推送）

**Files:**
- Modify: `internal/monitor/weather.go`（追加 `pushOnce` 方法、补 `log/slog` import）
- Test: `internal/monitor/weather_test.go`（追加 fake 注入测试）

**Interfaces:**
- Consumes: `fetchCity`、`buildMessage`、`Advisor.Advise`、`notify.Notifier.NotifyBatch`、`cfg.Features.WeatherCities`。
- Produces: `func (w *Weather) pushOnce(ctx context.Context, cfg *config.Config) error` — 抓所有城市（失败跳过），全失败则不推送返回 nil；否则调 advisor、拼消息、`NotifyBatch` 单条发出。

- [ ] **Step 1: 写失败测试**

在 `internal/monitor/weather_test.go` 追加（fake 定义 + 测试）：

```go
// —— 测试用 fake ——

type fakeNotifier struct{ batch []string } // 捕获 NotifyBatch 的入参
func (f *fakeNotifier) Notify(ctx context.Context, content string) error              { return nil }
func (f *fakeNotifier) NotifyTo(ctx context.Context, chatID int64, content string) error { return nil }
func (f *fakeNotifier) NotifyBatch(ctx context.Context, contents []string) error {
	f.batch = contents
	return nil
}

type fakeAdvisor struct{ out string } // 固定返回建议文本
func (f fakeAdvisor) Advise(ctx context.Context, weatherText string) (string, error) {
	return f.out, nil
}

func TestPushOnce(t *testing.T) {
	// 服务器：101010100 正常，888888888 返回 500（模拟单城市失败）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/101010100.html") {
			w.Write([]byte(`{"weatherinfo":{"city":"北京","temp1":"33℃","temp2":"24℃","weather":"多云"}}`))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Run("部分城市失败仍推送好的那个 + 带建议", func(t *testing.T) {
		fn := &fakeNotifier{}
		wm := &Weather{
			httpClient: srv.Client(),
			notifier:   fn,
			advisor:    fakeAdvisor{out: "北京：多穿点"},
			baseURL:    srv.URL,
		}
		cfg := &config.Config{Features: config.Features{
			WeatherCities: []string{"101010100", "888888888"}, // 后者会失败
		}}

		if err := wm.pushOnce(context.Background(), cfg); err != nil {
			t.Fatalf("pushOnce 意外报错: %v", err)
		}
		if len(fn.batch) != 1 {
			t.Fatalf("应恰好推送 1 条，实际 %d 条", len(fn.batch))
		}
		msg := fn.batch[0]
		if !strings.Contains(msg, "*北京*") || !strings.Contains(msg, "北京：多穿点") {
			t.Fatalf("消息内容缺失:\n%s", msg)
		}
	})

	t.Run("全部城市失败 → 不推送", func(t *testing.T) {
		fn := &fakeNotifier{}
		wm := &Weather{
			httpClient: srv.Client(),
			notifier:   fn,
			advisor:    fakeAdvisor{out: "x"},
			baseURL:    srv.URL,
		}
		cfg := &config.Config{Features: config.Features{
			WeatherCities: []string{"888888888"}, // 唯一城市失败
		}}

		if err := wm.pushOnce(context.Background(), cfg); err != nil {
			t.Fatalf("pushOnce 意外报错: %v", err)
		}
		if len(fn.batch) != 0 {
			t.Fatalf("全失败时不应推送，实际推了 %d 条", len(fn.batch))
		}
	})
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/monitor -run TestPushOnce -v`
Expected: 编译失败 —— `wm.pushOnce undefined`。

- [ ] **Step 3: 写实现**

在 `internal/monitor/weather.go` 的 import 块新增 `"log/slog"`：

```go
	"io"
	"log/slog" // 新增
	"net/http"
```

追加方法：

```go
// pushOnce 执行一次完整推送：抓所有城市 → 生成建议 → 拼消息 → 发出。
// 单城市抓取失败只跳过；全部失败则不推送（返回 nil，等下一天）。
func (w *Weather) pushOnce(ctx context.Context, cfg *config.Config) error {
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

	date := time.Now().Format("2006-01-02")
	msg := buildMessage(date, items, advice, w.mentions)

	// 单条也走 NotifyBatch：它发预渲染 MarkdownV2、不整体转义，正合适。
	return w.notifier.NotifyBatch(ctx, []string{msg})
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/monitor -run TestPushOnce -v`
Expected: PASS（2 个子测试绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/monitor/weather.go internal/monitor/weather_test.go
git commit -m "$(cat <<'EOF'
#feat weather pushOnce 编排一次天气推送（含降级路径）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Run 定点调度循环

**Files:**
- Modify: `internal/monitor/weather.go`（追加 `Run` 方法、补 `logx` import 与 `_ "time/tzdata"`）

**Interfaces:**
- Consumes: `parsePushTime`、`nextRun`、`pushOnce`、`logx.WithTaskID`/`logx.NewTaskID`。
- Produces: `func (w *Weather) Run(ctx context.Context, cfg *config.Config) error` — 满足 `monitor.Monitor`。`WeatherEnabled=false` 直接返回 nil；否则每天定点调 `pushOnce`；ctx 取消返回 `ctx.Err()`。

> 说明：`Run` 是含无限定时循环的 IO 编排，不写单测（同 v2ex/hackernews 的 `Run` 无单测）；调度正确性已由 `nextRun`/`parsePushTime` 单测覆盖，本任务以编译 + vet 验收。

- [ ] **Step 1: 写实现**

在 `internal/monitor/weather.go` 的 import 块新增两行：

```go
	"strings"
	"time"

	_ "time/tzdata" // 新增：把时区库嵌进二进制，保证 Windows/容器都能 LoadLocation

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/logx" // 新增：task_id 关联
	"github.com/cheedonghu/news2tg/internal/notify"
```

追加方法（放文件末尾）：

```go
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
		if err := w.pushOnce(cctx, cfg); err != nil {
			// pushOnce 里的 NotifyBatch 在 ctx 取消时会返回 ctx.Err() —— 那属于正常关闭。
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.ErrorContext(cctx, "天气推送失败", "err", err)
		}
	}
}
```

- [ ] **Step 2: 编译 + vet 确认**

Run: `go build ./... && go vet ./internal/monitor`
Expected: 无输出（成功）。

- [ ] **Step 3: 全包测试确认没弄坏别的**

Run: `go test ./internal/monitor`
Expected: `ok`。

- [ ] **Step 4: 提交**

```bash
git add internal/monitor/weather.go
git commit -m "$(cat <<'EOF'
#feat weather Run 定点调度循环（东八区/可禁用）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: 接入 main + 配置模板

**Files:**
- Modify: `cmd/news2tg/main.go`（解析 mention 块 118-126 行附近；monitors 表 137-144 行）
- Modify: `config.toml`（`[features]` 段追加天气配置）

**Interfaces:**
- Consumes: `config.ParseMention`、`monitor.NewWeather`、现有 `aiClient`（`*ai.DeepSeek`，已实现 `Advise` → 满足 `monitor.Advisor`）、`tgClient`、`httpClient`。
- Produces: 运行时多一个 `"weather"` monitor。

- [ ] **Step 1: main 解析 weather_mention**

在 `cmd/news2tg/main.go` 的 admin_ids 解析块（`adminIDs := make(...)` 那段，约 118-126 行）**之后**插入：

```go
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
```

- [ ] **Step 2: main 构造 weather monitor 并注册**

紧接 Step 1 的 `mentions` 解析块**之后**加（必须在 `mentions` 定义之后，否则编译报 `mentions` 未定义）：

```go
	// aiClient 已实现 Advise，天然满足 monitor.Advisor。
	weatherMon := monitor.NewWeather(httpClient, tgClient, aiClient, mentions)
```

在 monitors 表里（约 141-143 行）追加一行：

```go
		{"hackernews", hnMon},
		{"v2ex", v2exMon},
		{"weather", weatherMon}, // 新增：每日天气定点推送
		{"command-bot", cmdBot},
```

- [ ] **Step 3: 编译 + vet 确认**

Run: `go build ./... && go vet ./...`
Expected: 无输出（成功）。

- [ ] **Step 4: config.toml 追加天气配置**

在 `config.toml` 的 `[features]` 段末尾（`hn_fetch_time_gap = 1` 那行之后）追加：

```toml
# —— 每日天气推送 ——
# 总开关
weather_enabled = false
# 推送时间 HH:MM（空或非法回落 07:00）
weather_push_time = "07:00"
# 城市编码列表（101010100=北京 101280101=广州；编码可在 weather.com.cn 城市页 URL 里找）
weather_cities = ["101010100"]
# 每日天气要 @ 的人，"id" 或 "id:显示名"；与 admin_ids 无关
weather_mention = ["123456789:管理员"]
```

> 本地 `myconfig.toml` 是你的私有配置（不提交）。若你用它跑，手动把上面 4 个键也加进它的 `[features]` 段并填真实值；本任务只提交模板 `config.toml`。

- [ ] **Step 5: 全量测试**

Run: `go test ./... && gofmt -l internal/ cmd/`
Expected: 测试全 `ok`；`gofmt -l` 无输出（无未格式化文件）。若 `gofmt -l` 列出文件，运行 `gofmt -w <文件>` 后重跑。

- [ ] **Step 6: 提交**

```bash
git add cmd/news2tg/main.go config.toml
git commit -m "$(cat <<'EOF'
#feat 接入每日天气 monitor 并补充配置模板

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## 收尾验证（全部任务完成后）

- [ ] `go build ./...` 成功
- [ ] `go vet ./...` 无告警
- [ ] `go test ./...` 全绿
- [ ] `gofmt -l internal/ cmd/` 无输出
- [ ] 人工冒烟（可选）：临时把 `config.toml` 的 `weather_enabled=true`、`weather_push_time` 设成「当前时间 +1 分钟」、填真实 `chat_id`/`api_token`/一个城市编码，跑 `./bin/news2tg -c config.toml`，确认到点收到一条天气消息；验证完把开关改回 `false`、还原时间，勿提交真实密钥。
