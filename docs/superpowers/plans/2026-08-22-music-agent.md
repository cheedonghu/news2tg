# /music 音乐抓取 agent 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 Telegram bot 增加 `/music <歌曲描述>` 指令：LLM 理解自由格式描述，从 mp3.pm 搜索并挑选正确曲目，下载到本地临时目录，再并发上传到两个 alist WebDAV 目标，全程用一条原地编辑的 Telegram 消息回报进度。

**Architecture:** 新增 `internal/music` 包。`Source` 接口是音源扩展点（`Search`/`Download`），agent 为每个注册的源自动生成 `search_<名>`/`download_<名>` 两个 function-calling 工具。上传不是工具，是 agent 返回后的确定性 Go 步骤。进度通过 `Reporter` 接口（覆盖式完整快照）流向 `notify.Editor` 的 Telegram 实现，后者做 3s 合并节流。

**Tech Stack:** Go 1.25、`sashabaranov/go-openai`（DeepSeek 兼容）、`PuerkitoBio/goquery`（HTML 解析，已是直接依赖）、`go-telegram-bot-api/v5`、标准库 `net/http`（WebDAV PUT）。**不引入任何新依赖。**

**设计文档：** `docs/superpowers/specs/2026-08-22-music-agent-design.md`

## Global Constraints

- **中文教学注释是本仓库的房屋风格**，不是噪音。新代码几乎每个非平凡语句都要带中文注释解释 Go 语义与设计意图，密度对齐 `internal/agent/agent.go`、`internal/store/sqlite.go`。不要写英文注释，不要精简掉注释。
- **日志：** 任务链路内一律用 `slog.InfoContext(ctx, …)` / `slog.ErrorContext(ctx, …)` / `slog.WarnContext(ctx, …)`，让 `logx` 注入的 `task_id` 能流过去。基础设施日志（`main` 里的启动/关停）才用不带 Context 的 `slog.Info`。
- **验收命令：** `go test -short ./...`。**绝不要**跑不带 `-short` 的 `go test ./...` —— `internal/agent/agent_test.go` 里有 e2e 测试会真实调用 DeepSeek API 花钱。
- **`gofmt -l .` 不是门禁。** 本仓库因 CRLF 天然列出二十余个文件。只对本次新增/修改的文件跑 `gofmt -l <文件>`，并用 `go vet ./...` 兜底。
- **错误信息、用户可见文案、提示词一律中文。**
- **MarkdownV2 转义约定：** `notify.Notify`/`NotifyTo` 内部转义纯文本，调用方不得预转义；`NotifyMarkdown`/`Editor.SendEditable`/`Editor.Edit` 发的是已渲染 MarkdownV2，调用方自己用 `tools.EscapeMarkdownV2` 转义动态片段。
- **字符串截断一律用 `tools.TruncateUTF8`**（rune 感知），绝不用 `s[:n]` 按字节切。
- **下载大小上限：50MB**（`50 << 20`）。
- **超时预算：** agent 循环 180s；每个 WebDAV 目标 300s；`/music` 任务总预算 600s。
- **`maxSteps = 6`**，模型最多搜索 3 次。
- **提交信息格式**沿用仓库习惯：`#feat` / `#fix` / `#docs` / `#test` 开头 + 中文描述，结尾带 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。

---

## File Structure

**新建：**

| 文件 | 职责 |
|---|---|
| `internal/music/source.go` | `Source` 接口 + `Candidate` DTO。音源扩展点，无任何站点逻辑 |
| `internal/music/mp3pm.go` | `Source` 的 mp3.pm 实现。**唯一含站点特有逻辑的文件** |
| `internal/music/mp3pm_test.go` | fixture 解析 + httptest 两跳搜索 |
| `internal/music/testdata/mp3pm_search.html` | 真实结果页裁剪出的 fixture |
| `internal/music/report.go` | `Stage`/`TargetStatus`/`Status`/`Reporter` + Telegram 节流实现 + 渲染 |
| `internal/music/report_test.go` | 节流合并、终态立即发、首发/后续编辑切换 |
| `internal/music/upload.go` | `Target`、`Uploader`（WebDAV 并发 PUT）、`BuildFilename`（文件名清洗） |
| `internal/music/upload_test.go` | 假 WebDAV、部分失败语义、文件名清洗 |
| `internal/music/agent.go` | LLM 工具调用循环、`Track` DTO、`limitWriter` 大小上限 |
| `internal/music/agent_test.go` | fake `chatCompleter` + fake `Source`，覆盖全部分支 |
| `internal/music/runner.go` | 串联 agent + uploader + reporter 的编排层 |
| `internal/music/runner_test.go` | fake fetcher/uploader，覆盖成功与各失败路径 |

> 说明：设计文档的文件清单里没有 `runner.go`（编排层当时没单独列出）。把编排从 `agent.go` 拆出来是为了让 agent 只管"模型循环"这一件事，同时让编排层能靠两个消费侧接口独立测试。这是本计划对 spec 的唯一结构性细化。

**修改：**

| 文件 | 改动 |
|---|---|
| `internal/config/config.go` | 新增 `Music` 结构体、`Configured()`、`validate()`，接进 `FromFile` |
| `internal/config/config_test.go` | 新增 `[music]` 全空/部分填写/完整 三组用例 |
| `internal/notify/notify.go` | 新增 `Editor` 接口 |
| `internal/notify/telegram.go` | `send` 抽出 `sendRaw`；新增 `SendEditable`/`Edit`；编译期断言 |
| `internal/command/bot.go` | `classify` 重构成返回 `intent`；新增 `MusicRunner` 依赖与 `/music` 分支 |
| `internal/command/bot_test.go` | 用例改造 + `/music` 新用例 |
| `cmd/news2tg/main.go` | 组装 music 组件；注意接口类型 nil 陷阱 |
| `config.toml` | 新增 `[music]` 空占位段 + 注释 |
| `CLAUDE.md` / `README.md` | 同步架构与配置说明 |

---

## Task 1: `[music]` 配置段

**Files:**
- Modify: `internal/config/config.go`（在 `Storage` 结构体后新增；`FromFile` 末尾接校验）
- Test: `internal/config/config_test.go`（追加）

**Interfaces:**
- Consumes: 无（第一个任务）
- Produces: `config.Music` 结构体，字段 `WebdavUser`/`WebdavPass`/`WebdavName1`/`WebdavURL1`/`WebdavName2`/`WebdavURL2` 均为 `string`；方法 `func (m Music) Configured() bool`；`config.Config` 新增字段 `Music Music \`toml:"music"\``。Task 9 的 `main.go` 会读这些。

- [ ] **Step 1: 写失败的测试**

追加到 `internal/config/config_test.go` 末尾：

```go
// musicTOML 拼一份带 [music] 段的最小可用配置。
// deepseek/storage 是 FromFile 的必填项，每个用例都得带上，抽成 helper 免得重复。
func musicTOML(musicSection string) string {
	return `
[deepseek]
api_token = "k"
model = "deepseek-v4-flash"
agent_model = "deepseek-chat"

[storage]
db_path = "./target/dev.db"
` + musicSection
}

// TestFromFileMusicAbsent 验证：整个 [music] 段缺失时不报错，Configured() 为 false。
// 这是「可选功能不能让现有部署升级即挂」这条设计的守门测试。
func TestFromFileMusicAbsent(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML("")))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if cfg.Music.Configured() {
		t.Errorf("[music] 段缺失时 Configured() 应为 false")
	}
}

// TestFromFileMusicComplete 验证：六项齐全时解析正确且 Configured() 为 true。
func TestFromFileMusicComplete(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://alist:5244/dav/onedrive/Music"
`))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if !cfg.Music.Configured() {
		t.Fatalf("六项齐全时 Configured() 应为 true")
	}
	if cfg.Music.WebdavName1 != "阿里云盘" {
		t.Errorf("WebdavName1 = %q, want %q", cfg.Music.WebdavName1, "阿里云盘")
	}
	if cfg.Music.WebdavURL2 != "http://alist:5244/dav/onedrive/Music" {
		t.Errorf("WebdavURL2 = %q", cfg.Music.WebdavURL2)
	}
	if cfg.Music.WebdavPass != "pw" {
		t.Errorf("WebdavPass = %q, want %q", cfg.Music.WebdavPass, "pw")
	}
}

// TestFromFileMusicPartial 验证：填了一部分就直接启动失败。
// 半配置一定是打字漏了，静默降级只会让人对着「上传失败」抓瞎。
func TestFromFileMusicPartial(t *testing.T) {
	cases := []struct {
		name    string
		section string
	}{
		{
			name: "只填了凭据没填目标",
			section: `
[music]
webdav_user = "alist"
webdav_pass = "pw"
`,
		},
		{
			name: "第二个目标缺 url",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
`,
		},
		{
			name: "只有空白字符也算填了",
			section: `
[music]
webdav_user = "   "
`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			cfg, err := FromFile(writeTempTOML(t, musicTOML(c.section)))
			if err == nil {
				t.Fatalf("FromFile 期望报错，却成功返回 %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("报错时应返回 nil *Config，实际 %+v", cfg)
			}
			if !strings.Contains(err.Error(), "[music]") {
				t.Errorf("错误信息 %q 应指明是 [music] 段的问题", err.Error())
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```
go test ./internal/config -run TestFromFileMusic -v
```

预期：编译失败，`cfg.Music undefined (type Config has no field or method Music)`。

- [ ] **Step 3: 写实现**

在 `internal/config/config.go` 的 `Storage` 结构体之后插入：

```go
// Music 段：/music 指令的 WebDAV 上传目标与凭据。
//
// 为什么是六个平铺字段而不是一个数组？
// 需求是"固定两个目标、每次都传"，不需要动态列表；平铺字段最直白，
// 也不用为 TOML 数组解析写额外代码。Go 侧组装成 []music.Target 之后，
// Uploader 内部仍然是按切片循环的，将来加第三个网盘只需在这里加两个字段。
//
// 凭据只写在 myconfig.toml（已 gitignore）里 —— config.toml 是进 git 的模板，
// 仓库又是公开的，密码写进去等于直接泄漏。
type Music struct {
	WebdavUser  string `toml:"webdav_user"`
	WebdavPass  string `toml:"webdav_pass"`
	WebdavName1 string `toml:"webdav_name_1"`
	WebdavURL1  string `toml:"webdav_url_1"`
	WebdavName2 string `toml:"webdav_name_2"`
	WebdavURL2  string `toml:"webdav_url_2"`
}

// fields 把六个字段收成一张「配置项名 → 值」表，供 Configured/validate 共用，
// 避免两处各写一遍字段清单（加字段时只改这里一处）。
// 返回切片而不是 map：要保证报错时字段顺序稳定，map 遍历顺序是随机的。
func (m Music) fields() []struct {
	key string
	val string
} {
	return []struct {
		key string
		val string
	}{
		{"webdav_user", m.WebdavUser},
		{"webdav_pass", m.WebdavPass},
		{"webdav_name_1", m.WebdavName1},
		{"webdav_url_1", m.WebdavURL1},
		{"webdav_name_2", m.WebdavName2},
		{"webdav_url_2", m.WebdavURL2},
	}
}

// Configured 报告 [music] 是否配置完整。
// 全空 = 功能关闭（不是错误）；FromFile 已经保证不会出现"填一半"的状态，
// 所以这里只需判断第一个字段非空即可 —— 但为了不依赖那个隐含前提，仍逐项检查。
func (m Music) Configured() bool {
	for _, f := range m.fields() {
		if strings.TrimSpace(f.val) == "" {
			return false
		}
	}
	return true
}

// validate 执行「要么全空，要么全填」的校验。
//   - 全空   → 功能关闭，返回 nil（现有部署没有 [music] 段，不能因升级就起不来）
//   - 全填   → 返回 nil
//   - 填一半 → 返回 error，列出缺了哪些项，启动即失败
func (m Music) validate() error {
	var missing []string
	filled := 0
	for _, f := range m.fields() {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.key)
		} else {
			filled++
		}
	}
	// filled == 0 是「整段没配」，功能关闭；len(missing) == 0 是「配全了」。
	if filled == 0 || len(missing) == 0 {
		return nil
	}
	// strings.Join 把切片按分隔符拼成一句话，比循环拼字符串直观。
	return fmt.Errorf("[music] 段配置不完整，缺少：%s（该段要么整段不配、要么六项全配）", strings.Join(missing, ", "))
}
```

在 `Config` 结构体里加字段（放 `Storage` 之后）：

```go
	Storage  Storage  `toml:"storage"`
	Music    Music    `toml:"music"` // 新增；可选功能，全空即关闭
```

在 `FromFile` 的 `return &cfg, nil` 之前插入：

```go
	// [music] 是可选功能：全空则 /music 不可用，但不阻止启动；
	// 填一半则报错 —— 半配置状态一定是打字漏了。
	if err := cfg.Music.validate(); err != nil {
		return nil, err
	}
```

- [ ] **Step 4: 跑测试确认通过**

```
go test ./internal/config -v
```

预期：`TestFromFileMusicAbsent`、`TestFromFileMusicComplete`、`TestFromFileMusicPartial` 三个及其子测试全 PASS，原有 `TestParseMention`/`TestFromFileDeepSeekModel`/`TestFromFileMissingModel` 不回归。

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "#feat 新增 [music] 配置段与全空/全填校验

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 2: `notify.Editor` 接口与 Telegram 实现

**Files:**
- Modify: `internal/notify/notify.go`（文件末尾追加接口）
- Modify: `internal/notify/telegram.go:61-91`（`send` 抽出 `sendRaw`）、文件末尾（新增两个方法）

**Interfaces:**
- Consumes: 无
- Produces:
  ```go
  type Editor interface {
      SendEditable(ctx context.Context, chatID int64, md string) (msgID int, err error)
      Edit(ctx context.Context, chatID int64, msgID int, md string) error
  }
  ```
  `*notify.Telegram` 实现它。Task 4 的 `music` 包按这个接口写 fake；Task 9 把 `tgClient` 传进去。

> **本任务无单元测试。** 理由：`notify.NewTelegram` 构造时就会真连 Telegram API 验 token，仓库里也没有 `telegram_test.go` 先例；为一层薄封装引入 HTTP 层 mock 不值当。守门靠**编译期接口断言** + Task 4 里针对 `Editor` fake 的完整测试。这是有意的取舍，不是遗漏。

- [ ] **Step 1: 加接口定义与编译期断言**

追加到 `internal/notify/notify.go` 末尾：

```go
// Editor 是"可原地编辑的消息通道"。
//
// 为什么不并进 Notifier？
// Notifier 的语义是"发出去就不管了"，monitor 那些只推不改的调用方压根不需要 msgID；
// 把编辑方法塞进去会逼它们全都认识这个概念。分成两个接口后，
// 只有真正要改消息的调用方（music 的进度上报）才依赖 Editor。
//
// 转义约定：与 NotifyMarkdown 一致 —— md 是**已渲染好的 MarkdownV2**，
// 实现不做整体转义，调用方自己转义动态片段。
//
// 限速：由实现内部保证（与 Notifier 共用同一把全局节流锁）。
type Editor interface {
	// SendEditable 发一条新消息，返回它的 message id，供后续 Edit 使用。
	SendEditable(ctx context.Context, chatID int64, md string) (msgID int, err error)
	// Edit 用新内容覆盖已有消息。
	Edit(ctx context.Context, chatID int64, msgID int, md string) error
}
```

在 `internal/notify/telegram.go` 的 import 之后加断言：

```go
// 编译期断言：*Telegram 必须同时满足 Notifier 和 Editor。
// 写成 var _ = 形式不占运行时开销，接口漏实现会在编译阶段就报错。
var (
	_ Notifier = (*Telegram)(nil)
	_ Editor   = (*Telegram)(nil)
)
```

- [ ] **Step 2: 跑编译确认失败**

```
go build ./...
```

预期：FAIL，`*Telegram does not implement Editor (missing method Edit)`。

- [ ] **Step 3: 把 `send` 抽成 `sendRaw` 并实现两个方法**

把 `telegram.go` 现有的 `send` 方法（`61-91` 行）整体替换为：

```go
// sendRaw 是所有发送/编辑路径的唯一出口：先取得限速许可，再真正发，
// 返回 Telegram 回传的消息（SendEditable 需要里面的 MessageID）。
//
// 参数类型从 MessageConfig 放宽到 Chattable：编辑消息用的
// EditMessageTextConfig 也满足 Chattable，这样发送和编辑共用同一把节流锁。
//
// 整个方法持有 mu，所以发送是全局串行的 —— 这正是想要的：
// Telegram 的限额是按 bot 算的，不是按调用点算的。
func (t *Telegram) sendRaw(ctx context.Context, c tgbotapi.Chattable) (tgbotapi.Message, error) {
	// 先看 ctx 是否已取消：bot.Send 不接受 ctx，一旦进去就拦不住了。
	// 这个检查必须在抢锁之前，也必须独立于下面的限速等待 ——
	// 距上次发送超过 sendInterval 时不会进入等待分支，那条路径同样需要被 ctx 拦住。
	if err := ctx.Err(); err != nil {
		return tgbotapi.Message{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// time.Since(零值) 是一个巨大的正数，所以第一次发送 wait <= 0，不等待。
	if wait := sendInterval - time.Since(t.lastSend); wait > 0 {
		// 用 Timer 而不是 time.Sleep：这样等待期间可以被 ctx 取消。
		timer := time.NewTimer(wait)
		defer timer.Stop() // 提前返回时释放 timer，避免泄漏
		select {
		case <-ctx.Done():
			return tgbotapi.Message{}, ctx.Err()
		case <-timer.C:
		}
	}

	sent, err := t.bot.Send(c)
	// 无论成功失败都更新时间戳：失败往往也是被服务端限速了，更该等。
	t.lastSend = time.Now()
	if err != nil {
		return tgbotapi.Message{}, fmt.Errorf("telegram 消息推送失败: %w", err)
	}
	return sent, nil
}

// send 保留原签名，给只关心成败、不需要 MessageID 的调用方用
// （NotifyTo / NotifyMarkdown）。这样这次改造对它们零影响。
func (t *Telegram) send(ctx context.Context, msg tgbotapi.MessageConfig) error {
	_, err := t.sendRaw(ctx, msg)
	return err
}
```

在 `telegram.go` 末尾追加：

```go
// SendEditable 发一条**已渲染好的 MarkdownV2** 消息并返回它的 message id。
// 与 NotifyMarkdown 的区别只有两点：可指定 chatID、会把 message id 交回调用方。
// 关掉链接预览：进度消息会被反复编辑，预览卡片跟着闪很吵。
func (t *Telegram) SendEditable(ctx context.Context, chatID int64, md string) (int, error) {
	msg := tgbotapi.NewMessage(chatID, md)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.DisableWebPagePreview = true
	sent, err := t.sendRaw(ctx, msg)
	if err != nil {
		slog.ErrorContext(ctx, "telegram 可编辑消息发送失败", "chat", chatID, "err", err)
		return 0, err
	}
	return sent.MessageID, nil
}

// Edit 用新内容覆盖已有消息。md 同样是已渲染好的 MarkdownV2。
//
// 注意：Telegram 对"内容完全没变"的编辑会返回
// "message is not modified" 错误。调用方（music 的 Reporter）靠覆盖式快照
// 天然不会连发两条一模一样的内容，所以这里不做特殊处理，如实把错误返回。
func (t *Telegram) Edit(ctx context.Context, chatID int64, msgID int, md string) error {
	e := tgbotapi.NewEditMessageText(chatID, msgID, md)
	e.ParseMode = tgbotapi.ModeMarkdownV2
	if _, err := t.sendRaw(ctx, e); err != nil {
		slog.ErrorContext(ctx, "telegram 编辑消息失败", "chat", chatID, "msg", msgID, "err", err)
		return err
	}
	return nil
}
```

- [ ] **Step 4: 跑编译与既有测试确认通过**

```
go build ./... && go vet ./... && go test -short ./...
```

预期：编译通过（断言满足），既有测试全 PASS（`send` 签名未变，`weather_test.go`/`deliver_test.go` 不受影响）。

- [ ] **Step 5: 提交**

```bash
git add internal/notify/notify.go internal/notify/telegram.go
git commit -m "#feat notify 新增 Editor 接口与 Telegram 原地编辑实现

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 3: `Source` 接口与 mp3.pm 实现

**Files:**
- Create: `internal/music/source.go`
- Create: `internal/music/mp3pm.go`
- Create: `internal/music/testdata/mp3pm_search.html`
- Test: `internal/music/mp3pm_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  ```go
  type Candidate struct {
      ID       string // 源内唯一 id
      Artist   string
      Title    string
      Duration string // "04:44"
      dlURL    string // 小写：不出包，也绝不进模型上下文
  }

  type Source interface {
      Name() string
      Hint() string
      Search(ctx context.Context, query string) ([]Candidate, error)
      Download(ctx context.Context, c Candidate, w io.Writer) (int64, error)
  }

  func NewMp3PM(httpClient *http.Client) *Mp3PM   // 字段 searchAPI 可被同包测试改写
  ```
  Task 6 的 agent 按 `Source` 注册工具；Task 9 的 `main.go` 调 `NewMp3PM`。

- [ ] **Step 1: 建 fixture 文件**

创建 `internal/music/testdata/mp3pm_search.html`。这是从 mp3.pm 真实结果页裁剪出的三条记录，**第三条故意缺 `data-download-url`**，用来锁"脏数据被跳过"这条分支：

```html
<div id="xbody">
<h2 class="mtitle">jay chou</h2>
<ul class="mp3list">
<li class="cplayer-sound-item" data-sound-id="4287389" data-download-url="https://cs1.mp3.pm/download/4287389/TOKEN1/Jay_Chou_-_Kai_Bu_Liao_Kou._(mp3.pm).mp3" data-share-url="https://mp3.pmhttps://158850-jay-chou.mp3.pm/song/4287389-kai-bu-liao-kou/">
	<div class="mp3list-btns">
		<a href="javascript:void(0);" class="mp3list-btn-play cplayer-ui-play" title="play">(play)</a>
		<a href="https://158850-jay-chou.mp3.pm/song/4287389-kai-bu-liao-kou/" class="mp3list-btn-download cplayer-ui-download" title="download">(download)</a>
	</div>
	<h4>
		<a href="https://158850-jay-chou.mp3.pm/"><i class="cplayer-data-sound-author">Jay Chou</i></a>
		<a href="https://158850-jay-chou.mp3.pm/song/4287389-kai-bu-liao-kou/"><b class="cplayer-data-sound-title">Kai Bu Liao Kou.</b></a>
	</h4>
	<em class="cplayer-data-sound-time">04:44</em>
</li><li class="cplayer-sound-item" data-sound-id="12810728" data-download-url="https://cs1.mp3.pm/download/12810728/TOKEN2/Jay_Chou_-_Superman_Can_t_Fly_(mp3.pm).mp3" data-share-url="https://mp3.pmhttps://158850-jay-chou.mp3.pm/song/12810728-superman-can-t-fly/">
	<h4>
		<a href="https://158850-jay-chou.mp3.pm/"><i class="cplayer-data-sound-author">Jay Chou</i></a>
		<a href="https://158850-jay-chou.mp3.pm/song/12810728-superman-can-t-fly/"><b class="cplayer-data-sound-title">Superman Can&#039;t Fly / 超人不會飛 (Chao Ren Bu Hui Fei)</b></a>
	</h4>
	<em class="cplayer-data-sound-time">05:00</em>
</li><li class="cplayer-sound-item" data-sound-id="99999999" data-share-url="https://mp3.pm/broken/">
	<h4>
		<a href="#"><i class="cplayer-data-sound-author">Broken Entry</i></a>
		<a href="#"><b class="cplayer-data-sound-title">No Download URL</b></a>
	</h4>
	<em class="cplayer-data-sound-time">00:00</em>
</li>
</ul>
</div>
```

- [ ] **Step 2: 写失败的测试**

创建 `internal/music/mp3pm_test.go`：

```go
package music

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFixture 读 testdata 下的固定样本。
// testdata 是 Go 工具链约定的目录名：它不会被当成包编译，专门放测试数据。
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s 失败: %v", name, err)
	}
	return string(b)
}

// TestParseMp3PMResults 锁死 HTML 解析行为，尤其是脏标题。
func TestParseMp3PMResults(t *testing.T) {
	got := parseMp3PMResults(context.Background(), loadFixture(t, "mp3pm_search.html"))

	// 第三条缺 data-download-url，必须被跳过 —— 没有直链的候选给模型看也没用。
	if len(got) != 2 {
		t.Fatalf("解析出 %d 条候选，want 2（缺直链的那条应被跳过）", len(got))
	}
	if got[0].ID != "4287389" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "4287389")
	}
	if got[0].Artist != "Jay Chou" {
		t.Errorf("[0].Artist = %q, want %q", got[0].Artist, "Jay Chou")
	}
	if got[0].Title != "Kai Bu Liao Kou." {
		t.Errorf("[0].Title = %q, want %q", got[0].Title, "Kai Bu Liao Kou.")
	}
	if got[0].Duration != "04:44" {
		t.Errorf("[0].Duration = %q, want %q", got[0].Duration, "04:44")
	}
	if !strings.HasPrefix(got[0].dlURL, "https://cs1.mp3.pm/download/4287389/") {
		t.Errorf("[0].dlURL = %q，前缀不对", got[0].dlURL)
	}

	// 第二条是脏标题的重点：带 HTML 实体 &#039;、斜杠、中文、括号。
	// goquery 的 Text() 会把实体解码成真正的单引号，这正是我们要的。
	wantTitle := "Superman Can't Fly / 超人不會飛 (Chao Ren Bu Hui Fei)"
	if got[1].Title != wantTitle {
		t.Errorf("[1].Title = %q, want %q", got[1].Title, wantTitle)
	}
}

// TestParseMp3PMResultsEmpty 验证：页面里没有任何 li.cplayer-sound-item 时
// 返回空切片而非 panic。站点改版会走到这条路径。
func TestParseMp3PMResultsEmpty(t *testing.T) {
	got := parseMp3PMResults(context.Background(), "<html><body><p>nothing here</p></body></html>")
	if len(got) != 0 {
		t.Fatalf("空页面应解析出 0 条，实际 %d 条", len(got))
	}
}

// newMp3PMTestServer 起一个假 mp3.pm：
//
//	POST /public/api.search.php → 回一行指向本服务器结果页的 URL
//	GET  /result                → 回 fixture HTML
//
// 用一个 mux 同时扮演两跳，才能覆盖完整的搜索流程。
func newMp3PMTestServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	// 先声明再赋值：handler 里要用到 srv.URL，而 URL 在 NewServer 返回后才有值。
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/public/api.search.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("搜索接口应为 POST，实际 %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败: %v", err)
		}
		if q := r.PostFormValue("q"); q != "jay chou" {
			t.Errorf("q = %q, want %q", q, "jay chou")
		}
		// 真站返回的就是一行纯文本 URL（可能带换行）。
		_, _ = w.Write([]byte(srv.URL + "/result\n"))
	})
	mux.HandleFunc("/result", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestMp3PMSearch 覆盖完整两跳。
func TestMp3PMSearch(t *testing.T) {
	srv := newMp3PMTestServer(t, loadFixture(t, "mp3pm_search.html"))

	m := NewMp3PM(srv.Client())
	m.searchAPI = srv.URL + "/public/api.search.php" // 指向假服务器

	got, err := m.Search(context.Background(), "jay chou")
	if err != nil {
		t.Fatalf("Search 意外报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search 返回 %d 条，want 2", len(got))
	}
	if got[0].ID != "4287389" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "4287389")
	}
}

// TestMp3PMSearchBadRedirect 验证：搜索接口回的不是 URL 时报错，
// 而不是拿着垃圾去 GET 得到更莫名其妙的错误。
func TestMp3PMSearchBadRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("error: rate limited"))
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	m.searchAPI = srv.URL

	if _, err := m.Search(context.Background(), "x"); err == nil {
		t.Fatal("搜索接口返回非 URL 时应报错")
	}
}

// TestMp3PMDownload 验证下载把字节原样写进 io.Writer 并返回字节数。
func TestMp3PMDownload(t *testing.T) {
	const payload = "ID3fake-mp3-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	var buf strings.Builder
	n, err := m.Download(context.Background(), Candidate{ID: "1", dlURL: srv.URL}, &buf)
	if err != nil {
		t.Fatalf("Download 意外报错: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("写入 %d 字节, want %d", n, len(payload))
	}
	if buf.String() != payload {
		t.Errorf("内容 = %q, want %q", buf.String(), payload)
	}
}

// TestMp3PMDownloadNon2xx 验证非 2xx 时报错，让上层能回灌给模型换候选。
func TestMp3PMDownloadNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	var buf strings.Builder
	if _, err := m.Download(context.Background(), Candidate{ID: "1", dlURL: srv.URL}, &buf); err == nil {
		t.Fatal("403 时应报错")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/music -v`
Expected: FAIL — 编译不过，`undefined: parseMp3PMResults` / `undefined: NewMp3PM`。

- [ ] **Step 4: 写 `internal/music/source.go`**

```go
// Package music 实现 /music 指令背后的整条链路：
// LLM 理解歌曲描述 → 从音源搜索并挑选 → 下载到本地 → 并发上传到多个 WebDAV 目标，
// 全程通过 Reporter 把进度回报出去。
//
// 包内分工：
//
//	source.go  音源的统一形状（扩展点），无任何站点逻辑
//	mp3pm.go   Source 的 mp3.pm 实现，唯一含站点特有逻辑的文件
//	agent.go   LLM 工具调用循环
//	upload.go  WebDAV 并发上传
//	report.go  进度快照与上报
//	runner.go  把上面几件事串起来的编排层
package music

import (
	"context"
	"io"
)

// Candidate 是一条搜索结果。
//
// dlURL 小写 = 包外不可见。这是刻意的：那个直链带一长串 token（200+ 字符），
// 既不该出包，更不该进模型上下文 —— 20 条候选就是 4000+ 字符白烧 token，
// 而且模型很容易把它改坏。模型只看得到 ID，Go 侧凭 ID 查表拿真直链。
type Candidate struct {
	ID       string // 源内唯一 id（mp3.pm 用 data-sound-id）
	Artist   string // 站点原始歌手名，可能是罗马化的（"Jay Chou"）
	Title    string // 站点原始歌名，可能是罗马化的（"Kai Bu Liao Kou."）
	Duration string // "04:44"
	dlURL    string // 下载直链
}

// Source 是"一个音源"的统一形状，也是本包唯一的扩展点。
//
// 实现独立、形状统一：每个音源自己一个文件、自己一套解析逻辑，
// 但都长成这个样子，于是 agent 能为它自动生成 search_<名> / download_<名>
// 两个工具，加新音源时 agent 循环一行都不用改。
//
// Download 收 io.Writer 而不是文件路径：让"写到哪里"归调用方决定，
// 音源只负责把字节流吐出来。大小上限由调用方在这个 Writer 上强制（见 agent.go
// 的 limitWriter），不下放给各音源 —— 否则每加一个源都要重复实现一遍同样的防御。
type Source interface {
	// Name 返回源标识，用于拼工具名，必须是合法的函数名片段（小写字母/数字/下划线）。
	Name() string
	// Hint 是给模型看的站点特性说明书，会拼进工具 description。
	// "某个站怎么搜才有效"这条知识跟着站点实现走，不散落进全局 system prompt。
	Hint() string
	// Search 按关键词搜索，返回候选列表。
	// 零结果返回空切片 + nil error —— "没搜到"是正常路径，不是错误。
	Search(ctx context.Context, query string) ([]Candidate, error)
	// Download 把候选的音频字节写进 w，返回实际写入字节数。
	Download(ctx context.Context, c Candidate, w io.Writer) (int64, error)
}
```

- [ ] **Step 5: 写 `internal/music/mp3pm.go`**

```go
package music

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	// 第三方包：HTML 解析（类似 jQuery 的 API），仓库里 hackernews.go 已在用。
	"github.com/PuerkitoBio/goquery"

	"github.com/cheedonghu/news2tg/internal/tools"
)

const (
	// mp3pmName 是源标识，同时决定工具名 search_mp3pm / download_mp3pm。
	mp3pmName = "mp3pm"
	// mp3pmSearchAPI 是站点前端 Angular 用的搜索接口（form-encoded POST）。
	// 它不返回 JSON，返回的是**一行纯文本结果页 URL**。
	mp3pmSearchAPI = "https://mp3.pm/public/api.search.php"
	// browserUA：站点对默认的 Go-http-client UA 不友好，伪装成浏览器更稳。
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
)

// 编译期断言：*Mp3PM 必须满足 Source。接口漏实现会在编译阶段就报错。
var _ Source = (*Mp3PM)(nil)

// Mp3PM 是 mp3.pm 的 Source 实现。
type Mp3PM struct {
	httpClient *http.Client
	// searchAPI 单独抽成字段（而非直接用常量）是为了测试能指向 httptest，
	// 与 digest/jina.go 里 baseURL 的做法一致。
	searchAPI string
}

// NewMp3PM 构造函数。httpClient 复用 main 里创建的共享连接池。
func NewMp3PM(httpClient *http.Client) *Mp3PM {
	return &Mp3PM{httpClient: httpClient, searchAPI: mp3pmSearchAPI}
}

// Name 返回源标识。
func (m *Mp3PM) Name() string { return mp3pmName }

// Hint 告诉模型这个站怎么搜才有效。
// 这条是实测结论：站上"开不了口"存的是 "Kai Bu Liao Kou."，
// 直接用中文关键词搜大概率零结果。
func (m *Mp3PM) Hint() string {
	return "俄语站点，中文歌曲以拼音或英文译名收录（如「开不了口」存为「Kai Bu Liao Kou」，" +
		"「超人不会飞」存为「Superman Can't Fly」）。直接用中文关键词搜索大概率零结果，" +
		"应改用拼音全名、英文译名，或只用歌名不带歌手。"
}

// Search 走两跳：
//
//	① POST searchAPI（form: q=关键词）→ 响应体是一行结果页 URL
//	② GET 那个 URL → 结果页 HTML，交给 parseMp3PMResults 解析
//
// 为什么不是一次请求？站点前端是 Angular，搜索框提交时先打这个接口拿到
// 该关键词对应的结果页地址（形如 https://s-jay-chou.mp3.pm/），再跳过去。
func (m *Mp3PM) Search(ctx context.Context, query string) ([]Candidate, error) {
	slog.InfoContext(ctx, "开始在 mp3.pm 搜索", "query", query)

	// ① 拿结果页地址。
	// url.Values 是 map[string][]string 的别名，Encode() 把它编成 a=1&b=2。
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.searchAPI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest") // 前端就是这么发的
	req.Header.Set("User-Agent", browserUA)

	pageURL, err := m.readBody(req, "mp3.pm 搜索接口")
	if err != nil {
		return nil, err
	}
	pageURL = strings.TrimSpace(pageURL)
	// 站点异常时会回一段错误文本而不是 URL；拿着它去 GET 只会得到更莫名其妙的错误，
	// 所以在这里就拦住并把原文（截断）带进错误信息，方便排查。
	if !strings.HasPrefix(pageURL, "http") {
		return nil, fmt.Errorf("mp3.pm 搜索接口未返回结果页地址: %q", tools.TruncateUTF8(pageURL, 100))
	}

	// ② 抓结果页。
	pageReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	pageReq.Header.Set("User-Agent", browserUA)
	body, err := m.readBody(pageReq, "mp3.pm 结果页")
	if err != nil {
		return nil, err
	}

	return parseMp3PMResults(ctx, body), nil
}

// readBody 发请求、读完整响应体、校验状态码，是上面两跳的公共部分。
// what 是出错时用来说明"哪一跳挂了"的标签。
func (m *Mp3PM) readBody(req *http.Request, what string) (string, error) {
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s 请求失败: %w", what, err)
	}
	// defer 在函数返回时执行，保证连接被释放回连接池。
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取 %s 响应失败: %w", what, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s 返回非 2xx 状态码 %d", what, resp.StatusCode)
	}
	return string(b), nil
}

// parseMp3PMResults 从结果页 HTML 里抽出候选列表。
//
// 页面结构（实测）：每首歌一个 <li class="cplayer-sound-item">，
// 直链就在 li 自己的 data-download-url 属性上，歌手/歌名/时长在子元素里。
// 也就是说搜索结果页一次性给全，不需要再进详情页。
//
// 返回值不带 error：解析不出东西时返回空切片，由调用方当作"零结果"处理 ——
// 对模型来说"没搜到"和"页面变了"都该走同一条"换关键词重试"的路。
// 但两者对我们排障的意义不同，所以下面额外打一条 WARN 区分。
func parseMp3PMResults(ctx context.Context, htmlBody string) []Candidate {
	// goquery 从 io.Reader 解析；strings.NewReader 把字符串包成 Reader。
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		slog.ErrorContext(ctx, "解析 mp3.pm 结果页 HTML 失败", "err", err)
		return nil
	}

	var out []Candidate
	// Each 的回调签名是 func(下标 int, 元素 *goquery.Selection)；_ 表示丢弃下标。
	doc.Find("li.cplayer-sound-item").Each(func(_ int, s *goquery.Selection) {
		// Attr 返回 (值, 是否存在)；这里只关心值是否为空。
		id, _ := s.Attr("data-sound-id")
		dl, _ := s.Attr("data-download-url")
		// 没有 id 或没有直链的条目对我们毫无用处（模型选了也下不下来），直接跳过。
		if id == "" || dl == "" {
			return
		}
		out = append(out, Candidate{
			ID: id,
			// First() 防御同一个 li 里意外出现多个同类元素；
			// Text() 会把 HTML 实体（&#039;）解码成真正的字符。
			Artist:   strings.TrimSpace(s.Find(".cplayer-data-sound-author").First().Text()),
			Title:    strings.TrimSpace(s.Find(".cplayer-data-sound-title").First().Text()),
			Duration: strings.TrimSpace(s.Find(".cplayer-data-sound-time").First().Text()),
			dlURL:    dl,
		})
	})

	// 页面有内容却一条都没解析出来 —— 大概率是站点改版了，而不是"真没这首歌"。
	// 单独告警，让日志能区分这两种情况，否则改版会静默表现成"永远搜不到"。
	if len(out) == 0 && strings.TrimSpace(htmlBody) != "" {
		slog.WarnContext(ctx, "mp3.pm 结果页解析出 0 条候选（可能站点改版）", "bodyLen", len(htmlBody))
	}
	return out
}

// Download 直接 GET 直链把音频字节写进 w。
// 实测该直链无需 referer / cookie，支持 Range，所以这里就是一个朴素的 GET + io.Copy。
func (m *Mp3PM) Download(ctx context.Context, c Candidate, w io.Writer) (int64, error) {
	slog.InfoContext(ctx, "开始从 mp3.pm 下载", "id", c.ID, "title", c.Title)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.dlURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", browserUA)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mp3.pm 下载请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("mp3.pm 下载返回非 2xx 状态码 %d", resp.StatusCode)
	}

	// io.Copy 从 Body 流式拷到 w，不会把整个文件读进内存。
	// w 可能是带大小上限的 limitWriter，超限时它会返回错误让 Copy 提前中断；
	// 这里用 %w 包装以保留 errors.Is 的可判别性（agent 要靠它区分"过大"和别的失败）。
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("写入 mp3 数据失败: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: PASS — `TestParseMp3PMResults`、`TestParseMp3PMResultsEmpty`、`TestMp3PMSearch`、`TestMp3PMSearchBadRedirect`、`TestMp3PMDownload`、`TestMp3PMDownloadNon2xx` 全绿。

- [ ] **Step 7: 提交**

```bash
git add internal/music/source.go internal/music/mp3pm.go internal/music/mp3pm_test.go internal/music/testdata/mp3pm_search.html
git commit -m "#feat music 新增 Source 接口与 mp3.pm 实现

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 4: 进度快照与 Telegram 节流上报

**Files:**
- Create: `internal/music/report.go`
- Test: `internal/music/report_test.go`

**Interfaces:**
- Consumes: `notify.Editor`（Task 2）
- Produces:
  ```go
  type Stage int
  const (StageSearching Stage = iota; StageDownloading; StageUploading; StageDone; StageFailed)

  type TargetState int
  const (TargetPending TargetState = iota; TargetRunning; TargetOK; TargetFailed)

  type TargetStatus struct { Name string; State TargetState; Err string }

  type Status struct {
      Query    string
      Stage    Stage
      Searches int
      Found    int
      Track    *Track          // Track 在 Task 6 定义
      Targets  []TargetStatus
      Err      string
  }

  type Reporter interface {
      Update(ctx context.Context, s Status)
      Done(ctx context.Context, s Status)
  }

  func NewTelegramReporter(e notify.Editor, chatID int64) Reporter
  func newTGReporter(e notify.Editor, chatID int64, throttle time.Duration) *tgReporter // 测试用
  ```
  Task 5/6/7 都消费 `Reporter` 与 `Status`。

> **重要顺序说明：** `Status.Track` 的类型 `*Track` 在 Task 6 才定义。为了让本任务能独立编译通过，**本任务先在 `report.go` 里定义 `Track` 结构体**，Task 6 直接使用、不重复定义。`Track` 的完整定义见 Step 4。

- [ ] **Step 1: 写失败的测试**

创建 `internal/music/report_test.go`：

```go
package music

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEditor 记录所有调用，替代真实的 Telegram 客户端。
// 加锁是因为节流用的 time.AfterFunc 会在另一个 goroutine 里回调。
type fakeEditor struct {
	mu    sync.Mutex
	sends []string // 每次 SendEditable 的内容
	edits []string // 每次 Edit 的内容
	sendErr error  // 非 nil 时 SendEditable 返回它
}

func (f *fakeEditor) SendEditable(_ context.Context, _ int64, md string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.sends = append(f.sends, md)
	return 42, nil // 固定 msgID，方便断言后续 Edit 用的是它
}

func (f *fakeEditor) Edit(_ context.Context, _ int64, _ int, md string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, md)
	return nil
}

func (f *fakeEditor) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends), len(f.edits)
}

// TestReporterFirstUpdateSends 验证：首次 Update 走 SendEditable，之后走 Edit。
func TestReporterFirstUpdateSends(t *testing.T) {
	fe := &fakeEditor{}
	// 节流设为 0：本用例只关心"首发 vs 编辑"的切换，不关心节流。
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天", Stage: StageSearching, Searches: 1})
	r.Update(ctx, Status{Query: "晴天", Stage: StageSearching, Searches: 2})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Errorf("SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 1 {
		t.Errorf("Edit 调用 %d 次, want 1", edits)
	}
}

// TestReporterThrottleCoalesces 验证：节流窗口内的连续 Update 被合并，
// 只有最新那个快照会在窗口结束后补发出去。
func TestReporterThrottleCoalesces(t *testing.T) {
	fe := &fakeEditor{}
	const throttle = 80 * time.Millisecond
	r := newTGReporter(fe, 1, throttle)
	ctx := context.Background()

	// 第 1 次立即发出（lastSent 是零值，不受节流约束）。
	r.Update(ctx, Status{Query: "晴天", Searches: 1})
	// 紧接着 3 次都落在节流窗口内，应该被合并成一次补发。
	r.Update(ctx, Status{Query: "晴天", Searches: 2})
	r.Update(ctx, Status{Query: "晴天", Searches: 3})
	r.Update(ctx, Status{Query: "晴天", Searches: 4})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Fatalf("节流窗口内 SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 0 {
		t.Fatalf("节流窗口内 Edit 调用 %d 次, want 0（应被合并推迟）", edits)
	}

	// 等窗口结束 + 余量，补发应当发生且只发生一次。
	time.Sleep(throttle * 3)

	sends, edits = fe.counts()
	if edits != 1 {
		t.Fatalf("窗口结束后 Edit 调用 %d 次, want 1（补发最新快照）", edits)
	}
	// 补发的必须是**最新**快照（Searches=4），而不是被丢弃的中间态。
	fe.mu.Lock()
	last := fe.edits[0]
	fe.mu.Unlock()
	if !strings.Contains(last, "4") {
		t.Errorf("补发内容 %q 里没有最新的搜索次数 4", last)
	}
}

// TestReporterDoneIsImmediate 验证：Done 无条件立即发出，不受节流限制。
// 最后一条必须准确，这是整个节流设计里唯一不能妥协的地方。
func TestReporterDoneIsImmediate(t *testing.T) {
	fe := &fakeEditor{}
	r := newTGReporter(fe, 1, 10*time.Second) // 节流拉到 10s，正常情况下绝不会补发
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天", Searches: 1}) // 首发
	r.Update(ctx, Status{Query: "晴天", Searches: 2}) // 被节流丢弃
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone, Searches: 2})

	sends, edits := fe.counts()
	if sends != 1 {
		t.Errorf("SendEditable 调用 %d 次, want 1", sends)
	}
	if edits != 1 {
		t.Fatalf("Edit 调用 %d 次, want 1（Done 必须立即发）", edits)
	}
}

// TestReporterUpdateAfterDoneIgnored 验证：Done 之后迟到的 Update 不再改动消息。
// 上传 goroutine 的进度回调可能晚于 Done 到达，不拦住会把终态覆盖成中间态。
func TestReporterUpdateAfterDoneIgnored(t *testing.T) {
	fe := &fakeEditor{}
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天"})
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone})
	_, before := fe.counts()

	r.Update(ctx, Status{Query: "晴天", Searches: 99}) // 迟到

	if _, after := fe.counts(); after != before {
		t.Errorf("Done 之后的 Update 不应再发送（Edit 从 %d 变成 %d）", before, after)
	}
}

// TestReporterSendErrorDoesNotPanic 验证：Editor 报错时不 panic、不影响后续调用。
// 进度是辅助信息，不该反过来把主流程搞挂。
func TestReporterSendErrorDoesNotPanic(t *testing.T) {
	fe := &fakeEditor{sendErr: errors.New("telegram 挂了")}
	r := newTGReporter(fe, 1, 0)
	ctx := context.Background()

	r.Update(ctx, Status{Query: "晴天"})
	r.Done(ctx, Status{Query: "晴天", Stage: StageDone})
	// 走到这里没 panic 就算通过。首发失败时 msgID 仍为 0，
	// 所以 Done 会再走一次 SendEditable（也失败），同样必须被吞掉。
}

// TestRenderStatus 锁死渲染内容：该出现的字段都出现，MarkdownV2 特殊字符被转义。
func TestRenderStatus(t *testing.T) {
	md := renderStatus(Status{
		Query:    "晴天 - 周杰伦",
		Stage:    StageDone,
		Searches: 2,
		Found:    12,
		Track: &Track{
			Artist: "周杰伦", Title: "晴天", Duration: "03:58",
			Bytes: 8_400_000, Source: "mp3pm", Tokens: 3184,
		},
		Targets: []TargetStatus{
			{Name: "阿里云盘", State: TargetOK},
			{Name: "OneDrive", State: TargetFailed, Err: "507 Insufficient Storage"},
		},
	})

	for _, want := range []string{"周杰伦", "晴天", "03:58", "阿里云盘", "OneDrive", "3184"} {
		if !strings.Contains(md, want) {
			t.Errorf("渲染结果里缺少 %q\n实际:\n%s", want, md)
		}
	}
	// MarkdownV2 里 '-' 是特殊字符，必须被转义成 '\-'，否则 Telegram 会 400。
	if strings.Contains(md, "晴天 - 周杰伦") {
		t.Errorf("查询串里的 '-' 未被转义:\n%s", md)
	}
	if !strings.Contains(md, `\-`) {
		t.Errorf("渲染结果里没有任何转义过的 '-':\n%s", md)
	}
}

// TestRenderStatusFailed 验证失败态把错误原因渲染出来。
func TestRenderStatusFailed(t *testing.T) {
	md := renderStatus(Status{
		Query: "不存在的歌",
		Stage: StageFailed,
		Err:   "模型未收敛",
	})
	if !strings.Contains(md, "模型未收敛") {
		t.Errorf("失败态应展示错误原因:\n%s", md)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/music -run TestReporter -v`
Expected: FAIL — 编译不过，`undefined: newTGReporter` / `undefined: Status`。

- [ ] **Step 3: 写 `internal/music/report.go`**

```go
package music

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/tools"
)

// throttleInterval 是两次**真正发出**编辑请求之间的最小间隔。
//
// 为什么要节流：编辑请求和普通推送共用 notify.Telegram 里那把全局发送锁，
// 每发一次就多占一次 1.5s 的锁，会挤占 HN/V2EX 的推送。3s 是实测下
// "看起来仍然实时"和"别太吵"之间的折中；觉得挤就把这个常量调大。
const throttleInterval = 3 * time.Second

// Stage 是任务所处的阶段。
type Stage int

const (
	StageSearching   Stage = iota // 正在搜索（含换词重搜）
	StageDownloading              // 已选定候选，正在下载
	StageUploading                // 正在上传到 WebDAV 目标
	StageDone                     // 成功收尾
	StageFailed                   // 失败收尾
)

// TargetState 是单个上传目标的状态。
type TargetState int

const (
	TargetPending TargetState = iota // 排队中
	TargetRunning                    // 上传中
	TargetOK                         // 成功
	TargetFailed                     // 失败
)

// TargetStatus 是单个上传目标的快照。
type TargetStatus struct {
	Name  string      // 展示名，如 "阿里云盘"
	State TargetState //
	Err   string      // 失败原因，成功时为空
}

// Track 是 agent 产出的"已下载好的曲目"。
//
// 定义在这里而不是 agent.go：Status 要引用它，而 Status 属于进度模型，
// 让进度模型去依赖 agent 会把依赖方向搞反。
type Track struct {
	LocalPath string // 临时目录下的 mp3 文件绝对路径
	Artist    string // 模型规范化后的中文歌手名（用于拼文件名）
	Title     string // 模型规范化后的中文歌名
	Duration  string // 站点给的时长，如 "03:58"
	Bytes     int64  // 实际写入字节数（不信任 Content-Length）
	Source    string // 源标识，如 "mp3pm"
	Tokens    int    // 本次任务累计消耗的 token
}

// Status 是一次任务的**完整快照**。
//
// 为什么是覆盖式完整快照而不是增量事件？
// 因为每个快照都自洽，所以节流时丢弃中间态是安全的 —— 这是整个节流设计成立的前提。
// 增量事件一旦丢一条，后面的状态就全错了。
type Status struct {
	Query    string         // 用户原始输入
	Stage    Stage          //
	Searches int            // 已搜索次数（含换词重搜）
	Found    int            // 最近一次搜索的候选数
	Track    *Track         // 选定并下载后才非 nil
	Targets  []TargetStatus // 上传目标状态，上传阶段才非空
	Err      string         // 失败原因
}

// Reporter 是一次任务的进度出口。
//
// 实现方负责渲染与节流；调用方（agent / uploader / runner）只管灌快照，
// 不知道背后是 Telegram 还是别的什么。单测时塞个记录所有快照的 fake 即可。
type Reporter interface {
	// Update 报告一个中间快照。实现可以丢弃它（节流），调用方不应假设它一定发出去了。
	Update(ctx context.Context, s Status)
	// Done 报告终态。**必须立即发出**，不受节流约束。
	Done(ctx context.Context, s Status)
}

// 编译期断言。
var _ Reporter = (*tgReporter)(nil)

// tgReporter 把 Status 渲染成 MarkdownV2，通过 notify.Editor 原地编辑同一条消息。
type tgReporter struct {
	editor   notify.Editor
	chatID   int64
	throttle time.Duration

	// mu 保护下面这组状态。time.AfterFunc 的回调在另一个 goroutine 里跑，
	// 上传阶段也有多个 goroutine 并发灌快照，所以必须加锁。
	mu       sync.Mutex
	msgID    int         // 0 表示还没发过首条消息
	lastSent time.Time   // 上次**真正发出**的时刻
	pending  *Status     // 被节流丢弃的最新快照
	timer    *time.Timer // 节流窗口结束时补发用；nil 表示当前没有待补发
	finished bool        // Done 之后为 true，之后的 Update 一律忽略
}

// NewTelegramReporter 构造一个走 Telegram 原地编辑的 Reporter。
func NewTelegramReporter(e notify.Editor, chatID int64) Reporter {
	return newTGReporter(e, chatID, throttleInterval)
}

// newTGReporter 是带节流参数的内部构造函数，让测试能把节流调到毫秒级。
// 导出的 NewTelegramReporter 固定用 throttleInterval。
func newTGReporter(e notify.Editor, chatID int64, throttle time.Duration) *tgReporter {
	return &tgReporter{editor: e, chatID: chatID, throttle: throttle}
}

// Update 报告中间快照，受节流约束。
func (r *tgReporter) Update(ctx context.Context, s Status) {
	r.mu.Lock()
	if r.finished {
		// Done 之后迟到的快照（比如上传 goroutine 的回调）不能覆盖终态。
		r.mu.Unlock()
		return
	}
	// time.Since(零值) 是个巨大的正数，所以首次调用 wait 必为负，直接发出。
	wait := r.throttle - time.Since(r.lastSent)
	if wait > 0 {
		// 落在节流窗口内：只留最新快照，并安排一次窗口结束后的补发。
		// timer 非 nil 说明补发已经排上了，不用再排一次 —— 它会取到最新的 pending。
		r.pending = &s
		if r.timer == nil {
			r.timer = time.AfterFunc(wait, func() { r.flush(ctx) })
		}
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	r.send(ctx, s)
}

// Done 报告终态，无条件立即发出。
func (r *tgReporter) Done(ctx context.Context, s Status) {
	r.mu.Lock()
	// 停掉待补发：终态发出后，任何中间态补发都是倒退。
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.pending = nil
	r.finished = true
	r.mu.Unlock()

	r.send(ctx, s)
}

// flush 是节流窗口结束时的补发：把最新的 pending 快照发出去。
func (r *tgReporter) flush(ctx context.Context) {
	r.mu.Lock()
	r.timer = nil
	if r.finished || r.pending == nil {
		r.mu.Unlock()
		return
	}
	s := *r.pending // 解引用拷一份，锁外用
	r.pending = nil
	r.mu.Unlock()

	r.send(ctx, s)
}

// send 真正发请求：首次走 SendEditable 拿 msgID，之后走 Edit。
//
// 失败只记日志、不返回错误 —— 进度是辅助信息，不该反过来把主流程搞挂。
func (r *tgReporter) send(ctx context.Context, s Status) {
	md := renderStatus(s)

	r.mu.Lock()
	msgID := r.msgID
	r.mu.Unlock()

	var err error
	if msgID == 0 {
		var id int
		id, err = r.editor.SendEditable(ctx, r.chatID, md)
		if err == nil {
			r.mu.Lock()
			r.msgID = id
			r.mu.Unlock()
		}
	} else {
		err = r.editor.Edit(ctx, r.chatID, msgID, md)
	}

	// 无论成败都更新时间戳：失败往往也是被限速了，更该等。
	r.mu.Lock()
	r.lastSent = time.Now()
	r.mu.Unlock()

	if err != nil {
		slog.ErrorContext(ctx, "音乐进度上报失败", "chat", r.chatID, "err", err)
	}
}

// renderStatus 把快照渲染成 MarkdownV2。
//
// 转义约定：整条消息是**预渲染的 MarkdownV2**，Editor 不会再整体转义，
// 所以这里必须自己把每一段动态文本用 EscapeMarkdownV2 处理掉，
// 只留下我们主动写的 * 等标记不转义。
func renderStatus(s Status) string {
	esc := tools.EscapeMarkdownV2

	// strings.Builder 比反复 += 拼字符串高效（不产生中间字符串）。
	var b strings.Builder
	b.WriteString("🎵 *" + esc(s.Query) + "*\n")

	// 搜索行：搜过才显示。阶段已经越过搜索就打勾，否则显示进行中。
	if s.Searches > 0 {
		b.WriteString(stageIcon(s.Stage > StageSearching) + " " +
			esc(fmt.Sprintf("搜索 %d 次 · %d 个候选", s.Searches, s.Found)) + "\n")
	}

	// 曲目行：选定后就有（此时 Bytes 可能还是 0，下载完才有值）。
	if s.Track != nil {
		line := fmt.Sprintf("%s - %s · %s", s.Track.Artist, s.Track.Title, s.Track.Duration)
		if s.Track.Bytes > 0 {
			line += fmt.Sprintf(" · %s", humanSize(s.Track.Bytes))
		}
		if s.Track.Source != "" {
			line += " · " + s.Track.Source
		}
		b.WriteString(stageIcon(s.Track.Bytes > 0) + " " + esc(line) + "\n")
	}

	// 上传区：每个目标一行。
	if len(s.Targets) > 0 {
		b.WriteString("⬆️ 上传\n")
		for _, t := range s.Targets {
			line := "   " + targetIcon(t.State) + " " + esc(t.Name)
			if t.Err != "" {
				line += esc(": " + tools.TruncateUTF8(t.Err, 80))
			}
			b.WriteString(line + "\n")
		}
	}

	if s.Stage == StageFailed && s.Err != "" {
		b.WriteString("❌ " + esc(tools.TruncateUTF8(s.Err, 300)) + "\n")
	}

	// token 统计只在收尾时展示，中途刷它没意义。
	if s.Track != nil && s.Track.Tokens > 0 && (s.Stage == StageDone || s.Stage == StageFailed) {
		b.WriteString(esc(fmt.Sprintf("本次消耗 tokens: %d", s.Track.Tokens)))
	}

	return b.String()
}

// stageIcon 已完成打勾，进行中显示沙漏。
func stageIcon(done bool) string {
	if done {
		return "✅"
	}
	return "⏳"
}

// targetIcon 把上传目标状态映射成图标。
func targetIcon(st TargetState) string {
	switch st {
	case TargetOK:
		return "✅"
	case TargetFailed:
		return "❌"
	case TargetRunning:
		return "⏳"
	default:
		return "⬜"
	}
}

// humanSize 把字节数渲染成人看得懂的大小。
// 1<<20 = 1048576，即 1 MiB；mp3 一般几 MB，用 MB 粒度就够。
func humanSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: PASS — 六个 `TestReporter*` / `TestRenderStatus*` 全绿，Task 3 的用例不回归。

- [ ] **Step 5: 竞态检查**

Run: `go test -race ./internal/music -run TestReporter -v`
Expected: PASS，无 `DATA RACE` 报告。节流用的 `time.AfterFunc` 在别的 goroutine 里回调，这一步是必须的。

- [ ] **Step 6: 提交**

```bash
git add internal/music/report.go internal/music/report_test.go
git commit -m "#feat music 新增进度快照模型与 Telegram 节流上报

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 5: WebDAV 并发上传与文件名清洗

**Files:**
- Create: `internal/music/upload.go`
- Test: `internal/music/upload_test.go`

**Interfaces:**
- Consumes: `TargetStatus` / `TargetState`（Task 4）
- Produces:
  ```go
  type Target struct { Name string; URL string }

  func NewUploader(httpClient *http.Client, targets []Target, user, pass string) *Uploader

  // Upload 并发把 localPath 传到所有目标。每当某个目标状态变化就用最新的
  // 完整状态切片回调 onProgress（onProgress 可为 nil）。
  // 返回最终状态切片；error 仅在**所有**目标都失败时非 nil。
  func (u *Uploader) Upload(ctx context.Context, localPath, filename string,
      onProgress func([]TargetStatus)) ([]TargetStatus, error)

  func BuildFilename(artist, title string) string // "周杰伦 - 晴天.mp3"
  ```
  Task 7 的 Runner 消费这两个。

- [ ] **Step 1: 写失败的测试**

创建 `internal/music/upload_test.go`：

```go
package music

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// writeTempMP3 在 t.TempDir() 里造一个假 mp3 文件，返回路径。
// t.TempDir() 给每个测试独立目录，结束后由 testing 包自动清理。
func writeTempMP3(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "download.mp3")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时 mp3 失败: %v", err)
	}
	return p
}

// davRecorder 是一个假 WebDAV 服务端，记录收到的 PUT。
type davRecorder struct {
	mu      sync.Mutex
	paths   []string // 收到的请求路径
	bodies  []string // 收到的请求体
	authOK  bool     // 是否所有请求都带对了 Basic Auth
	status  int      // 返回的状态码，0 视作 201
}

func (d *davRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		d.mu.Lock()
		d.paths = append(d.paths, r.URL.Path)
		d.bodies = append(d.bodies, string(body))
		d.authOK = ok && user == "alist" && pass == "pw"
		code := d.status
		d.mu.Unlock()
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}
}

// TestUploadBothSucceed 验证：两个目标都成功时无错误，两边都收到了文件。
func TestUploadBothSucceed(t *testing.T) {
	rec := &davRecorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "阿里云盘", URL: srv.URL + "/dav/aliyun/Music"},
		{Name: "OneDrive", URL: srv.URL + "/dav/onedrive/Music"},
	}, "alist", "pw")

	path := writeTempMP3(t, "ID3fake")
	got, err := u.Upload(context.Background(), path, "周杰伦 - 晴天.mp3", nil)
	if err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("返回 %d 个目标状态, want 2", len(got))
	}
	for _, s := range got {
		if s.State != TargetOK {
			t.Errorf("目标 %s 状态 = %d, want TargetOK；err=%s", s.Name, s.State, s.Err)
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) != 2 {
		t.Fatalf("服务端收到 %d 个 PUT, want 2", len(rec.paths))
	}
	if !rec.authOK {
		t.Error("请求未携带正确的 Basic Auth")
	}
	for _, b := range rec.bodies {
		if b != "ID3fake" {
			t.Errorf("收到的内容 = %q, want %q", b, "ID3fake")
		}
	}
	// 两个目标各自的路径前缀必须不同，且文件名被 URL 转义过（含中文和空格）。
	joined := strings.Join(rec.paths, " ")
	if !strings.Contains(joined, "/dav/aliyun/Music/") || !strings.Contains(joined, "/dav/onedrive/Music/") {
		t.Errorf("PUT 路径不对: %v", rec.paths)
	}
}

// TestUploadPartialFailure 验证：一成一败仍算任务成功，且失败原因带在对应目标上。
// 这是「不为一个网盘挂掉丢弃已下好的歌」这条设计的守门测试。
func TestUploadPartialFailure(t *testing.T) {
	okSrv := httptest.NewServer((&davRecorder{}).handler())
	t.Cleanup(okSrv.Close)
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusInsufficientStorage}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(okSrv.Client(), []Target{
		{Name: "阿里云盘", URL: okSrv.URL + "/dav"},
		{Name: "OneDrive", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", nil)
	if err != nil {
		t.Fatalf("一成一败应算成功，却返回错误: %v", err)
	}
	if got[0].State != TargetOK {
		t.Errorf("阿里云盘状态 = %d, want TargetOK", got[0].State)
	}
	if got[1].State != TargetFailed {
		t.Fatalf("OneDrive 状态 = %d, want TargetFailed", got[1].State)
	}
	if !strings.Contains(got[1].Err, "507") {
		t.Errorf("OneDrive 错误信息 %q 应包含状态码 507", got[1].Err)
	}
}

// TestUploadAllFail 验证：全失败才算任务失败。
func TestUploadAllFail(t *testing.T) {
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusUnauthorized}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(badSrv.Client(), []Target{
		{Name: "A", URL: badSrv.URL + "/dav"},
		{Name: "B", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", nil)
	if err == nil {
		t.Fatal("全部目标失败时应返回错误")
	}
	for _, s := range got {
		if s.State != TargetFailed {
			t.Errorf("目标 %s 状态 = %d, want TargetFailed", s.Name, s.State)
		}
	}
}

// TestUploadProgressCallback 验证：每个目标完成都会回调一次，且回调拿到的是
// 完整状态切片（而不是单个目标）—— Reporter 要靠它渲染整块上传区。
func TestUploadProgressCallback(t *testing.T) {
	srv := httptest.NewServer((&davRecorder{}).handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "A", URL: srv.URL + "/dav"},
		{Name: "B", URL: srv.URL + "/dav"},
	}, "alist", "pw")

	var mu sync.Mutex
	var calls int
	_, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", func(ts []TargetStatus) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if len(ts) != 2 {
			t.Errorf("回调收到 %d 个目标状态, want 2（应为完整切片）", len(ts))
		}
	})
	if err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// 初始一次 + 每个目标完成各一次 = 3 次。
	if calls != 3 {
		t.Errorf("onProgress 被调用 %d 次, want 3（初始 1 次 + 两个目标各 1 次）", calls)
	}
}

// TestBuildFilename 锁死文件名清洗：路径分隔符必须被替换，否则 WebDAV 路径会被打歪。
func TestBuildFilename(t *testing.T) {
	cases := []struct {
		name   string
		artist string
		title  string
		want   string
	}{
		{"普通中文", "周杰伦", "晴天", "周杰伦 - 晴天.mp3"},
		{"歌手名带斜杠", "AC/DC", "Back in Black", "AC_DC - Back in Black.mp3"},
		{"歌名带全套危险字符", "A", `a/b\c:d*e?f"g<h>i|j`, "A - a_b_c_d_e_f_g_h_i_j.mp3"},
		{"换行被换成空格", "A", "a\nb", "A - a b.mp3"},
		{"两端空白被裁掉", "  周杰伦  ", "  晴天  ", "周杰伦 - 晴天.mp3"},
		{"歌手歌名全空 → 兜底", "", "", "unknown.mp3"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := BuildFilename(c.artist, c.title); got != c.want {
				t.Errorf("BuildFilename(%q, %q) = %q, want %q", c.artist, c.title, got, c.want)
			}
		})
	}
}

// TestBuildFilenameTruncatesByRune 验证超长歌名按 rune 截断，不产生乱码。
// 按字节切会把一个中文字符切成两半，得到 invalid UTF-8。
func TestBuildFilenameTruncatesByRune(t *testing.T) {
	long := strings.Repeat("很长的歌名", 100) // 500 个中文字符
	got := BuildFilename("歌手", long)

	if !strings.HasSuffix(got, ".mp3") {
		t.Fatalf("结果应以 .mp3 结尾: %q", got)
	}
	base := strings.TrimSuffix(got, ".mp3")
	if n := len([]rune(base)); n > maxFilenameRunes {
		t.Errorf("截断后 %d 个字符, want <= %d", n, maxFilenameRunes)
	}
	// 按字节切会把一个中文字符切成两半，产生非法 UTF-8；按 rune 切不会。
	if !utf8.ValidString(base) {
		t.Errorf("截断后不是合法 UTF-8: %q", base)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/music -run "TestUpload|TestBuildFilename" -v`
Expected: FAIL — 编译不过，`undefined: NewUploader` / `undefined: BuildFilename`。

- [ ] **Step 3: 写 `internal/music/upload.go`**

```go
package music

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cheedonghu/news2tg/internal/tools"
)

// targetUploadTimeout 是**每个**目标的上传超时。
// alist 转发到网盘可能很慢，给宽松一点；两个目标是并发跑的，不会叠加。
const targetUploadTimeout = 300 * time.Second

// maxFilenameRunes 是文件名主体（不含 .mp3）的最大字符数。
// 按 rune 而不是字节算：多数文件系统和网盘的限制是 255 字节，
// 中文一个字 3 字节，120 个字符最多 360 字节 —— 还是可能超，
// 但配合各家网盘普遍更宽松的实际限制，这个值足够安全且不至于砍掉正常歌名。
const maxFilenameRunes = 120

// Target 是一个 WebDAV 上传目标。
type Target struct {
	Name string // 展示名，如 "阿里云盘"，只用于进度消息
	URL  string // 目录地址，如 http://alist:5244/dav/aliyun/Music
}

// Uploader 把本地文件并发 PUT 到所有目标。
//
// 注意：内部是按 []Target 循环的，不硬编码"两个"。
// 配置层之所以是六个平铺字段，只是因为需求明确说了不做动态列表；
// 这里保持切片形态，加第三个网盘时这个文件一行都不用改。
type Uploader struct {
	httpClient *http.Client
	targets    []Target
	user       string
	pass       string
	timeout    time.Duration // 抽成字段而非直接用常量，方便测试压短
}

// NewUploader 构造函数。user/pass 是所有目标共用的 WebDAV 凭据
// （alist 单实例挂多个网盘，账号是同一个）。
func NewUploader(httpClient *http.Client, targets []Target, user, pass string) *Uploader {
	return &Uploader{
		httpClient: httpClient,
		targets:    targets,
		user:       user,
		pass:       pass,
		timeout:    targetUploadTimeout,
	}
}

// Upload 并发把 localPath 传到所有目标。
//
// onProgress 在初始时以及**每个目标完成时**各回调一次，参数是当前所有目标状态的
// 快照副本（不是内部切片本身，调用方随便读不用担心竞态）。可传 nil。
//
// 返回值：最终状态切片总是返回（哪怕全失败，调用方要靠它渲染每个目标的成败）；
// error 仅在**所有**目标都失败时才非 nil —— 至少一个成功就算任务成功，
// 延续仓库"AI/digest 失败不阻断推送"的既有约定，不为一个网盘挂掉丢弃已下好的歌。
func (u *Uploader) Upload(ctx context.Context, localPath, filename string, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	states := make([]TargetStatus, len(u.targets))
	for i, t := range u.targets {
		states[i] = TargetStatus{Name: t.Name, State: TargetRunning}
	}

	// mu 保护 states：多个上传 goroutine 会并发改各自那一格。
	var mu sync.Mutex
	// notify 拷一份快照再回调，避免调用方拿着内部切片在锁外读，产生数据竞态。
	notify := func() {
		mu.Lock()
		// append(nil, s...) 是 Go 里拷贝切片的惯用写法。
		snapshot := append([]TargetStatus(nil), states...)
		mu.Unlock()
		if onProgress != nil {
			onProgress(snapshot)
		}
	}
	notify() // 初始快照：让进度消息立刻显示出"上传中"的骨架

	// sync.WaitGroup 是计数器：Add(n) 加，Done() 减 1，Wait() 阻塞到归零。
	var wg sync.WaitGroup
	for i, t := range u.targets {
		wg.Add(1)
		// Go 1.22 起 for-range 变量每轮都是新变量，不必再手动 shadow；
		// 但显式传参更直白，也和仓库里其它并发代码的谨慎风格一致。
		go func(idx int, tg Target) {
			defer wg.Done()

			// 每个目标独立超时：一个网盘卡住不该拖垮另一个。
			cctx, cancel := context.WithTimeout(ctx, u.timeout)
			defer cancel()

			err := u.putOne(cctx, tg, localPath, filename)

			mu.Lock()
			if err != nil {
				states[idx].State = TargetFailed
				states[idx].Err = err.Error()
			} else {
				states[idx].State = TargetOK
			}
			mu.Unlock()

			if err != nil {
				slog.ErrorContext(ctx, "WebDAV 上传失败", "target", tg.Name, "file", filename, "err", err)
			} else {
				slog.InfoContext(ctx, "WebDAV 上传成功", "target", tg.Name, "file", filename)
			}
			notify()
		}(i, t)
	}
	wg.Wait()

	mu.Lock()
	final := append([]TargetStatus(nil), states...)
	mu.Unlock()

	okCount := 0
	for _, s := range final {
		if s.State == TargetOK {
			okCount++
		}
	}
	if okCount == 0 {
		return final, fmt.Errorf("全部 %d 个 WebDAV 目标上传失败", len(final))
	}
	return final, nil
}

// putOne 把本地文件 PUT 到单个目标。
func (u *Uploader) putOne(ctx context.Context, t Target, localPath, filename string) error {
	// 每个目标各自打开一次文件：*os.File 内部有共享的读偏移量，
	// 多个 goroutine 拿同一个 *os.File 当 body 会互相把对方的读位置搅乱。
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("读取本地文件信息失败: %w", err)
	}

	// url.PathEscape 转义文件名里的空格、中文等，避免拼出非法 URL。
	// TrimRight 去掉配置里可能多写的尾斜杠，防止出现 //。
	dst := strings.TrimRight(t.URL, "/") + "/" + url.PathEscape(filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, dst, f)
	if err != nil {
		return err
	}
	// 显式给出长度：不设的话 net/http 会走 chunked 传输，
	// 部分 WebDAV 服务端（含某些 alist 后端）会直接拒绝。
	req.ContentLength = fi.Size()
	req.SetBasicAuth(u.user, u.pass)
	req.Header.Set("Content-Type", "audio/mpeg")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV PUT 请求失败: %w", err)
	}
	defer resp.Body.Close()
	// 把响应体读干净才能让连接回到连接池复用；PUT 的响应体通常是空的，丢掉即可。
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV 返回 %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

// filenameReplacer 把文件系统 / URL 路径里的危险字符换掉。
// 换行单独换成空格而不是下划线：它多半来自模型输出的意外折行，换空格更自然。
var filenameReplacer = strings.NewReplacer(
	"/", "_",
	`\`, "_",
	":", "_",
	"*", "_",
	"?", "_",
	`"`, "_",
	"<", "_",
	">", "_",
	"|", "_",
	"\n", " ",
	"\r", " ",
)

// BuildFilename 拼出 "<歌手> - <歌名>.mp3"。
//
// 为什么必须清洗：歌名里的 '/' 会被 WebDAV 当成路径分隔符，
// 把文件写到一个意料之外的子目录里（甚至 404）。其余字符是 Windows
// 文件名非法字符，网盘客户端同步下来会出问题。
func BuildFilename(artist, title string) string {
	a := strings.TrimSpace(filenameReplacer.Replace(artist))
	tt := strings.TrimSpace(filenameReplacer.Replace(title))
	base := strings.TrimSpace(a + " - " + tt)

	// 歌手和歌名都空时会剩下一个孤零零的 "-"，兜个底避免出现 " - .mp3"。
	if strings.Trim(base, " -") == "" {
		base = "unknown"
	}
	// TruncateUTF8 按 rune 截断，绝不会把一个中文字符切成两半。
	return tools.TruncateUTF8(base, maxFilenameRunes) + ".mp3"
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: PASS — 全部 `TestUpload*` / `TestBuildFilename*` 绿，Task 3/4 的用例不回归。

- [ ] **Step 5: 竞态检查**

Run: `go test -race ./internal/music -run TestUpload -v`
Expected: PASS，无 `DATA RACE`。上传是多 goroutine 并发改同一个切片，这一步必须做。

- [ ] **Step 6: 提交**

```bash
git add internal/music/upload.go internal/music/upload_test.go
git commit -m "#feat music 新增 WebDAV 并发上传与文件名清洗

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 6: LLM 工具调用循环

**Files:**
- Create: `internal/music/agent.go`
- Test: `internal/music/agent_test.go`

**Interfaces:**
- Consumes: `Source`/`Candidate`（Task 3）、`Status`/`Track`/`Reporter`/`Stage`（Task 4）
- Produces:
  ```go
  func NewAgent(apiKey, model string, sources []Source) *Agent

  // Fetch 跑工具调用循环。st 由调用方创建并持有（Query 已填好），
  // agent 直接在上面改 Searches/Found/Stage/Track 并灌给 rep ——
  // 这样 agent 和 Runner 共享同一份进度真相，不会各记一份。
  // dir 的创建与清理由调用方负责。
  func (a *Agent) Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error)
  ```
  Task 7 的 Runner 通过一个本地 `fetcher` 接口消费它；Task 9 的 `main.go` 调 `NewAgent`。

- [ ] **Step 1: 写失败的测试**

创建 `internal/music/agent_test.go`：

```go
package music

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	openai "github.com/sashabaranov/go-openai"
)

// fakeLLM 按预设脚本逐轮返回响应，不发任何网络请求。
// scripts[i] 是第 i 轮 CreateChatCompletion 的返回。
type fakeLLM struct {
	scripts []openai.ChatCompletionResponse
	calls   int
	err     error // 非 nil 时直接返回错误
}

func (f *fakeLLM) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if f.err != nil {
		return openai.ChatCompletionResponse{}, f.err
	}
	if f.calls >= len(f.scripts) {
		return openai.ChatCompletionResponse{}, errors.New("fakeLLM 脚本用尽")
	}
	resp := f.scripts[f.calls]
	f.calls++
	return resp, nil
}

// toolCallResp 造一个"模型要求调工具"的响应。
func toolCallResp(id, name, args string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{{
				ID:       id,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: name, Arguments: args},
			}},
		}}},
		Usage: openai.Usage{TotalTokens: 100},
	}
}

// finalResp 造一个"模型给出最终回答"的响应。
func finalResp(content string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: content,
		}}},
		Usage: openai.Usage{TotalTokens: 50},
	}
}

// fakeSource 是可编程的假音源。
type fakeSource struct {
	name      string
	results   map[string][]Candidate // 关键词 → 候选；未命中的关键词返回零结果
	searchErr error
	payload   string // Download 写出的内容
	dlErr     error
	dlCalls   []string // 记录被下载的 id
}

func (s *fakeSource) Name() string { return s.name }
func (s *fakeSource) Hint() string { return "测试音源" }

func (s *fakeSource) Search(_ context.Context, query string) ([]Candidate, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.results[query], nil
}

func (s *fakeSource) Download(_ context.Context, c Candidate, w io.Writer) (int64, error) {
	s.dlCalls = append(s.dlCalls, c.ID)
	if s.dlErr != nil {
		return 0, s.dlErr
	}
	n, err := io.WriteString(w, s.payload)
	return int64(n), err
}

// nopReporter 是不做任何事的 Reporter，用于不关心进度的用例。
type nopReporter struct{ snapshots []Status }

func (r *nopReporter) Update(_ context.Context, s Status) { r.snapshots = append(r.snapshots, s) }
func (r *nopReporter) Done(_ context.Context, s Status)   { r.snapshots = append(r.snapshots, s) }

// newTestAgent 组装一个注入了 fake 的 agent。
func newTestAgent(t *testing.T, llm chatCompleter, src Source) *Agent {
	t.Helper()
	a := NewAgent("test-key", "test-model", []Source{src})
	a.llm = llm // 覆盖掉真实客户端，绝不发网络请求
	return a
}

// TestAgentHappyPath：搜一次就搜中，下载，输出 JSON。
func TestAgentHappyPath(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "ID3fake-bytes",
		results: map[string][]Candidate{
			"Jay Chou Qing Tian": {{ID: "1", Artist: "Jay Chou", Title: "Qing Tian", Duration: "03:58", dlURL: "u"}},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"Jay Chou Qing Tian"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	st := &Status{Query: "晴天 周杰伦"}
	rep := &nopReporter{}

	track, err := a.Fetch(context.Background(), st, t.TempDir(), rep)
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}
	if track.Artist != "周杰伦" || track.Title != "晴天" {
		t.Errorf("规范化结果 = %q - %q, want 周杰伦 - 晴天", track.Artist, track.Title)
	}
	if track.Duration != "03:58" {
		t.Errorf("Duration = %q, want 03:58", track.Duration)
	}
	if track.Bytes != int64(len(src.payload)) {
		t.Errorf("Bytes = %d, want %d", track.Bytes, len(src.payload))
	}
	// token 是逐轮累加的：100 + 100 + 50。
	if track.Tokens != 250 {
		t.Errorf("Tokens = %d, want 250", track.Tokens)
	}
	// 文件真的落盘了吗？
	b, rErr := os.ReadFile(track.LocalPath)
	if rErr != nil {
		t.Fatalf("读下载文件失败: %v", rErr)
	}
	if string(b) != src.payload {
		t.Errorf("落盘内容 = %q, want %q", b, src.payload)
	}
	if st.Searches != 1 {
		t.Errorf("st.Searches = %d, want 1", st.Searches)
	}
}

// TestAgentRetriesAfterZeroResults：第一次搜中文零结果，模型换成拼音再搜就中。
// 这是 mp3.pm 搜中文歌的常规路径，必须覆盖。
func TestAgentRetriesAfterZeroResults(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "bytes",
		results: map[string][]Candidate{
			// 只有拼音关键词有结果；"周杰伦 晴天" 命不中 → 零结果
			"Jay Chou Qing Tian": {{ID: "7", Artist: "Jay Chou", Title: "Qing Tian", Duration: "03:58", dlURL: "u"}},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"周杰伦 晴天"}`),
		toolCallResp("c2", "search_fake", `{"query":"Jay Chou Qing Tian"}`),
		toolCallResp("c3", "download_fake", `{"id":"7"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"7"}`),
	}}

	a := newTestAgent(t, llm, src)
	st := &Status{Query: "晴天"}
	track, err := a.Fetch(context.Background(), st, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}
	if track.Title != "晴天" {
		t.Errorf("Title = %q, want 晴天", track.Title)
	}
	if st.Searches != 2 {
		t.Errorf("st.Searches = %d, want 2（换词重搜过一次）", st.Searches)
	}
}

// TestAgentDownloadFailureThenAnotherCandidate：下载失败后模型改选另一个候选。
func TestAgentDownloadFailureThenAnotherCandidate(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "bytes",
		dlErr:   errors.New("403 forbidden"),
		results: map[string][]Candidate{
			"q": {
				{ID: "1", Artist: "A", Title: "T1", Duration: "01:00", dlURL: "u1"},
				{ID: "2", Artist: "A", Title: "T2", Duration: "02:00", dlURL: "u2"},
			},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`), // 失败
		toolCallResp("c3", "download_fake", `{"id":"2"}`), // 仍失败（dlErr 是常驻的）
		finalResp(`{"artist":"A","title":"T2","id":"2"}`),
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	// 两次下载都失败 → 最终没有 track，必须报错而不是拿着空文件继续。
	if err == nil {
		t.Fatal("下载全失败时应报错")
	}
	// 关键断言：失败被回灌给模型了，所以它有机会尝试第二个候选。
	if len(src.dlCalls) != 2 {
		t.Errorf("下载被调用 %d 次, want 2（第一次失败后模型应能改选）", len(src.dlCalls))
	}
}

// TestAgentTooLarge：超过 50MB 上限时中断下载并删掉半成品。
func TestAgentTooLarge(t *testing.T) {
	// payload 比上限大一点点即可触发；不用真造 50MB，把 agent 的上限压小。
	src := &fakeSource{
		name:    "fake",
		payload: strings.Repeat("x", 1024),
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	a.maxBytes = 100 // 压到 100 字节，payload 1024 字节必定超限

	dir := t.TempDir()
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, dir, &nopReporter{})
	if err == nil {
		t.Fatal("下载超限后没有可用文件，应报错")
	}
	// 半成品必须被删掉，不能在临时目录里留个残缺 mp3。
	entries, rErr := os.ReadDir(dir)
	if rErr != nil {
		t.Fatalf("读临时目录失败: %v", rErr)
	}
	if len(entries) != 0 {
		t.Errorf("临时目录应为空（半成品被删），实际有 %d 个文件", len(entries))
	}
}

// TestAgentNonJSONFinal：模型最终输出不是 JSON → 任务失败，错误里带上原始输出。
func TestAgentNonJSONFinal(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp("我下载好了，这首歌很好听！"),
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("非 JSON 输出应报错")
	}
	if !strings.Contains(err.Error(), "很好听") {
		t.Errorf("错误信息应带上模型原始输出便于排查，实际: %v", err)
	}
}

// TestAgentNoDownloadBeforeFinal：模型没下载就给结论 → 失败（没有文件可传）。
func TestAgentNoDownloadBeforeFinal(t *testing.T) {
	src := &fakeSource{
		name: "fake",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	if _, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{}); err == nil {
		t.Fatal("没调下载工具就给结论时应报错")
	}
}

// TestAgentIDMismatch：最终 JSON 的 id 与实际下载的不一致 → 失败。
// 模型自相矛盾时，文件名与文件内容大概率对不上，宁可失败也不要传错东西。
func TestAgentIDMismatch(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {
			{ID: "1", Artist: "A", Title: "T1", dlURL: "u1"},
			{ID: "2", Artist: "A", Title: "T2", dlURL: "u2"},
		}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"A","title":"T2","id":"2"}`), // 说下了 2，实际下的是 1
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("id 不一致时应报错")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Errorf("错误信息应说明是 id 不一致，实际: %v", err)
	}
}

// TestAgentMaxStepsExhausted：模型反复调工具不收敛 → 报"未收敛"。
func TestAgentMaxStepsExhausted(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		results: map[string][]Candidate{},
	}
	// 每轮都只搜索、从不收敛；脚本给足 maxSteps 轮。
	scripts := make([]openai.ChatCompletionResponse, defaultMaxSteps)
	for i := range scripts {
		scripts[i] = toolCallResp("c", "search_fake", `{"query":"q"}`)
	}
	llm := &fakeLLM{scripts: scripts}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("超过最大步数时应报错")
	}
	if !strings.Contains(err.Error(), "未收敛") {
		t.Errorf("错误信息应说明未收敛，实际: %v", err)
	}
}

// TestAgentUnknownCandidateID：模型编了个不存在的 id → 回灌提示而非崩溃。
func TestAgentUnknownCandidateID(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"999"}`), // 编的
		toolCallResp("c3", "download_fake", `{"id":"1"}`),   // 改正
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	track, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("模型自我纠正后应成功: %v", err)
	}
	if track.Title != "T" {
		t.Errorf("Title = %q, want T", track.Title)
	}
	// 只有合法的那次真正走到了音源。
	if len(src.dlCalls) != 1 {
		t.Errorf("下载被调用 %d 次, want 1（不存在的 id 不该打到音源）", len(src.dlCalls))
	}
}

// TestBuildToolDefs 验证：每个源自动生成两个工具，名字按约定拼。
func TestBuildToolDefs(t *testing.T) {
	defs := buildToolDefs([]Source{&fakeSource{name: "alpha"}, &fakeSource{name: "beta"}})
	if len(defs) != 4 {
		t.Fatalf("2 个源应生成 4 个工具，实际 %d 个", len(defs))
	}
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	for _, want := range []string{"search_alpha", "download_alpha", "search_beta", "download_beta"} {
		if !names[want] {
			t.Errorf("缺少工具 %q，实际有: %v", want, names)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/music -run TestAgent -v`
Expected: FAIL — 编译不过，`undefined: NewAgent` / `undefined: chatCompleter`。

- [ ] **Step 3: 写 `internal/music/agent.go`**

```go
package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/cheedonghu/news2tg/internal/tools"
)

const (
	// defaultMaxSteps 限制 agent 循环步数，防止模型反复调工具不收敛。
	// 比 internal/agent 的 3 大：这里要容纳"搜索 → 零结果 → 换词搜 → 再换词搜 → 下载 → 给结论"。
	defaultMaxSteps = 6
	// defaultMaxBytes 是单曲下载大小上限，防御模型选中一个错误条目把磁盘写爆。
	// 50 << 20 = 50 MiB；正常 mp3 在 3-15 MB 之间。
	defaultMaxBytes = 50 << 20
	// maxCandidates 是一次搜索回给模型的候选数上限。
	// 搜出上百条时全塞进上下文纯属烧 token，前 20 条足够模型挑。
	maxCandidates = 20
	// downloadFileName 是临时目录里的固定文件名。
	// 用固定名而非歌名：此刻还没拿到模型规范化后的名字，
	// 真正的文件名在上传时由 BuildFilename 决定。
	downloadFileName = "download.mp3"
)

// errTooLarge 是下载超限的哨兵错误，供 errors.Is 判别。
var errTooLarge = errors.New("下载内容超过大小上限")

// chatCompleter 抽象 LLM 聊天补全调用：*openai.Client 天然满足，
// 测试时注入 fake，避免真发网络请求。同 internal/agent 的做法。
type chatCompleter interface {
	CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// Agent 持有 LLM 客户端、模型名、音源注册表和工具定义。
type Agent struct {
	llm       chatCompleter
	model     string
	sources   map[string]Source // 源名 → 音源
	toolDefs  []openai.Tool     // 传给模型的工具 schema
	sysPrompt string
	maxSteps  int
	maxBytes  int64 // 抽成字段而非直接用常量，方便测试压小
}

// NewAgent 构造函数。
// apiKey 复用 DeepSeek 的 key；model 由 main 从 [deepseek] agent_model 传入，
// 必须支持 function calling，否则下面的 toolDefs 形同虚设。
func NewAgent(apiKey, model string, sources []Source) *Agent {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = "https://api.deepseek.com/v1" // 同 ai/deepseek.go：DeepSeek 兼容 OpenAI 协议

	// 建注册表：工具名里的源标识 → 音源实例。
	reg := make(map[string]Source, len(sources))
	for _, s := range sources {
		reg[s.Name()] = s
	}

	return &Agent{
		llm:       openai.NewClientWithConfig(cfg),
		model:     model,
		sources:   reg,
		toolDefs:  buildToolDefs(sources),
		sysPrompt: buildSystemPrompt(sources),
		maxSteps:  defaultMaxSteps,
		maxBytes:  defaultMaxBytes,
	}
}

// buildSystemPrompt 拼系统提示词。
// 各音源的特性说明（Hint）在这里被收集进来 —— "某个站怎么搜才有效"这条知识
// 跟着站点实现走，加音源时提示词自动跟着长，不用手改这段。
func buildSystemPrompt(sources []Source) string {
	var b strings.Builder
	b.WriteString(`你是一个音乐下载助手。用户会用自由格式描述想要的歌（如「晴天 - 周杰伦」「周杰伦 晴天」「晴天」「Jay Chou 的晴天」），你需要理解它，从音源里搜索、挑出正确的曲目并下载。

工具使用规则：
1. 先用 search_<源名> 搜索，返回的候选每条含 id / artist / title / duration。
2. 如果零结果，按下面该音源的特性说明换关键词重试（拼音、英文译名、只用歌名不带歌手）。整个任务最多搜索 3 次。
3. 从候选里挑最匹配用户意图的那一条。避开 live 版、伴奏/instrumental、翻唱(cover)、remix、加速/慢放版本，除非用户明确要。
4. 选定后必须调用同一个源的 download_<源名>，参数 id 取自候选列表。
5. 下载成功后不要再调用任何工具，直接输出一个 JSON 对象作为最终回答，格式严格如下：
   {"artist": "歌手中文原名", "title": "歌名中文原名", "id": "刚才下载的那个 id"}
   artist 和 title 必须是规范化后的原名：音源里的罗马化写法（如 "Jay Chou" / "Kai Bu Liao Kou"）
   要还原成中文原名（"周杰伦" / "开不了口"）；本来就是英文歌的保持英文即可。
   只输出这个 JSON，不要有任何其它文字、解释或 markdown 代码块。

可用音源：
`)
	for _, s := range sources {
		b.WriteString("- " + s.Name() + "：" + s.Hint() + "\n")
	}
	return b.String()
}

// buildToolDefs 为每个音源生成 search_<名> / download_<名> 两个工具的 schema。
// 这就是"加音源不用改 agent 循环"的兑现处：注册表和工具定义都由 Source 列表推导出来。
func buildToolDefs(sources []Source) []openai.Tool {
	// 参数 schema 用 json.RawMessage 直接写，FunctionDefinition.Parameters 接受 any。
	searchParams := json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": { "type": "string", "description": "搜索关键词，如歌名、歌名+歌手、拼音或英文译名" }
		},
		"required": ["query"]
	}`)
	idParams := json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": { "type": "string", "description": "搜索结果里某条候选的 id，必须原样照抄" }
		},
		"required": ["id"]
	}`)

	defs := make([]openai.Tool, 0, len(sources)*2)
	for _, s := range sources {
		name := s.Name()
		defs = append(defs,
			openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "search_" + name,
				Description: "在 " + name + " 搜索歌曲，返回候选列表。" + s.Hint(),
				Parameters:  searchParams,
			}},
			openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "download_" + name,
				Description: "从 " + name + " 下载指定 id 的歌曲到本地。id 必须来自 search_" + name + " 的返回，不能自己编。",
				Parameters:  idParams,
			}},
		)
	}
	return defs
}

// run 是**一次** Fetch 的局部状态。
//
// 关键设计：候选表、已下载 track 等都挂在这里而不是 Agent 上，
// 所以同一个 Agent 可以被多个并发的 /music 任务共用而互不干扰 ——
// 每次 Fetch 都新建一个 run，随任务结束丢弃。
type run struct {
	agent        *Agent
	dir          string               // 下载落盘目录，由调用方创建与清理
	rep          Reporter             //
	st           *Status              // 与 Runner 共享的同一份进度快照
	candidates   map[string]Candidate // "源名:id" → 候选（直链只在这里，不进模型上下文）
	track        *Track               // 下载成功后才非 nil
	downloadedID string               // 实际下载的候选 id，用于校验模型最终输出
}

// Fetch 跑工具调用循环：理解描述 → 搜索 → 选曲 → 下载 → 拿到规范化命名。
//
// st 由调用方创建并持有（Query 已填好）：agent 直接在这份快照上改
// Searches/Found/Stage/Track，Runner 后续接着改上传状态 ——
// 全程只有一份进度真相，不会出现"agent 记了搜索次数、Runner 那份是 0"的错位。
//
// dir 的创建与清理归调用方（Runner 用 os.MkdirTemp + defer os.RemoveAll）。
func (a *Agent) Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error) {
	r := &run{
		agent:      a,
		dir:        dir,
		rep:        rep,
		st:         st,
		candidates: make(map[string]Candidate),
	}
	st.Stage = StageSearching
	rep.Update(ctx, *st)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: a.sysPrompt},
		{Role: openai.ChatMessageRoleUser, Content: "请帮我下载这首歌：" + st.Query},
	}

	// 累计本次任务的 token 消耗：一次 Fetch 会多轮调模型，逐轮累加 usage.total。
	totalTokens := 0

	for step := 0; step < a.maxSteps; step++ {
		resp, err := a.llm.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    a.model,
			Messages: messages,
			Tools:    a.toolDefs,
		})
		if err != nil {
			return nil, fmt.Errorf("音乐 agent 调用大模型失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("音乐 agent 大模型返回空 choices")
		}
		totalTokens += resp.Usage.TotalTokens

		msg := resp.Choices[0].Message
		// 把本轮 assistant 消息（可能带 ToolCalls）追加进上下文。
		messages = append(messages, msg)

		// 没有工具调用 = 模型给出了最终回答。
		if len(msg.ToolCalls) == 0 {
			// 返回前把整段对话序列化打印，便于审计。注意 dump 的是 messages 而不是
			// 单次 resp —— resp 只有最后一轮消息，拿不到工具调用链和候选列表。
			if raw, mErr := json.Marshal(messages); mErr != nil {
				slog.ErrorContext(ctx, "音乐 agent 对话审计序列化失败", "err", mErr)
			} else {
				slog.InfoContext(ctx, "音乐 agent 大模型交互对话审计", "tokens", totalTokens, "conversation", string(raw))
			}
			return r.finalize(msg.Content, totalTokens)
		}

		// 逐个执行工具调用，把结果（或失败说明）作为 tool 消息回灌。
		for _, tc := range msg.ToolCalls {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: tc.ID,
				Content:    r.runTool(ctx, tc),
			})
		}
	}

	return nil, fmt.Errorf("音乐 agent 超过最大步数(%d)仍未给出结果，模型未收敛", a.maxSteps)
}

// finalize 校验并解析模型的最终 JSON 输出，产出 Track。
func (r *run) finalize(content string, tokens int) (*Track, error) {
	// 没下载过就给结论 = 没有文件可传，直接失败。
	if r.track == nil {
		return nil, fmt.Errorf("模型未调用下载工具就给出了结论：%s", tools.TruncateUTF8(content, 200))
	}

	var out struct {
		Artist string `json:"artist"`
		Title  string `json:"title"`
		ID     string `json:"id"`
	}
	// 有些模型爱把 JSON 包在 ```json 代码块里，虽然提示词禁止了，仍顺手剥一层。
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &out); err != nil {
		return nil, fmt.Errorf("模型最终输出不是合法 JSON（%v）：%s", err, tools.TruncateUTF8(content, 200))
	}
	if strings.TrimSpace(out.Artist) == "" || strings.TrimSpace(out.Title) == "" {
		return nil, fmt.Errorf("模型最终输出缺少 artist/title：%s", tools.TruncateUTF8(content, 200))
	}
	// id 对不上说明模型自相矛盾，文件名与文件内容大概率对不上，宁可失败也别传错东西。
	if out.ID != r.downloadedID {
		return nil, fmt.Errorf("模型最终输出的 id %q 与实际下载的 %q 不一致", out.ID, r.downloadedID)
	}

	r.track.Artist = strings.TrimSpace(out.Artist)
	r.track.Title = strings.TrimSpace(out.Title)
	r.track.Tokens = tokens
	return r.track, nil
}

// stripCodeFence 剥掉可能存在的 ```json ... ``` 包裹。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 去掉首行（``` 或 ```json）和末尾的 ```
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// runTool 把工具调用派发到搜索或下载。
// 工具名的形状是 search_<源名> / download_<源名>，所以按前缀切一刀就知道去哪。
func (r *run) runTool(ctx context.Context, tc openai.ToolCall) string {
	name := tc.Function.Name
	switch {
	case strings.HasPrefix(name, "search_"):
		return r.doSearch(ctx, strings.TrimPrefix(name, "search_"), tc.Function.Arguments)
	case strings.HasPrefix(name, "download_"):
		return r.doDownload(ctx, strings.TrimPrefix(name, "download_"), tc.Function.Arguments)
	}
	return fmt.Sprintf("未知工具 %q。请使用 search_<源名> 或 download_<源名>。", name)
}

// doSearch 执行一次搜索，把轻量候选列表序列化后回灌给模型。
//
// 注意所有失败路径都是**回灌文本**而不是中断：模型据此可以换关键词或换源，
// 沿用 internal/agent 里同样的"失败回灌不中断"设计。
func (r *run) doSearch(ctx context.Context, srcName, rawArgs string) string {
	src, ok := r.agent.sources[srcName]
	if !ok {
		return fmt.Sprintf("未知音源 %q。", srcName)
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return fmt.Sprintf("工具参数解析失败: %v；参数应为 {\"query\": \"...\"}。", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "工具参数 query 为空，请给出搜索关键词。"
	}

	// 进度：搜索次数在真正发起搜索前就 +1，这样"搜索中"能立刻显示出来。
	r.st.Searches++
	r.rep.Update(ctx, *r.st)

	cands, err := src.Search(ctx, args.Query)
	if err != nil {
		slog.ErrorContext(ctx, "音乐搜索失败", "source", srcName, "query", args.Query, "err", err)
		return fmt.Sprintf("搜索失败: %v。可以换个关键词再试一次。", err)
	}
	if len(cands) == 0 {
		// 零结果是正常路径，把音源特性再提醒一遍，引导模型换词。
		return "零结果。" + src.Hint() + " 请换关键词重试。"
	}
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}

	// lite 是回给模型的精简形态：**不含直链**。
	type lite struct {
		ID       string `json:"id"`
		Artist   string `json:"artist"`
		Title    string `json:"title"`
		Duration string `json:"duration"`
	}
	out := make([]lite, 0, len(cands))
	for _, c := range cands {
		// 候选存进本次任务的局部表，下载时凭 id 查回真直链。
		r.candidates[srcName+":"+c.ID] = c
		out = append(out, lite{ID: c.ID, Artist: c.Artist, Title: c.Title, Duration: c.Duration})
	}

	r.st.Found = len(out)
	r.rep.Update(ctx, *r.st)

	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("序列化候选列表失败: %v", err)
	}
	slog.InfoContext(ctx, "音乐搜索完成", "source", srcName, "query", args.Query, "count", len(out))
	return string(raw)
}

// doDownload 下载指定候选到临时目录。
func (r *run) doDownload(ctx context.Context, srcName, rawArgs string) string {
	src, ok := r.agent.sources[srcName]
	if !ok {
		return fmt.Sprintf("未知音源 %q。", srcName)
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return fmt.Sprintf("工具参数解析失败: %v；参数应为 {\"id\": \"...\"}。", err)
	}
	c, ok := r.candidates[srcName+":"+args.ID]
	if !ok {
		// 模型编了个不存在的 id：回灌纠正，别打到音源上去。
		return fmt.Sprintf("id %q 不在 %s 的搜索结果里。请先搜索，并原样使用候选列表里的 id。", args.ID, srcName)
	}

	// 进度：先把"选定了哪首"显示出来（此刻 Bytes 还是 0，渲染成进行中）。
	r.st.Stage = StageDownloading
	r.st.Track = &Track{Artist: c.Artist, Title: c.Title, Duration: c.Duration, Source: srcName}
	r.rep.Update(ctx, *r.st)

	path := filepath.Join(r.dir, downloadFileName)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Sprintf("创建本地文件失败: %v", err)
	}

	// limitWriter 在这里统一强制大小上限，不下放给各音源实现。
	lw := &limitWriter{w: f, limit: r.agent.maxBytes}
	n, dErr := src.Download(ctx, c, lw)
	closeErr := f.Close()

	if dErr != nil || closeErr != nil {
		// 删掉半成品：残缺的 mp3 传上网盘比没有更糟。
		if rmErr := os.Remove(path); rmErr != nil {
			slog.WarnContext(ctx, "删除下载半成品失败", "path", path, "err", rmErr)
		}
		r.st.Track = nil
		r.rep.Update(ctx, *r.st)

		// errors.Is 沿着 %w 包装链找哨兵错误，所以音源里包了几层都能认出来。
		if errors.Is(dErr, errTooLarge) {
			return fmt.Sprintf("文件过大（超过 %d MB），已中断下载。请改选其它候选。", r.agent.maxBytes>>20)
		}
		if dErr == nil {
			dErr = closeErr
		}
		slog.ErrorContext(ctx, "音乐下载失败", "source", srcName, "id", c.ID, "err", dErr)
		return fmt.Sprintf("下载失败: %v。可以改选其它候选。", dErr)
	}

	r.track = &Track{
		LocalPath: path,
		Artist:    c.Artist, // 先填站点原始名，finalize 里会被模型规范化后的名字覆盖
		Title:     c.Title,
		Duration:  c.Duration,
		Bytes:     n,
		Source:    srcName,
	}
	r.downloadedID = c.ID
	r.st.Track = r.track
	r.rep.Update(ctx, *r.st)

	slog.InfoContext(ctx, "音乐下载完成", "source", srcName, "id", c.ID, "bytes", n)
	return fmt.Sprintf("下载完成，%d 字节。现在请按要求输出最终 JSON。", n)
}

// limitWriter 是计数 writer：写入总量超过 limit 立即返回 errTooLarge，
// 让上游的 io.Copy 中断。
//
// 为什么上限在这里而不是各个 Source 里？
// 否则每加一个音源都要重复实现一遍同样的防御，而且很容易漏。
type limitWriter struct {
	w     io.Writer
	limit int64
	n     int64 // 已写入字节数
}

// Write 满足 io.Writer。超限时返回 (0, errTooLarge)，io.Copy 会立刻停下来。
func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > l.limit {
		return 0, errTooLarge
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: PASS — 十个 `TestAgent*` / `TestBuildToolDefs` 全绿，Task 3/4/5 的用例不回归。

- [ ] **Step 5: 提交**

```bash
git add internal/music/agent.go internal/music/agent_test.go
git commit -m "#feat music 新增 LLM 工具调用循环与下载大小上限

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 7: Runner 编排层

**Files:**
- Create: `internal/music/runner.go`
- Test: `internal/music/runner_test.go`

**Interfaces:**
- Consumes: `Agent.Fetch`（Task 6）、`Uploader.Upload` / `BuildFilename`（Task 5）、`Reporter`/`Status`（Task 4）、`notify.Editor`（Task 2）
- Produces:
  ```go
  func NewRunner(a *Agent, u *Uploader, e notify.Editor) *Runner
  func (r *Runner) Run(ctx context.Context, chatID int64, query string) error
  ```
  Task 8 的 `command.Bot` 通过 `command.MusicRunner` 接口消费；Task 9 的 `main.go` 调 `NewRunner`。

- [ ] **Step 1: 写失败的测试**

创建 `internal/music/runner_test.go`：

```go
package music

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeFetcher 替代真实 agent，让 Runner 的编排逻辑能脱离 LLM 单测。
type fakeFetcher struct {
	track   *Track
	err     error
	gotDir  string // 记录 Runner 传进来的临时目录，用于断言清理行为
	gotStat *Status
}

func (f *fakeFetcher) Fetch(_ context.Context, st *Status, dir string, _ Reporter) (*Track, error) {
	f.gotDir = dir
	f.gotStat = st
	if f.err != nil {
		return nil, f.err
	}
	// 真 agent 会把文件落在 dir 里，这里也造一个，好验证 Runner 会清理它。
	if f.track != nil && f.track.LocalPath == "" {
		p := dir + string(os.PathSeparator) + downloadFileName
		if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
			return nil, err
		}
		f.track.LocalPath = p
	}
	st.Track = f.track
	return f.track, nil
}

// fakeUploader 替代真实 Uploader。
type fakeUploader struct {
	states   []TargetStatus
	err      error
	gotFile  string // 记录 Runner 传进来的文件名，用于断言 BuildFilename 被用上
	gotLocal string
}

func (f *fakeUploader) Upload(_ context.Context, localPath, filename string, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	f.gotLocal = localPath
	f.gotFile = filename
	if onProgress != nil {
		onProgress(f.states)
	}
	return f.states, f.err
}

// newTestRunner 组装注入了 fake 的 Runner。
func newTestRunner(f *fakeFetcher, u *fakeUploader, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, editor: e}
}

// TestRunnerHappyPath：下载成功 + 上传成功 → 无错误，文件名用规范化后的歌手歌名，
// 临时目录被清理，终态被发出。
func TestRunnerHappyPath(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "周杰伦", Title: "晴天", Duration: "03:58", Bytes: 100, Source: "mp3pm"}}
	u := &fakeUploader{states: []TargetStatus{
		{Name: "阿里云盘", State: TargetOK},
		{Name: "OneDrive", State: TargetOK},
	}}
	fe := &fakeEditor{}

	if err := newTestRunner(f, u, fe).Run(context.Background(), 1, "晴天 周杰伦"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	if u.gotFile != "周杰伦 - 晴天.mp3" {
		t.Errorf("上传文件名 = %q, want %q", u.gotFile, "周杰伦 - 晴天.mp3")
	}
	// 临时目录必须被删掉，不能在系统临时目录里堆 mp3。
	if _, err := os.Stat(f.gotDir); !os.IsNotExist(err) {
		t.Errorf("临时目录 %q 未被清理", f.gotDir)
	}
	// 进度快照的 Query 必须是用户原始输入。
	if f.gotStat.Query != "晴天 周杰伦" {
		t.Errorf("Status.Query = %q, want %q", f.gotStat.Query, "晴天 周杰伦")
	}
	// 至少发过一次消息（首发）+ 终态编辑。
	sends, edits := fe.counts()
	if sends != 1 || edits < 1 {
		t.Errorf("SendEditable=%d Edit=%d，期望 1 次首发 + 至少 1 次编辑", sends, edits)
	}
}

// TestRunnerFetchFailure：agent 失败 → 返回错误，终态写明原因，临时目录仍被清理。
func TestRunnerFetchFailure(t *testing.T) {
	f := &fakeFetcher{err: errors.New("模型未收敛")}
	u := &fakeUploader{}
	fe := &fakeEditor{}

	err := newTestRunner(f, u, fe).Run(context.Background(), 1, "不存在的歌")
	if err == nil {
		t.Fatal("agent 失败时 Run 应返回错误")
	}
	if u.gotFile != "" {
		t.Error("agent 失败后不该再尝试上传")
	}
	if _, sErr := os.Stat(f.gotDir); !os.IsNotExist(sErr) {
		t.Errorf("失败路径下临时目录 %q 也必须被清理", f.gotDir)
	}
	// 终态消息里要能看到失败原因。
	fe.mu.Lock()
	defer fe.mu.Unlock()
	all := strings.Join(append(append([]string{}, fe.sends...), fe.edits...), "\n")
	if !strings.Contains(all, "模型未收敛") {
		t.Errorf("终态消息里应包含失败原因，实际:\n%s", all)
	}
}

// TestRunnerUploadAllFailed：上传全失败 → 返回错误。
func TestRunnerUploadAllFailed(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T"}}
	u := &fakeUploader{
		states: []TargetStatus{{Name: "A", State: TargetFailed, Err: "401"}},
		err:    errors.New("全部 1 个 WebDAV 目标上传失败"),
	}

	if err := newTestRunner(f, u, &fakeEditor{}).Run(context.Background(), 1, "x"); err == nil {
		t.Fatal("上传全失败时 Run 应返回错误")
	}
}

// TestRunnerUploadPartialFailedStillSucceeds：一成一败 → Run 不报错。
// 这是「不为一个网盘挂掉丢弃已下好的歌」在编排层的兑现。
func TestRunnerUploadPartialFailedStillSucceeds(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T"}}
	u := &fakeUploader{states: []TargetStatus{
		{Name: "A", State: TargetOK},
		{Name: "B", State: TargetFailed, Err: "507"},
	}} // err 为 nil：Uploader 的语义是"至少一个成功就不报错"

	if err := newTestRunner(f, u, &fakeEditor{}).Run(context.Background(), 1, "x"); err != nil {
		t.Fatalf("一成一败时 Run 不该报错: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/music -run TestRunner -v`
Expected: FAIL — 编译不过，`undefined: Runner`。

- [ ] **Step 3: 写 `internal/music/runner.go`**

```go
package music

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/cheedonghu/news2tg/internal/notify"
)

// agentTimeout 是 agent 循环（搜索 + 下载）的超时，与 /summary 的 180s 对齐。
// 模型最多 6 轮调用 + 一次下载，正常在 30s 内跑完，180s 是宽松兜底。
// 上传的超时在 Uploader 内部按目标各自计（300s），总预算 600s 由 command.Bot 套。
const agentTimeout = 180 * time.Second

// fetcher / uploader 是**消费侧接口**（"accept interfaces"）：
// Runner 只依赖这两件能力，不直接绑死 *Agent / *Uploader 的具体类型。
// 好处是编排逻辑能脱离 LLM 和网络单测 —— 塞两个 fake 就够了。
type fetcher interface {
	Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error)
}

type uploader interface {
	Upload(ctx context.Context, localPath, filename string, onProgress func([]TargetStatus)) ([]TargetStatus, error)
}

// Runner 把 agent、上传、进度上报串成一次完整的 /music 任务。
type Runner struct {
	fetcher  fetcher
	uploader uploader
	editor   notify.Editor // 用来给每次任务新建一个 Reporter
}

// NewRunner 构造函数。收具体类型、存接口，是仓库里一贯的注入风格。
func NewRunner(a *Agent, u *Uploader, e notify.Editor) *Runner {
	return &Runner{fetcher: a, uploader: u, editor: e}
}

// Run 执行一次完整任务：建临时目录 → agent 搜+下 → 并发上传 → 收尾。
//
// 进度与最终结果全部通过原地编辑**同一条**消息回报，所以这里除了返回 error
// 给调用方记日志之外，不再往 Telegram 发任何额外消息。
func (r *Runner) Run(ctx context.Context, chatID int64, query string) error {
	// 每次任务一个 Reporter：它内部持有那条消息的 msgID 和节流状态，
	// 多个并发 /music 各用各的，互不干扰。
	rep := NewTelegramReporter(r.editor, chatID)

	// 这份 Status 是全程唯一的进度真相：agent 改搜索/下载部分，
	// 下面的上传回调改 Targets，Reporter 每次拿到的都是完整快照。
	st := &Status{Query: query, Stage: StageSearching}
	rep.Update(ctx, *st)

	// os.MkdirTemp 在系统临时目录下建一个带随机后缀的目录，"*" 是随机部分的占位。
	dir, err := os.MkdirTemp("", "news2tg-music-*")
	if err != nil {
		st.Stage, st.Err = StageFailed, "创建临时目录失败: "+err.Error()
		rep.Done(ctx, *st)
		return err
	}
	// 成败都删：容器里攒 mp3 只会撑爆磁盘，歌已经在网盘里了。
	// defer 在函数返回时执行，所以下面每条 return 路径都会走到它。
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			slog.ErrorContext(ctx, "清理音乐临时目录失败", "dir", dir, "err", rmErr)
		}
	}()

	// agent 单独套一层超时：它慢在模型多轮调用上，和上传的慢是两回事。
	actx, cancel := context.WithTimeout(ctx, agentTimeout)
	track, err := r.fetcher.Fetch(actx, st, dir, rep)
	cancel() // 不用 defer：后面的上传不该再受 agent 那层超时约束
	if err != nil {
		st.Stage, st.Err = StageFailed, err.Error()
		rep.Done(ctx, *st)
		return err
	}

	// 文件名用模型规范化后的歌手/歌名，清洗掉危险字符。
	filename := BuildFilename(track.Artist, track.Title)
	st.Track, st.Stage = track, StageUploading
	rep.Update(ctx, *st)

	slog.InfoContext(ctx, "开始上传音乐到 WebDAV", "file", filename, "bytes", track.Bytes)

	targets, upErr := r.uploader.Upload(ctx, track.LocalPath, filename, func(ts []TargetStatus) {
		// 每个目标状态一变就刷进度。Reporter 内部会节流，这里放心调。
		st.Targets = ts
		rep.Update(ctx, *st)
	})
	st.Targets = targets

	if upErr != nil {
		st.Stage, st.Err = StageFailed, upErr.Error()
		rep.Done(ctx, *st)
		return upErr
	}

	st.Stage = StageDone
	rep.Done(ctx, *st)
	slog.InfoContext(ctx, "音乐任务完成", "file", filename)
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: PASS — 四个 `TestRunner*` 全绿，Task 3-6 的用例不回归。

- [ ] **Step 5: 全包竞态与静态检查**

Run: `go test -race ./internal/music && go vet ./internal/music && gofmt -l internal/music`
Expected: 测试 PASS 无 DATA RACE；`go vet` 无输出；`gofmt -l` 无输出（只看本次新增的文件）。

- [ ] **Step 6: 提交**

```bash
git add internal/music/runner.go internal/music/runner_test.go
git commit -m "#feat music 新增 Runner 编排层串联 agent、上传与进度

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 8: `command.Bot` 容纳第二条指令

**Files:**
- Modify: `internal/command/bot.go`（`classify` 与 `handle` 重构、新增 `MusicRunner` 依赖与 `/music` 分支、`NewBot` 加参数、`setMyCommands` 条件注册）
- Modify: `internal/command/bot_test.go`（用例改造 + `/music` 新用例）

**Interfaces:**
- Consumes: `music.Runner.Run`（Task 7，通过接口）
- Produces:
  ```go
  type MusicRunner interface {
      Run(ctx context.Context, chatID int64, query string) error
  }

  // 签名变化：末尾新增 music 参数。music 为 nil 时 /music 不注册、收到也只回"未配置"。
  func NewBot(token string, agent Summarizer, notifier notify.Notifier,
      adminIDs []int64, music MusicRunner) (*Bot, error)
  ```
  Task 9 的 `main.go` 按新签名调用。

> **为什么必须重构 `classify`：** 现签名是 `classify(msg) (action, url)`，`actionSummarize` 这个枚举名把 summary 语义焊死在决策树里，第二条指令没法挂进去。改成返回 `intent{act, cmd, args}` 之后，判定逻辑仍是纯函数（不发消息、不调 agent），仍可脱离真实 bot 单测。**这是本次唯一必要的存量重构，不要顺手改别的。**

- [ ] **Step 1: 重写测试**

把 `internal/command/bot_test.go` 的 `TestClassify` 整体替换为下面内容（`cmdMsg` 辅助函数保持不变）：

```go
// fakeMusic 是 MusicRunner 的空实现，只为让 classify 能看到"音乐功能已配置"。
type fakeMusic struct{}

func (fakeMusic) Run(_ context.Context, _ int64, _ string) error { return nil }

func TestClassify(t *testing.T) {
	// 白名单只含 111；音乐功能已配置。
	b := &Bot{admins: map[int64]bool{111: true}, music: fakeMusic{}}

	const (
		sumLen   = len("/summary") // 8
		musicLen = len("/music")   // 6
	)

	cases := []struct {
		name     string
		msg      *tgbotapi.Message
		wantAct  action
		wantCmd  string
		wantArgs string
	}{
		{
			name:    "nil 消息 → 忽略",
			msg:     nil,
			wantAct: actionIgnore,
		},
		{
			name:    "非命令文本 → 忽略",
			msg:     &tgbotapi.Message{Text: "https://example.com", From: &tgbotapi.User{ID: 111}, Chat: &tgbotapi.Chat{ID: 1}},
			wantAct: actionIgnore,
		},
		{
			name:    "别的命令 → 忽略",
			msg:     cmdMsg("/start", len("/start"), 111),
			wantAct: actionIgnore,
		},
		{
			name:    "非白名单发 /summary → 未授权",
			msg:     cmdMsg("/summary https://example.com", sumLen, 222),
			wantAct: actionUnauthorized,
			wantCmd: commandSummary,
		},
		{
			name:    "白名单但无参数 → 提示用法",
			msg:     cmdMsg("/summary", sumLen, 111),
			wantAct: actionUsage,
			wantCmd: commandSummary,
		},
		{
			name:    "白名单但参数非 http → 提示用法",
			msg:     cmdMsg("/summary 随便写点啥", sumLen, 111),
			wantAct: actionUsage,
			wantCmd: commandSummary,
		},
		{
			name:     "白名单 + 合法网址 → 去执行",
			msg:      cmdMsg("/summary https://example.com/x", sumLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandSummary,
			wantArgs: "https://example.com/x",
		},
		{
			name:    "非白名单发 /music → 未授权",
			msg:     cmdMsg("/music 晴天", musicLen, 222),
			wantAct: actionUnauthorized,
			wantCmd: commandMusic,
		},
		{
			name:    "白名单 /music 但无参数 → 提示用法",
			msg:     cmdMsg("/music", musicLen, 111),
			wantAct: actionUsage,
			wantCmd: commandMusic,
		},
		{
			name:     "白名单 /music + 自由格式歌名 → 去执行",
			msg:      cmdMsg("/music 晴天 - 周杰伦", musicLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandMusic,
			wantArgs: "晴天 - 周杰伦",
		},
		{
			name:     "/music 参数不需要是网址，随便什么格式都收",
			msg:      cmdMsg("/music 周杰伦那首晴天", musicLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandMusic,
			wantArgs: "周杰伦那首晴天",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := b.classify(c.msg)
			if got.act != c.wantAct {
				t.Fatalf("act = %d, want %d", got.act, c.wantAct)
			}
			if got.cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", got.cmd, c.wantCmd)
			}
			if got.args != c.wantArgs {
				t.Errorf("args = %q, want %q", got.args, c.wantArgs)
			}
		})
	}
}

// TestClassifyMusicUnavailable 验证：[music] 未配置（music 依赖为 nil）时，
// 白名单用户发 /music 得到"未配置"而不是被静默忽略 —— 否则用户会以为 bot 坏了。
func TestClassifyMusicUnavailable(t *testing.T) {
	b := &Bot{admins: map[int64]bool{111: true}} // music 字段留 nil

	got := b.classify(cmdMsg("/music 晴天", len("/music"), 111))
	if got.act != actionUnavailable {
		t.Fatalf("act = %d, want actionUnavailable(%d)", got.act, actionUnavailable)
	}
	if got.cmd != commandMusic {
		t.Errorf("cmd = %q, want %q", got.cmd, commandMusic)
	}
}

// TestUsageText 验证两条指令的用法提示各说各的，不会串。
func TestUsageText(t *testing.T) {
	if !strings.Contains(usageText(commandSummary), "/summary") {
		t.Errorf("summary 用法提示不对: %q", usageText(commandSummary))
	}
	if !strings.Contains(usageText(commandMusic), "/music") {
		t.Errorf("music 用法提示不对: %q", usageText(commandMusic))
	}
}
```

测试文件的 import 需要补 `"context"` 和 `"strings"`：

```go
import (
	"context"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/command -v`
Expected: FAIL — 编译不过，`undefined: actionRun` / `b.classify(...) used as value` 之类。

- [ ] **Step 3: 改 `internal/command/bot.go`**

**3a. 包注释与常量。** 把文件顶部的包注释改成：

```go
// Package command 实现"通过 Telegram 指令驱动"的能力：
// 管理员给 bot 发指令，服务执行对应任务。目前认两条：
//
//	/summary <网址>      调总结 agent 产出中文总结，推送到配置的频道
//	/music   <歌曲描述>  调音乐 agent 下载歌曲并上传到 WebDAV，进度私聊回报
//
// 与 notify 不同，这个包要**收** Telegram update（long-poll getUpdates），
// 所以它自己持有一个 *tgbotapi.BotAPI；推送结果时复用 notify.Notifier。
package command
```

常量部分替换为：

```go
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
```

**3b. 依赖接口与结构体。** 在 `Summarizer` 接口之后新增，并改 `Bot`：

```go
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
```

**3c. `Run` 里的命令注册。** 把现有 `cmdCfg := tgbotapi.NewSetMyCommands(...)` 那一段替换为：

```go
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
```

**3d. 判定层。** 把现有的 `action` 枚举、`classify`、`handle` 三段整体替换为：

```go
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
```

**3e. `summarizeAndPush` 不动**（它只是从 `handle` 里换了个调用点）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/command -v`
Expected: PASS — `TestClassify` 的 11 个子测试、`TestClassifyMusicUnavailable`、`TestUsageText` 全绿。

> 此刻 `go build ./...` 仍会失败：`cmd/news2tg/main.go` 还在用 4 参数的 `NewBot`。这是预期的，Task 9 修。

- [ ] **Step 5: 提交**

```bash
git add internal/command/bot.go internal/command/bot_test.go
git commit -m "#feat command bot 支持 /music 指令并重构 classify 判定层

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Task 9: main 组装、配置模板与文档

**Files:**
- Modify: `cmd/news2tg/main.go:156-161`（`NewBot` 调用处）及其上方新增组装段
- Modify: `config.toml`（末尾新增 `[music]` 占位段）
- Modify: `myconfig.toml`（本地真值；**不进 git**）
- Modify: `CLAUDE.md`、`README.md`

**Interfaces:**
- Consumes: `config.Music`（Task 1）、`music.NewMp3PM`/`NewAgent`/`NewUploader`/`NewRunner`/`Target`（Task 3/5/6/7）、`command.NewBot` 新签名与 `command.MusicRunner`（Task 8）
- Produces: 可运行的完整程序

- [ ] **Step 1: 改 `cmd/news2tg/main.go`**

在 import 块里加一行（放在 `logx` 和 `monitor` 之间，保持字母序）：

```go
	"github.com/cheedonghu/news2tg/internal/music"
```

在 `9.3) 命令 bot` 那段**之前**插入新的组装段：

```go
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
		// 音源列表：目前只有 mp3.pm，加新源就在这里多塞一个实现。
		sources := []music.Source{music.NewMp3PM(httpClient)}
		// 复用 DeepSeek 的 key 和 agent_model（同样需要 function calling 能力）。
		musicAgent := music.NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, sources)
		// 两个固定目标；Uploader 内部按切片循环，加第三个网盘只需在这里多一项。
		targets := []music.Target{
			{Name: cfg.Music.WebdavName1, URL: cfg.Music.WebdavURL1},
			{Name: cfg.Music.WebdavName2, URL: cfg.Music.WebdavURL2},
		}
		uploader := music.NewUploader(httpClient, targets, cfg.Music.WebdavUser, cfg.Music.WebdavPass)
		// tgClient 同时是 notify.Notifier 和 notify.Editor，这里用的是后者。
		musicRunner = music.NewRunner(musicAgent, uploader, tgClient)
		slog.Info("音乐功能已启用", "targets", len(targets))
	} else {
		slog.Warn("[music] 未配置，/music 指令不可用")
	}
```

把 `9.3)` 那段的 `NewBot` 调用改成带第五个参数：

```go
	// 9.3) 命令 bot：收 /summary <网址> 和 /music <歌曲描述>。
	cmdBot, err := command.NewBot(cfg.Telegram.APIToken, summaryAgent, tgClient, adminIDs, musicRunner)
	if err != nil {
		slog.Error("failed to init command bot", "err", err)
		os.Exit(1)
	}
```

- [ ] **Step 2: 跑全量构建与测试**

Run: `go build ./... && go vet ./... && go test -short ./...`
Expected: 全部通过。这是整条链路第一次编译成功。

- [ ] **Step 3: 改 `config.toml`（进 git 的模板）**

在文件末尾追加：

```toml
# /music 指令的 WebDAV 上传目标与凭据（alist 挂载的网盘，通过 WebDAV 统一接管上传）。
#
# 这一段是**可选**的：六项全空 → /music 不注册到命令清单、收到也只回一句未配置，
# 不影响程序启动。但**填一半会直接启动失败**——半配置一定是打字漏了。
#
# ⚠️ 真实凭据只写在 myconfig.toml（已 gitignore），不要填进这个模板文件：
# 本文件是被 git 跟踪的，仓库又是公开的。
[music]
webdav_user   = ""
webdav_pass   = ""
webdav_name_1 = ""
webdav_url_1  = ""
webdav_name_2 = ""
webdav_url_2  = ""
```

- [ ] **Step 4: 改 `myconfig.toml`（本地真值，不进 git）**

追加同样的 `[music]` 段但填上真实值。占位示例：

```toml
[music]
webdav_user   = "alist"
webdav_pass   = "换成你的 alist 密码"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://127.0.0.1:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://127.0.0.1:5244/dav/onedrive/Music"
```

> 执行者注意：`myconfig.toml` 在 `.gitignore` 里，**不要 `git add` 它**。若该文件当前不存在，跳过本步并在最终汇报里说明。

- [ ] **Step 5: 用真配置起一次进程验证不崩**

Run: `go run ./cmd/news2tg -c myconfig.toml`
Expected: 启动日志里出现 `音乐功能已启用 targets=2` 与 `command bot 已启动 commands=2`，无 panic。观察几秒后 `Ctrl-C` 退出。

若 `myconfig.toml` 不存在或没有可用的 Telegram token，跳过本步，改为验证"配置缺失时不崩"：

Run: `go run ./cmd/news2tg -c config.toml`
Expected: 因 `[telegram] api_token` 是占位值而在初始化 Telegram 时失败退出（这是既有行为），**但不能出现任何与 music 相关的 panic**。

- [ ] **Step 6: 改 `CLAUDE.md`**

三处需要同步：

**6a.** Architecture 的接口清单里，在 `command.Bot` 条目**之后**新增：

```markdown
- **`music`** (`internal/music/`) — `/music <歌曲描述>` 背后的完整链路。`music.Source`
  (`source.go`) 是音源扩展点（`Search`/`Download`），实现：`Mp3PM`（`mp3pm.go`，
  HTML 解析，站点特有逻辑只在这一个文件里）。`music.Agent`（`agent.go`）用 DeepSeek
  **function calling** 为每个注册的 `Source` **自动生成** `search_<名>`/`download_<名>`
  两个工具，模型负责理解自由格式歌名、换关键词重搜（mp3.pm 的中文歌按拼音收录，
  搜中文常零结果）、避开 live/伴奏/翻唱，并在最后输出规范化的
  `{"artist","title","id"}` JSON 用于拼文件名。**上传不是工具**——目标固定、无需模型判断，
  由 `music.Uploader`（`upload.go`）在 agent 返回后并发 PUT 到两个 WebDAV 目标，
  至少一个成功即算成功。`music.Runner`（`runner.go`）串联三者并负责临时目录的建与删
  （`defer os.RemoveAll`，成败都删）。复用 `cfg.DeepSeek.AgentModel`，不新增模型配置。
```

**6b.** `notify.Notifier` 那一段末尾补充：

```markdown
  另有 **`notify.Editor`**（`SendEditable`/`Edit`）供需要**原地编辑同一条消息**的调用方使用
  （目前只有 `/music` 的进度上报）。它与 `Notifier` 分开定义，避免 monitor 那些只推不改的
  调用方被迫认识 `msgID`。`*Telegram` 同时实现两者，编辑与发送**共用同一把全局节流锁**
  （Telegram 限额按 bot 计）。代价是 `/music` 运行期间 HN/V2EX 推送会被轻微挤占；
  `music` 侧的 Reporter 做 3s 合并节流来压低这个影响。
```

**6c.** Config 一节末尾补充：

```markdown
`[music]`（`/music` 的 WebDAV 凭据与两个上传目标，六个平铺字段）是**可选段**，
与其它段"缺配置即启动失败"的哲学不同：**六项全空 → `/music` 不可用但程序正常启动**
（现有部署没有这一段，不能因升级就起不来）；**填一半 → 启动失败**（半配置一定是打字漏了）。
凭据只写 `myconfig.toml`，`config.toml` 留空占位——后者进 git 且仓库公开。
```

**6d.** 单元测试清单那一段（"Unit tests focus on…"）补上 music 包：

```markdown
`internal/music/*_test.go`（mp3.pm 的 HTML fixture 解析与 httptest 两跳搜索、
agent 循环的全部分支（含换词重搜/下载失败改选/超 50MB/非 JSON 输出/id 不一致/未收敛）、
WebDAV 假服务端的部分失败语义、文件名清洗、进度上报的节流合并与终态立即发出）
```

- [ ] **Step 7: 改 `README.md`**

在"技术栈"一节的依赖行之后补一句功能说明（位置按现有行文自行安排）：

```markdown
- Telegram 指令：`/summary <网址>` 总结网页并推送到频道；`/music <歌曲描述>` 从 mp3.pm
  搜索下载歌曲并经 WebDAV 上传到 alist 挂载的网盘，进度以单条消息原地编辑的方式实时回报。
  两条指令都仅限 `[telegram] admin_ids` 白名单用户。
```

- [ ] **Step 8: 最终全量验收**

```
go build ./...
go vet ./...
go test -short ./...
go test -race -short ./internal/music
gofmt -l internal/music internal/command/bot.go internal/config/config.go internal/notify/notify.go internal/notify/telegram.go cmd/news2tg/main.go
```

Expected：
- 前四条全部通过，无 DATA RACE。
- `gofmt -l` **只对上面列出的本次改动文件**，应无输出。**不要跑 `gofmt -l .`** —— 本仓库因 CRLF 天然列出二十余个无关文件。

- [ ] **Step 9: 提交**

```bash
git add cmd/news2tg/main.go config.toml CLAUDE.md README.md
git commit -m "#feat main 接入音乐 agent，补齐配置模板与文档

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## 落地后的人工验证清单

代码全绿不等于功能可用。合并前请人工过一遍：

1. **起真进程**，在 Telegram 里发 `/music 晴天 周杰伦`。观察那条消息是否原地更新，且中途不刷出新消息。
2. **确认换词重搜生效**：日志里应能看到至少两次 `开始在 mp3.pm 搜索`，第一次多半是中文关键词、零结果。
3. **确认两个网盘里都出现了 `周杰伦 - 晴天.mp3`**，文件能正常播放（不是残缺文件）。
4. **确认临时目录被清理**：容器/本机的系统临时目录下不应残留 `news2tg-music-*`。
5. **故意写错一个 WebDAV 密码**再跑一次，确认终态消息如实显示"一成一败"且任务整体算成功。
6. **发 `/music`（不带参数）** 确认回的是用法提示；**用非白名单账号发** 确认 bot 完全不理。

## 已知风险（来自 spec，实施时留意）

- `data-download-url` 里的 token 是否有时效未验证。若搜索与下载间隔久后出现 403，
  表现为下载失败被回灌、模型重搜——功能上能自愈，但会多烧 token。
- mp3.pm 改版会让解析静默失效（表现为"永远搜不到"）。`parseMp3PMResults` 里那条
  "解析出 0 条候选（可能站点改版）" 的 WARN 就是为此埋的，排障时先看它。
- WebDAV PUT 是覆盖语义：重复下同一首歌会覆盖旧文件。幂等是好事，
  但也意味着下到差版本会盖掉之前的好版本。本次不做备份，属已知取舍。
