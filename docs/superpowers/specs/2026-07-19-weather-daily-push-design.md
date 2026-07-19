# 每日天气定点推送 · 设计

日期：2026-07-19
状态：已评审，待实现

## 目标

新增一个特性：每天早上 07:00（可配置）定点，把配置的城市当日天气推送到 Telegram 频道。
天气取自中国天气网（weather.com.cn）免费接口，另由 DeepSeek 生成中文穿衣建议。
多城市合并为一条消息发送。推送时同时 @（提及）配置的管理员（`admin_ids`）。

## 范围与非目标

- **做**：当日天气（天气状况 + 最高/最低温）、DeepSeek 穿衣建议、定点推送、多城市合并、@ 管理员。
- **不做**：实时气温、风向风力、多日预报、生活指数抓取、去重/持久化（每天只推一次，天然无需去重）。

## 整体接入方式

新增 `monitor.Weather`（`internal/monitor/weather.go`），实现现有 `monitor.Monitor` 接口
（`Run(ctx, cfg) error`），与 V2EX / HackerNews / command.Bot 平级。在 `cmd/news2tg/main.go`
的 `monitors` 表里作为第四个 monitor 加入。

业务代码只依赖已有接口：
- `notify.Notifier`：发送消息（用 `NotifyBatch`，发预渲染 MarkdownV2）。
- 本地 `Advisor` 接口（定义在 monitor 包内）：生成穿衣建议，便于单测注入 fake。

沿用项目依赖注入风格：`NewWeather(httpClient *http.Client, notifier notify.Notifier, advisor Advisor, mentions []Mention) *Weather`，
由 `main` 组装并注入现有的 `httpClient`、`tgClient`、`aiClient`，以及从 `admin_ids` 解析出的提及列表。

## 调度：定点触发（与现有 monitor 的关键差异）

现有 monitor 用 `time.Ticker` 固定间隔轮询；天气是「每天定点一次」，因此：

- `Run` 内部 **不用 Ticker**。每轮计算「下一个推送时刻」到现在的间隔，用 `time.NewTimer` 睡到该点，
  `select` 等 `ctx.Done()` vs `timer.C`；触发后执行推送，再计算下一天，循环。
- 时区固定 `time.LoadLocation("Asia/Shanghai")`，并在文件顶部 `import _ "time/tzdata"`，
  把时区数据库嵌入二进制，保证 Windows 本地开发与容器内都能加载，不依赖系统 zoneinfo。
- **启动行为**：若启动时当天推送时刻已过，则等到**次日**同一时刻，不立即补推（避免重启刷屏）。
- **关闭开关**：若 `weather_enabled = false`，`Run` 直接 `return nil` 退出，不影响其它 monitor。
- 每个推送周期入口打一次 `logx.WithTaskID(ctx, logx.NewTaskID())`，链路日志用 `slog.*Context`。

纯逻辑函数（便于单测）：
```
nextRun(now time.Time, hh, mm int, loc *time.Location) time.Time
```
语义：返回当天 hh:mm；若该时刻 `!After(now)`（已过或正好等于），则加一天。

## 数据获取（weather.com.cn）

每个城市按编码拉一个免费 JSON 接口：

- `http://www.weather.com.cn/data/cityinfo/{code}.html`
  返回 `{"weatherinfo":{"city","cityid","temp1","temp2","weather","img1","img2","ptime"}}`
  取用字段：`city`（显示名）、`weather`（天气状况）、`temp1`/`temp2`（高/低温）。

要点：
- **城市显示名直接用接口返回的 `city` 字段**，所以配置里只填编码。
- 抓取逻辑收在一个隔离的方法/函数里（如 `fetchCity(ctx, code)`），接口变动只改一处。
- 单个城市抓取失败只 log、跳过该城市，不中断整轮（沿用「抓取失败不阻断推送」约定）。
- 请求带 `ctx`（`http.NewRequestWithContext`），复用 `main` 里共享的 `*http.Client`。

已知取舍：这是非官方接口，可能偶发不稳定或字段改动（评审时已认可）。

## 穿衣建议（DeepSeek）

给 `ai.DeepSeek` 增加方法：
```go
func (d *DeepSeek) Advise(ctx context.Context, weatherText string) (string, error)
```
- 自定义中文 prompt，**一次调用处理所有城市**：把各城市的「城市名 + 状况 + 高低温」拼进 prompt，
  要求模型按城市各给一句简短穿衣建议。
- 沿用项目约定：失败返回兜底字符串 + `nil`（不抛 error），如 `"穿衣建议获取失败"`。

weather monitor 依赖本地接口（不直接依赖 ai 包，便于单测）：
```go
type Advisor interface {
    Advise(ctx context.Context, weatherText string) (string, error)
}
```
`main` 把现有 `aiClient`（`*ai.DeepSeek`）注入。若建议为空/兜底，天气正文照发，仅省略/降级建议段落。

## @ 提及管理员

推送时在消息末尾 @ 配置的管理员，触发 Telegram 通知。

- 用 MarkdownV2 内联提及：`[显示名](tg://user?id=ID)`。点击可跳转，且能给该用户推送提醒。
- **前提**：被提及者需为该 `chat_id`（群）的成员。若 `chat_id` 指向频道（无成员概念），
  提及只是可点链接、不会真正触发提醒 —— 这是 Telegram 的机制限制，设计中注明、不做规避。
- 提及目标复用 `admin_ids`（评审确认：当前要 @ 的就是管理员）。
- 显示名来自配置：`admin_ids` 每项支持 `"id"` 或 `"id:显示名"` 两种写法。缺省显示名时回落为 `"管理员"`。
- 显示名同样经 `tools.EscapeMarkdownV2` 转义后再放进 `[...]`。

`Mention` 类型定义在 monitor 包（`type Mention struct { ID int64; Name string }`），由 `main` 构造注入。

## 消息格式（合并一条）

一条 MarkdownV2 消息，经 `notifier.NotifyBatch`（单元素切片，发预渲染 markdown）。
所有动态片段（城市名、状况、温度、建议文本、@ 显示名）用 `tools.EscapeMarkdownV2` 转义；
`*bold*`、`[..](..)` 等结构标记由代码构造、不转义。示意渲染：

```
🌤️ *每日天气 · 2026-07-19*

*北京*  多云  24~33℃
*广州*  雷阵雨  27~34℃

👕 *穿衣建议*
北京：早晚微凉，薄外套备用…
广州：闷热有雨，短袖 + 带伞…

提醒：@老王 @小李
```
（末行的 `@老王`/`@小李` 实际是 `[老王](tg://user?id=..)` 形式的可点提及。）

拼装逻辑放纯函数 `buildMessage(items []cityWeather, advice string, mentions []Mention) string`，便于单测。

## 配置（`[features]`）

`internal/config/config.go` 的 `Features` 结构新增三个字段：

```go
WeatherEnabled  bool     `toml:"weather_enabled"`
WeatherPushTime string   `toml:"weather_push_time"` // "HH:MM"，空/非法回落 "07:00"
WeatherCities   []string `toml:"weather_cities"`    // 城市编码列表
```

`config.toml` 模板示例：
```toml
weather_enabled   = true
weather_push_time = "07:00"                        # HH:MM，可改推送时间；空/非法回落 07:00
weather_cities    = ["101010100", "101280101"]     # 城市编码：101010100=北京 101280101=广州
```

`weather_push_time` 解析放纯函数 `parsePushTime(s string) (hh, mm int)`：解析失败回落 07:00。

### `admin_ids` 格式扩展（复用为 @ 提及来源）

`admin_ids` 每项支持两种写法，纯 id 完全向后兼容：
```toml
admin_ids = ["123456", "234567:老王"]   # "id" 或 "id:显示名"
```
- `main` 解析每项：`parseAdminEntry(s) (id int64, name string, err error)`（纯函数，按第一个 `:` 分割；
  无 `:` 则 name 为空；id 非法则 log 跳过，沿用现有 `跳过非法 admin_id` 逻辑）。
- `command.Bot` 仍只需 `[]int64`（白名单，行为不变）；`main` 从解析结果取 id 部分传入。
- weather monitor 收 `[]Mention`（id + 显示名，name 为空时回落 `"管理员"`）。

## 错误处理策略（汇总，均沿用项目约定）

- 单城市抓取失败 → log + 跳过该城市，其余照推。
- 全部城市抓取失败 → 本轮不发消息，log 记录，等下一天。
- AI 建议失败 → 兜底/省略建议段，天气正文照发。
- `ctx` 取消 → `Run` 返回 `ctx.Err()`（正常关闭）。

## 测试（纯逻辑，不联网 / 不发真消息）

新增 `internal/monitor/weather_test.go`，表驱动风格：

- `nextRun(now, hh, mm, loc)`：断言「时刻已过 → 次日；未过 → 今日」，含边界（正好等于）。
- `parsePushTime(s)`：合法 "07:00" / "23:59"；非法 "" / "7" / "25:00" → 回落 07:00。
- `parseAdminEntry(s)`：`"123"` → id=123,name=""；`"123:老王"` → id=123,name="老王"；非法 id → err。
- `buildMessage(items, advice, mentions)`：断言含各城市、@ 提及为 `[名](tg://user?id=..)`、转义正确、
  有/无建议、有/无提及、显示名缺省回落 `"管理员"` 各情况。
- 抓取与 `Advisor` 用 fake 注入：验证降级路径（某城市抓取失败仍出其余；AI 失败仍出天气正文）。

## 涉及文件

- 新增 `internal/monitor/weather.go`
- 新增 `internal/monitor/weather_test.go`
- 改 `internal/ai/deepseek.go`（加 `Advise` 方法）
- 改 `internal/config/config.go`（`Features` 加 3 字段）
- 改 `cmd/news2tg/main.go`（组装并注册 weather monitor；`admin_ids` 解析改为 `parseAdminEntry`，
  同时产出 `[]int64`（给 command.Bot）与 `[]Mention`（给 weather monitor））
- 改 `config.toml` / `myconfig.toml`（模板与本地配置示例，含 `admin_ids` 的 `id:显示名` 示例）
