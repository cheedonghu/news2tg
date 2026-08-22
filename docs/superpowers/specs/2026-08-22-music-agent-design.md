# 音乐抓取 agent（/music）· 设计

日期：2026-08-22
状态：待评审

## 目标

给 Telegram bot 增加 `/music <歌曲描述>` 指令：由 LLM 理解自由格式的歌曲描述，
从候选 mp3 站点搜索并挑选正确的曲目，下载到本地，再通过 WebDAV 上传到两个
alist 挂载的网盘目录，全程以**原地编辑的单条消息**向发起者回报实时进度。

## 范围与非目标

- **做**：`internal/music` 新包（`Source` 接口 + mp3.pm 实现 + LLM 工具调用循环 +
  WebDAV 并发上传 + 进度上报）、`notify.Editor` 接口与 `*notify.Telegram` 的实现、
  `command.Bot` 的 `classify` 重构以容纳第二条指令、新增 `[music]` 配置段。
- **不做**：mp3.pm 之外的其它音源（本次只交付一个源 + 扩展点，第二个源单独设计）、
  推送去重（手动触发的指令不需要）、ID3 标签写入/修正、专辑封面抓取、
  把 mp3 文件本身发回 Telegram、歌单/批量下载、非管理员可用。

## 关键决策与理由

| 决策 | 选择 | 理由 |
|---|---|---|
| 每个源的工具粒度 | **搜索 / 下载两个 tool** | 模型看到候选列表才能避开 live/伴奏/翻唱；若合成一个 tool，LLM 退化为字符串分割器 |
| 源的抽象 | `Source` 接口，agent 自动生成两个 tool schema | 实现独立、形状统一；加源不改 agent 循环，也不用手写 schema 样板 |
| 上传是否做成 tool | **否，纯 Go 确定性步骤** | 目标固定两个且每次都传，无需模型判断；做成 tool 只会多烧 token、多一处失败面 |
| 进度回报方式 | **原地编辑单条消息**（`editMessageText`） | 聊天记录干净；离散多条会刷屏且每条多占 1.5s 全局发送锁 |
| 进度接口形态 | **覆盖式完整快照**，非增量事件 | 每个快照自洽 ⇒ 丢弃中间态是安全的，节流才成立；单测断言也简单 |
| 编辑请求的限速 | **复用 `Telegram` 的全局锁**，Reporter 侧 3s 合并节流 | Telegram 限额按 bot 计，绕过全局节流阀迟早撞 429 |
| 文件名来源 | **模型规范化输出** `歌手 - 歌名` | mp3.pm 的中文歌是罗马化的（`Kai Bu Liao Kou`），只有模型能还原成 `开不了口` |
| 模型最终输出格式 | **强制 JSON**，解析失败即任务失败 | 文件名依赖它，自由文本解析不可靠；模型不守格式说明它也没真选好歌 |
| 搜索零结果 | **回灌给模型，允许换词重搜** | mp3.pm 是俄站，中文关键词大概率零结果，换拼音/英文是刚需路径而非异常 |
| 上传目标配置 | 六个平铺字段，非数组 | 按需求"固定两个、不做动态列表"；但 `Uploader` 内部仍按 `[]Target` 循环，扩展只需加字段 |
| WebDAV 凭据存放 | `[music]` 段，落在 gitignore 的 `myconfig.toml` | `config.toml` 进 git 且仓库公开，硬编码密码等于直接泄漏 |
| 上传部分失败 | **至少一个成功即算任务成功** | 延续仓库"AI/digest 失败不阻断推送"的约定；不为一个网盘挂掉丢弃已下好的歌 |
| 去重 | **不做** | 手动重复触发基本是故意重下（上次下到伴奏版）；WebDAV 同名覆盖已天然幂等 |
| 本地文件 | 传完即删（`defer os.RemoveAll`） | 容器内攒 mp3 只会撑爆磁盘，网盘里已有 |
| 模型配置 | 复用 `[deepseek] agent_model` | 语义完全吻合（"必须支持 function calling 的那个模型"），不新增配置项 |

## mp3.pm 接口调研结论

已实地验证（2026-08-22），实现时按此结构写，不必重新摸索：

```
① POST https://mp3.pm/public/api.search.php
   Content-Type: application/x-www-form-urlencoded
   body: q=<关键词>
   → 响应体是一行纯文本 URL，例如：https://s-jay-chou.mp3.pm/

② GET <上一步的 URL>
   → 结果页 HTML。每首歌一个 <li class="cplayer-sound-item">，属性与子元素：
       data-sound-id      = "4287389"
       data-download-url  = "https://cs1.mp3.pm/download/4287389/<token>/Jay_Chou_-_Kai_Bu_Liao_Kou._(mp3.pm).mp3"
       <i class="cplayer-data-sound-author">  → 歌手 "Jay Chou"
       <b class="cplayer-data-sound-title">   → 歌名 "Kai Bu Liao Kou."
       <em class="cplayer-data-sound-time">   → 时长 "04:44"

③ GET <data-download-url>
   → 200 audio/mpeg，**无需 referer / cookie**，支持 Range（206）
```

三点实现约束由此而来：

1. **搜索结果页一次性给全**（歌手/歌名/时长/直链都在 `<li>` 上），`Download` 不需要二次抓详情页，
   就是一个 HTTP GET。
2. **站点不提供码率**，只有时长。最终消息展示**时长 + 文件大小**（大小取自实际写入字节数，
   不信任 `Content-Length`）。
3. **中文歌全部罗马化收录**：`开不了口` → `Kai Bu Liao Kou.`；
   `超人不會飛` → `Superman Can't Fly / 超人不會飛 (Chao Ren Bu Hui Fei)`。
   直接搜中文关键词大概率零结果，这条知识写进 `Hint()`（见下）。
   解析器必须能处理带斜杠、括号、HTML 实体（`&#039;`）的脏标题。

## 包结构

新增 `internal/music`，遵循仓库既有的接口注入风格：

```
internal/music/
  source.go       // Source 接口 + Candidate DTO —— 扩展点
  mp3pm.go        // Source 的 mp3.pm 实现 —— 唯一含站点特有逻辑的文件
  agent.go        // LLM 工具调用循环 + Track DTO
  upload.go       // WebDAV 并发上传 + 文件名清洗
  report.go       // Reporter 接口 + Status 快照 + Telegram 原地编辑实现
  testdata/mp3pm_search.html   // 真实搜索结果页 fixture
```

### Source 接口

```go
// Candidate 是一条搜索结果。dlURL 小写 —— 不出包，也不进模型上下文。
type Candidate struct {
    ID       string // 源内唯一 id（mp3.pm 用 data-sound-id）
    Artist   string // 站点原始歌手名，可能是罗马化的
    Title    string // 站点原始歌名
    Duration string // "04:44"
    dlURL    string
}

// Source 是"一个音源"的统一形状。实现独立、形状统一：
// 加新源 = 新写一个文件实现本接口 + 在 main 里多注册一个，agent 循环一行不改。
type Source interface {
    Name() string // "mp3pm" → 工具名 search_mp3pm / download_mp3pm
    Hint() string // 写进工具 description 的站点特性说明书
    Search(ctx context.Context, query string) ([]Candidate, error)
    Download(ctx context.Context, c Candidate, w io.Writer) (written int64, err error)
}
```

`Hint()` 让"某个站怎么搜才有效"这条知识**跟着站点实现走**，不散落进全局 system prompt。
mp3.pm 的 `Hint()` 内容：

> 俄语站点，中文歌曲以拼音或英文译名收录（如"开不了口"存为"Kai Bu Liao Kou"）。
> 直接用中文关键词搜索大概率零结果，应改用拼音全名、英文译名，或只用歌名不带歌手。

`Download` 接收 `io.Writer` 而非文件路径：让"写到哪里"归调用方（agent）决定，
源只负责把字节流吐出来，也便于测试时写进 `bytes.Buffer`。
**50MB 上限由 agent 在这个 `io.Writer` 上强制**（一个计数 writer，超限即返回错误让
`io.Copy` 中断），不放进各源实现——否则每加一个源都要重复实现一遍同样的防御。

### 工具 schema 自动生成

agent 持有 `[]Source`，为每个源生成两个 tool：

```
search_<name>(query string)  → JSON 数组 [{id, artist, title, duration}, ...]
download_<name>(id string)   → 文本"下载完成，N 字节"（本地路径不给模型，它用不着）
```

`search_*` 的 description 拼上该源的 `Hint()`。

**`dlURL` 不进模型上下文**：那个 token 直链 200+ 字符，20 条候选就是 4000+ 字符白烧 token，
且容易被模型改坏。做法是 agent 每次 `Fetch` 持有一个**单次任务局部**的
`candidates map[string]Candidate`（key = `<源名>:<ID>`），`search_*` 只返回轻量字段，
`download_*` 只收一个 id，Go 侧查表拿真直链。该 map 随任务结束丢弃，天然无并发问题。

### Track

agent 的产出：

```go
type Track struct {
    LocalPath string // 临时目录下的 mp3 文件
    Artist    string // 模型规范化后的中文歌手名
    Title     string // 模型规范化后的中文歌名
    Duration  string // 站点给的时长
    Bytes     int64  // 实际写入字节数
    Source    string // "mp3pm"
    Tokens    int    // 累计 usage.TotalTokens
}
```

搜索次数不放进 `Track`——它属于进度信息，只存在于 `Status.Searches`，避免两处各记一份。

## LLM 循环

结构照搬 `internal/agent/agent.go`：同样的 `chatCompleter` 接口（便于注入 fake 测试）、
同样的"工具失败回灌而非中断"、同样的返回前 slog-dump 整段 `messages` 做审计、
同样的 token 累计。差异：

- `maxSteps = 6`（`internal/agent` 是 3）—— 要容纳"搜索→零结果→换词搜→再换词搜→下载→给结论"。
- **最终输出强制 JSON**，system prompt 明确要求，无工具调用的那一轮按 JSON 解析：

  ```json
  {"artist": "周杰伦", "title": "晴天", "id": "mp3pm:4287389"}
  ```

  `artist`/`title` 必须是**规范化后的中文原名**（把站点的 `Jay Chou - Qing Tian` 还原回来），
  用于拼文件名；`id` 用于校验模型确实下载了它最终认定的那首——**与本次实际下载的 id
  不一致即判任务失败**（说明模型自相矛盾，此时文件名与文件内容大概率对不上）。
- system prompt 要点：理解自由格式的歌曲描述（`歌曲 - 歌手`、`歌手 歌曲`、只有歌名等都要认）；
  零结果时按 `Hint()` 换关键词重试，**最多 3 次搜索**；从候选里避开 live 版、伴奏版、
  翻唱、remix，除非用户明确要；选定后必须调 `download_*`；最后只输出 JSON，不再调工具。

## 数据流

```
你 ── /music 晴天 周杰伦 ──▶ command.Bot.classify → {act: run, cmd: "music", args: "晴天 周杰伦"}
                                      │ 鉴权走现有 admin_ids
                                      ▼
                            go music.Runner.Run(ctx, chatID, rawQuery)
                                      │  ctx 打 task_id（logx.WithTaskID），沿用现有约定
                                      │  Bot 侧套 600s 总预算（同 summarizeTimeout 的做法）
                    ┌─────────────────┴──────────────────┐
                    │ 首次 Update → SendEditable，记下 msgID
                    ▼                                    │
              music.Agent.Fetch(ctx, rawQuery, reporter) │ 每步 reporter.Update(快照)
                    │   ├ search_mp3pm("Jay Chou Qing Tian")
                    │   ├ （零结果则换词重搜，最多 3 次）
                    │   ├ 模型挑中 id
                    │   ├ download_mp3pm(id) → 写入 os.MkdirTemp 下的文件
                    │   └ 最终 JSON
                    ▼
              Track{...}
                    │
                    ▼
              music.Uploader.Upload(ctx, track, reporter)
                    │  两个 goroutine 并发 PUT，各自独立更新自己那行状态
                    ▼
              defer os.RemoveAll(tmpDir)   ← 无论成败
                    │
                    ▼
              reporter.Done(终态)  ← 那条消息定格
```

## 进度上报

### notify.Editor

`notify` 包新增一个小接口，**现有 `Notifier` 三个方法的语义与签名一字不动**：

```go
// Editor 是"可原地编辑的消息通道"。与 Notifier 分开定义：
// Notifier 的语义是"发出去就不管了"，塞进编辑方法会让 monitor 那些
// 只推不改的调用方也被迫认识 msgID。
type Editor interface {
    SendEditable(ctx context.Context, chatID int64, md string) (msgID int, err error)
    Edit(ctx context.Context, chatID int64, msgID int, md string) error
}
```

`*notify.Telegram` 实现它，**复用同一把 `mu`/`lastSend` 锁**（走现有的 `send` 路径，
`Edit` 用 `tgbotapi.NewEditMessageText`）。不给编辑开小灶，因为 Telegram 的限额按 bot 计。
两个方法都发 MarkdownV2 且**不做整体转义**——Reporter 自己渲染标记、只转义动态片段，
与 `NotifyMarkdown` 的约定一致。

### Reporter

```go
// Status 是一次任务的完整快照。覆盖式而非增量：每个快照自洽，
// 所以节流时丢弃中间态是安全的。
type Status struct {
    Query    string          // 用户原始输入
    Stage    Stage           // 搜索中 / 下载中 / 上传中 / 完成 / 失败
    Searches int             // 已搜索次数
    Found    int             // 候选数
    Track    *Track          // 选定后才非 nil
    Targets  []TargetStatus  // {Name, State(待/进行中/成功/失败), Err}
    Err      string          // 失败原因
}

type Reporter interface {
    Update(ctx context.Context, s Status) // 覆盖式，实现负责渲染与节流
    Done(ctx context.Context, s Status)   // 终态，无条件立即发出
}
```

Telegram 实现的节流规则：

- 距上次**实际发出** < 3s 的 `Update` 直接丢弃，只在内存里存最新快照；
- 一个后台 timer 在节流窗口结束时把最新快照补发一次（保证中间态不会永久丢失）；
- `Done` **无条件立即发送**，不受节流限制——最后一条必须准确；
- 首次 `Update` 走 `SendEditable` 拿 `msgID`，之后走 `Edit`。

Reporter 接口本身不认识 Telegram，单测时塞个记录全部快照的 fake 即可，不用真 bot。

渲染示例（进行中 / 终态）：

```
🎵 晴天 周杰伦
✅ 搜索 mp3.pm · 换词 2 次 · 12 个候选
✅ 选定 周杰伦 - 晴天 · 03:58
✅ 下载完成 · 8.4 MB
⬆️ 上传中
   ✅ 阿里云盘
   ⏳ OneDrive
```

```
🎵 晴天 周杰伦
✅ 周杰伦 - 晴天 · 03:58 · 8.4 MB · mp3.pm
⬆️ 上传
   ✅ 阿里云盘
   ❌ OneDrive: 507 Insufficient Storage
本次消耗 tokens: 3184
```

**已知代价**：编辑占用全局发送锁，`/music` 运行期间 HN/V2EX 的推送会被轻微挤占。
一次典型任务（20-60s）约产生 7-20 次编辑请求，每次最多多占 1.5s。
单人使用场景下可接受；若实测偏慢，把节流常量从 3s 调到 5s 即可。

## 上传

```go
type Target struct {
    Name string // "阿里云盘"，只用于展示
    URL  string // "http://alist:5244/dav/aliyun/Music"
}
```

- 两个目标各起一个 goroutine 并发 PUT `<URL>/<歌手> - <歌名>.mp3`，Basic Auth 用同一组凭据。
- 每个目标独立超时 300s；每完成一个就 `reporter.Update` 刷新自己那行。
- **`Uploader` 内部按 `[]Target` 循环**，不硬编码"两个"——加第三个网盘只需在配置和
  `main` 组装处各加两行。
- 每个目标各自打开一次本地文件（`os.Open`），不共享 `io.Reader`——并发读同一个 `*os.File`
  会因共享文件偏移量而互相打乱。
- 文件名清洗：模型输出的歌名可能含 `/ \ : * ? " < > |`，逐个替换为 `_`，
  并用 `tools.TruncateUTF8` 限长（rune 感知，遵循仓库既有约定，不按字节切）。

## 配置

```toml
[music]
webdav_user   = "alist"
webdav_pass   = "xxxxxx"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://alist:5244/dav/onedrive/Music"
```

六个平铺字段，无数组、无循环配置。`main` 组装成 `[]music.Target` 传给 `Uploader`。

**校验策略**（`config.FromFile`）：

- **六项全空** → `/music` 功能关闭：不注册进命令清单，收到指令回"音乐功能未配置"。
  **不报错退出**——现有部署没有 `[music]` 段，不能因为升级就起不来。
- **填了一部分** → 直接返回 error 启动失败。半配置一定是打字漏了，
  静默降级只会让人对着"上传失败"抓瞎。

`config.toml`（进 git 的模板）里这几项留空占位并加注释说明；真值只写 `myconfig.toml`。

## 错误处理

| 情况 | 处理 | 理由 |
|---|---|---|
| 搜索零结果 | **回灌给模型**："零结果。该站中文歌按拼音/英文收录，可改用拼音全名 / 英文译名 / 只用歌名再试" | 正常路径而非错误 |
| 搜索 HTTP / 解析失败 | 回灌错误文本，模型可重试或换源 | 沿用 `agent.go:183` 的"失败回灌不中断" |
| 下载失败 | 回灌，模型可改选另一个候选 | 同上 |
| 下载超 **50MB** | 中断该次下载、删除半成品、回灌"文件过大" | 防御模型选中错误条目把磁盘写爆 |
| 模型最终输出非 JSON | **任务失败**，进度消息展示模型原始输出（截断） | 便于排查模型在想什么；不做兜底解析 |
| 模型没调过 `download_*` 就给 JSON | 任务失败 | 没有文件可传 |
| 最终 JSON 的 `id` 与实际下载的不一致 | 任务失败 | 模型自相矛盾，文件名与内容大概率对不上 |
| `maxSteps = 6` 耗尽 | 任务失败，消息注明"模型未收敛" | 同 `agent.go:157` |
| 单个 WebDAV 目标失败 | **不中断**，另一个继续；终态如实列出该目标的错误 | 不为一个网盘挂掉丢弃已下好的歌 |
| 两个目标全失败 | 任务失败 | |
| 任何路径 | `defer os.RemoveAll(tmpDir)` | 成败都删，绝不留垃圾 |
| Reporter 自身发送失败 | 仅 `slog.ErrorContext`，不影响主流程 | 进度是辅助信息，不该反过来搞挂任务 |

超时预算：

| 阶段 | 超时 |
|---|---|
| agent 循环（搜索 + 下载） | 180s |
| 每个 WebDAV 目标上传 | 300s |
| 整个 `/music` 任务 | 600s |

并发不设上限：与 `/summary` 一致 `go` 出去，连发多条就并行跑，各自编辑各自的消息。
仅管理员可用，量本来就小。

## 对现有代码的改动

1. **`command.Bot.classify` 重构。** 现签名 `classify(msg) (action, url)`，
   `actionSummarize` 绑死了 summary 语义。改为：

   ```go
   type intent struct {
       act  action // ignore / unauthorized / usage / run
       cmd  string // "summary" | "music"
       args string
   }
   ```

   纯函数性质不变（不发消息、不调 agent），现有 `bot_test.go` 的用例改造后继续跑
   （它锁的是决策树而非签名）。`actionUsage` 的提示文案按 `cmd` 分支。
   **这是本次唯一必要的存量重构**，不顺手改别的。

2. **`Bot` 新增依赖 `musicRunner`**（本包定义的消费侧接口，同 `Summarizer` 的做法，
   不直接 import `music` 包）：

   ```go
   type musicRunner interface {
       Run(ctx context.Context, chatID int64, query string) error
   }
   ```

   `Run` 内部完成 agent + 上传 + 进度全流程；`Bot` 只负责鉴权与派发。
   `[music]` 未配置时该依赖为 nil，`/music` 直接回"未配置"。

3. **`notify.Telegram` 新增 `SendEditable` / `Edit`**，实现 `notify.Editor`。现有方法不动。

4. **`setMyCommands` 增注册 `/music`**（仅在 `[music]` 配置齐全时）。

5. **`main.go`** 组装：`music.NewMp3PM(httpClient)` → `music.NewAgent(apiKey, agentModel, sources...)`
   → `music.NewUploader(httpClient, targets, user, pass)` → `music.NewRunner(agent, uploader, tgClient)`
   → 传进 `command.NewBot`。共享现有的 `*http.Client`。

6. **`CLAUDE.md`** 补：`internal/music` 的接口清单条目、`notify.Editor` 的存在与转义约定、
   `[music]` 配置段、`command.Bot` 现在认两条指令。

## 测试

全部离线，`go test -short ./...` 可跑完，**不新增任何真实网络或花钱的测试**：

1. **`mp3pm_test.go`** —— 把真实搜索结果页存成 `testdata/mp3pm_search.html`，
   断言解析出的 `Candidate` 字段正确，重点覆盖脏标题：
   `Superman Can't Fly / 超人不會飛 (Chao Ren Bu Hui Fei)`（斜杠 + 括号 + HTML 实体 `&#039;`）。
   另用 `httptest` 覆盖完整两跳："POST api.search.php 拿到重定向 URL → GET 结果页"，
   以及"api.search.php 返回非 URL 内容"的失败分支。
2. **`agent_test.go`** —— fake `chatCompleter` + fake `Source`。覆盖：一次搜中的正常路径、
   **零结果换词重搜后搜中**、下载失败后改选另一候选、`maxSteps` 耗尽、模型输出非 JSON、
   下载超 50MB 被中断。
3. **`upload_test.go`** —— `httptest` 起假 WebDAV。覆盖：两目标都成功、
   一成一败（断言仍算成功且错误信息带目标名）、全失败、Basic Auth 头正确、
   PUT 路径就是清洗后的文件名。
4. **`upload_test.go` 的文件名清洗子测试** —— `AC/DC - Back in Black` 里的 `/` 必须被替换，
   否则 WebDAV 路径会被打歪；超长歌名按 rune 截断不产生乱码。
5. **`report_test.go`** —— fake `Editor` 记录所有调用。覆盖：3s 内的连续 `Update` 被合并成一次、
   节流窗口结束时补发最新快照、`Done` 无条件立即发出、首次走 `SendEditable` 之后走 `Edit`、
   `Editor` 报错时不 panic 且不影响后续。
6. **`bot_test.go`** —— 扩 `classify` 用例：`/music` 白名单内/外、空参数、
   `/summary` 原有行为不回归。
7. **`config_test.go`** —— `[music]` 全空可通过、部分填写报错。

验收注意：用 `go test -short ./...`（`internal/agent` 的 e2e 测试会真实调用 DeepSeek API
产生费用）；**不要**用 `gofmt -l .` 当门禁（本仓库因 CRLF 天然列出二十余个文件），
只检查本次新增/修改的文件。

## 风险与已知取舍

- **mp3.pm 的 HTML 结构随时可能变**，解析器会静默失效（解析出 0 条候选，
  表现为"搜不到"而非报错）。缓解：解析出 0 条但 HTTP 200 且响应体非空时，
  日志里额外打一条 WARN 并附响应体长度，便于区分"真没这首歌"和"页面改版了"。
- **`data-download-url` 里的 token 是否有时效未验证**。若过期，表现为搜索后隔很久才下载会 403。
  当前设计里搜索与下载在同一次任务内、间隔通常几秒，风险低；真撞上就在 `Download`
  失败时回灌，让模型重新搜索一次。
- **进度编辑挤占全局发送锁**，`/music` 期间 HN/V2EX 推送变慢。已确认可接受。
- **模型可能选错歌**（挑到伴奏版/翻唱）。这是 LLM 判断的固有风险，缓解手段是把
  候选的完整原始标题给模型看（不做截断），并在 system prompt 里明示要避开的版本类型。
  选错了重打一次 `/music` 即可——这也是不做去重的原因之一。
- **换词重搜会放大 token 与耗时**，一次任务最坏 6 轮模型调用。`maxSteps` 是唯一的刹车。
- **两个目标同名文件覆盖上传**：WebDAV PUT 语义即覆盖，重复下载同一首歌会覆盖旧文件。
  这是期望行为（幂等），但也意味着**下到差版本会覆盖掉之前的好版本**。已知取舍，不做备份。
- **`[music]` 未配置时 `/music` 静默不可用**，与仓库"缺配置即启动失败"的既有哲学不一致。
  差异是有意的：`[storage]`/`[deepseek]` 是核心链路，`[music]` 是可选功能，
  强制必填会让现有部署升级即挂。
- **版权**：本功能从公开 mp3 站点下载音频到个人网盘，仅管理员本人可用，
  不提供分发、不做批量抓取、不绕过任何付费或 DRM 保护。
