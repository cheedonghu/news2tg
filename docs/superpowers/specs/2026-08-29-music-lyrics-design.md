# /music 歌词下载 —— 设计

日期：2026-08-29
范围：`internal/music`、`cmd/news2tg/main.go`

## 问题

`/music` 只下载 mp3。歌下到网盘里没有歌词，播放器（网盘自带的、本地的）都配不上词。

现有代码里有一条现成的线索：`musicso.cc` 的 `/api/play.php` 响应**已经带了 `lrc` 字段**，
只是被 `musicSoPlayResp` 明确注释着丢弃（`internal/music/musicso.go:220`）。

## 目标与非目标

**目标**

- 给 `/music` 加歌词，产物是与 mp3 同名同目录的 `.lrc` 文件，一起传上 WebDAV。
- 把「歌词源」抽象成本包第二个扩展点，musicso 是第一个实现。

**非目标（本次明确不做）**

- 不做跨站配词：歌词源只服务**自己站里的候选**（手上有 id 和会话的那条快路径）。
  从 mp3.pm 下的歌因此没有歌词。将来想要，实现方式是给 `LyricSource` 再加一个
  按 artist/title 搜索的实现，接口不用改。
- 不把歌词写进 mp3 的 ID3 标签。
- 不把歌词文本发到 Telegram（几十行歌词会淹掉那条进度消息）。
- LLM **完全不参与**取词。没有新工具、不动 `defaultMaxSteps` / `agentTimeout`
  那对耦合常量、不多烧 token。
- 不加任何新配置项。

## 关键决策与理由

### 1. 歌词是独立接口，但由已有的 `MusicSo` 类型实现

新增 `internal/music/lyric.go`：

```go
// LyricSource 是"一个歌词源"的统一形状，本包第二个扩展点。
type LyricSource interface {
    // Name 返回它供词的音源标识。与 Source.Name() 同名即表示
    // "这个音源下的歌由我供词"——歌词源靠这个跟候选对上号。
    Name() string
    // Fetch 取回歌词文本。"这首没词"返回 ("", nil)，是正常路径不是错误。
    Fetch(ctx context.Context, c Candidate) (string, error)
}
```

`*MusicSo` **同时实现 `Source` 和 `LyricSource`**，不另造 `MusicSoLyric` 类型。

理由：`play.php` 那一跳所需的全部知识——`PHPSESSID` 会话、Cloudflare 质询识别、
候选 id 拆成 `<backend>-<id>`——已经在 `musicso.go` 里了；歌词只是同一个响应里
被丢掉的一个字段。拆成两个类型会让同一段站点知识被两个文件共享，正好违反包顶部
「站点特有逻辑只允许待在各自的音源文件里」那条约定。一个类型实现两个接口在本仓库
有先例：`*notify.Telegram` 同时实现 `Notifier` 和 `Editor`。

落地时把 `Download` 的第一跳抽成私有方法：

```go
func (m *MusicSo) play(ctx context.Context, c Candidate) (musicSoPlayResp, error)
```

`Download` 取其中的 `url`，`Fetch` 取其中的 `lrc`。`musicSoPlayResp` 增加一个 tag 为 `json:"lrc"` 的
`LRC string` 字段，并把那句"其余（lrc / pic）自动丢弃"的注释改掉。

### 2. 注册用可选能力断言，不加配置项

`main` 从**已启用的音源实例**里挑出会供词的：

```go
if ls, ok := src.(music.LyricSource); ok {
    lyricSources = append(lyricSources, ls)
}
```

不加第二张工厂表、不加 `[music] lyric_sources` 之类的开关。于是
「musicso 启用 → 自动有歌词」，用户不必理解第二套配置。

副作用是明确的：`sources = ["mp3pm"]` 时 musicso 压根不被构造，歌词表为空，
任何歌都没词。这是对的——歌词目前只有 musicso 供得出，单给它一个开关只会
制造「开着但永远没词」的自相矛盾配置。

将来若出现**只供词、不供歌**的站，它实现 `LyricSource` 单独注册即可，接口撑得住。

### 3. Runner 编排，不放进 agent

取词跟上传站在同一格：都是「目标固定、无需模型判断」的收尾动作。`runner.go`
保持唯一编排层的角色，`agent.go` 一行不改。

链路：

```
agent.Fetch  →  Track（带私有 cand）
                  ↓
       lyrics.Save(ctx, track, dir, base)       ← 新增
                  ↓  写 <base>.lrc 到同一个临时目录
       uploader.Upload(ctx, files, onProgress)  ← files 从 1 个变 1~2 个
                  ↓  defer os.RemoveAll(dir) 成败都删（已有）
```

`Track` 新增包内私有字段 `cand Candidate`，把候选带出 agent。`Candidate` 里的
`dlURL`/`dlCookie` 本来就是包内可见、绝不出包、绝不进模型上下文，挂在 `Track`
上不改变这条约束。`agent.finalize` 里 `nt := *r.track` 是值拷贝，新字段自动跟着走。

### 4. 歌词结果不写进 Track

`report.go` 有硬约定：*一个 `*Track` 一旦被塞进 `Status` 交出去，就永远不再改*。
`Fetch` 返回的 Track 是 `finalize` 新造的、尚未进过任何快照，Runner 手上这一刻
确实是能安全改它的窗口，但那个窗口在 `runner.go` 的 `st.Track = track` 一行就关死了。

与其让后来人去推敲窗口开着没有，不如**根本不往 Track 上写歌词结果**：
`.lrc` 的本地路径只作 Runner 的局部变量传给 `Upload`，歌词状态另走 `Status.Lyric`。

## 组件改动清单

### `internal/music/lyric.go`（新增）

```go
type LyricSource interface { ... }   // 见上

type Lyrics struct{ sources map[string]LyricSource } // 源名 → 歌词源

func NewLyrics(ss []LyricSource) *Lyrics

// Save 取词并落盘，返回 .lrc 的本地路径（无词时为空）与该步的状态。
// filename 由调用方用 BuildLyricFilename 拼好，Save 不参与命名。
// **永不返回 error**：歌词是附赠品，失败信息通过 LyricStatus.Err 呈现。
func (l *Lyrics) Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)
```

签名里没有 `error` 是刻意的：让调用方在**类型层面**就没有「把它当致命错误」的
选项，比靠注释约束可靠。同 `ai.DeepSeek.Summarize` 返回兜底字符串加 `nil` 的手法。

命名不在 `Save` 里做：`.lrc` 与 `.mp3` 必须同名，让两个文件名由同一处
（`upload.go` 的 `buildBase`）产出，是这条约束唯一的保障点。

`Save` 内部：按 `t.Source` 查注册表 → 未命中即 `LyricUnsupported` 直接返回；
命中则调 `Fetch`，空字符串 → `LyricMissing`；有内容 → 写 `filepath.Join(dir, filename)`
（UTF-8，不加 BOM，内容原样落盘）→ `LyricOK`；`Fetch` 或落盘出错 → `LyricFailed`，
并**删掉可能的半成品文件**。

### `internal/music/musicso.go`

- `musicSoPlayResp` 增加 `LRC string` 字段，tag 为 `json:"lrc"`。
- 抽出私有 `play` 方法，`Download` 改为调它。
- 新增 `Fetch` 方法实现 `LyricSource`；加编译期断言 `var _ LyricSource = (*MusicSo)(nil)`。
- `lrc` 内容形状**两种都吃**：以 `http://` / `https://` 开头就再 GET 一次拿正文
  （**不带 cookie**，同 CDN 直链那条理由：不把会话泄漏给第三方），否则当作正文本身。
  两者都取不到有效内容时记一条 WARN 并按「未收录」处理。
- 取词加 `maxLyricBytes = 1 << 20` 上限，防站点返回巨大响应撑爆内存。
  同 `defaultMaxBytes` 的思路。超限时**返回错误**，不返回截断后的半截歌词——
  半截 LRC 传上网盘比没有更糟（同 `doDownload` 删下载半成品的理由）。
- Cloudflare 质询沿用 `musicSoChallenge`：返回**明确错误**，不伪装成「没有歌词」。

### `internal/music/report.go`

```go
type LyricState int
const (
    LyricUnsupported LyricState = iota // 该音源没有歌词源（如 mp3pm）
    LyricMissing                       // 有歌词源，但站点没这首歌的词
    LyricOK
    LyricFailed                        // 请求/解析/落盘出错
)
type LyricStatus struct { State LyricState; Err string }
```

`Status` 增加 `Lyric *LyricStatus`（nil = 还没跑到这一步，用指针的理由同 `Track`）。
`TargetStatus` 增加 `Warn string`（可选文件传失败时的说明）。

`renderStatus` 在曲目行与上传区之间插一行：

| 状态 | 渲染 |
|---|---|
| `LyricOK` | `✅ 歌词` |
| `LyricUnsupported` | `➖ 歌词：<源名> 不提供` |
| `LyricMissing` | `➖ 歌词：站点未收录` |
| `LyricFailed` | `⚠️ 歌词获取失败: <80 字截断>` |
| `Lyric == nil` | 整行不出现 |

目标行在 `Warn` 非空时追加 `⚠️ 歌词未传上: <80 字截断>`。所有动态文本照旧过
`tools.EscapeMarkdownV2`（整条消息是预渲染 MarkdownV2，Editor 不再整体转义）。

### `internal/music/upload.go`

```go
type UploadFile struct {
    LocalPath, Filename, ContentType string
    Optional bool // true = 这个文件传失败不算目标失败
}
func (u *Uploader) Upload(ctx context.Context, files []UploadFile,
                          onProgress func([]TargetStatus)) ([]TargetStatus, error)
```

每个目标的 goroutine 内部**顺序**传自己那份文件，mp3 在前、lrc 在后
（ctx 中途断掉时，先落地的是要紧那个）。`Content-Type` 从写死的 `audio/mpeg`
挪进 `UploadFile`；`.lrc` 用 `text/plain; charset=utf-8`。

- 必传文件失败 → 该目标 `TargetFailed`，**不再发起该目标后续文件的 PUT**。
- 可选文件失败 → 目标仍 `TargetOK`，说明记入 `Warn`。
- 「全部目标失败才算任务失败」只看必传文件，语义不变——不为一个附赠的歌词文件
  把已经传上去的歌判死。这延续「不为一个网盘挂掉丢弃已下好的歌」的既有约定。

文件名拆分（`BuildFilename` 对外签名不变，现有调用点与测试不受影响）：

```go
func buildBase(artist, title string) string           // 清洗 + 兜底 + 120 rune 截断
func BuildFilename(artist, title string) string       // buildBase + ".mp3"
func BuildLyricFilename(artist, title string) string  // buildBase + ".lrc"
```

`.lrc` 与 `.mp3` 的主体必须**逐字一致**（含截断），否则播放器配不上对。

### `internal/music/runner.go`

- 新增消费侧小接口 `lyricSaver`（同现有 `fetcher` / `uploader` 的风格），
  `NewRunner` 多收一个 `*Lyrics`。
- `Fetch` 与 `Upload` 之间插入取词，套一层 `lyricTimeout = 30 * time.Second`
  的独立超时。它就是一次 JSON 请求（可能加一次小文件 GET），不该有资格去啃外层
  `command.Bot` 那 600s 预算里 agent 和上传要用的部分。同 `agentTimeout` /
  `targetUploadTimeout` 的套路。
- 取词后 `st.Lyric = &ls; rep.Update(...)`，让进度消息立刻反映歌词结果。
- 组装 `[]UploadFile` 传给 `Upload`。临时目录的 `defer os.RemoveAll` 已经覆盖
  `.lrc`，无需额外清理。

### `cmd/news2tg/main.go`

在已有的音源构造循环里顺带收集 `[]music.LyricSource`（类型断言，见决策 2），
`music.NewLyrics(lyricSources)` 后传给 `music.NewRunner`。不新增配置读取。

### `CLAUDE.md`

补一句歌词链路的说明，与包顶部注释保持一致。

## 错误处理总表

| 情况 | 行为 |
|---|---|
| 该音源没有歌词源（mp3pm 下的歌） | `LyricUnsupported`，只传 mp3，任务正常 |
| 站点没有这首歌的词（`lrc` 空） | `LyricMissing`，只传 mp3，任务正常 |
| 取词请求失败 / Cloudflare 质询 / 解析失败 | `LyricFailed` + 原因，只传 mp3，任务正常 |
| `.lrc` 落盘失败 | `LyricFailed`，删半成品，只传 mp3，任务正常 |
| `.lrc` 上传到某目标失败 | 该目标仍 `TargetOK`，记 `Warn` |
| mp3 上传到某目标失败 | 该目标 `TargetFailed`（不变） |
| mp3 上传到**所有**目标都失败 | 整任务失败（不变） |

一句话：**歌词的任何问题都不能让一首已经下好的歌传不上去。**

## 测试

全部离线（fixture + `httptest`），不碰真网络、不碰真模型。验收命令
`go test -short ./...`（`-short` 的理由见 CLAUDE.md：不加会真调 DeepSeek 花钱）。

**`musicso_test.go`（扩充）** —— `play.php` 假服务端加歌词分支：

1. `lrc` 是内联正文 → `Fetch` 原样返回
2. `lrc` 是 URL → 再 GET 一次拿正文，且**那一跳不带 Cookie 头**
3. `lrc` 为空串 → `("", nil)`
4. Cloudflare 质询 → 返回错误而非空歌词
5. 超过 `maxLyricBytes` → 报错，不返回半截歌词

抽出 `play` 之后 `Download` 行为一字不变——现有测试即回归门。

**`lyric_test.go`（新增）** —— `Lyrics.Save` 四种结局各一例：注册表未命中 →
`LyricUnsupported` 且不落盘；歌词源返回空 → `LyricMissing`；返回正文 → `LyricOK`
且文件名/内容正确；返回 error → `LyricFailed`、path 为空、**不留半成品文件**。

**`upload_test.go`（扩充）** —— 可选文件语义是本次风险最集中处：

- 可选文件传失败 → 目标仍 `TargetOK`，`Warn` 非空，整体不报错
- 必传文件失败 → `TargetFailed`，且**不再发起该目标的第二个 PUT**（假服务端记请求数）
- 两个目标、mp3 一成一败 → 整体成功（既有语义不回退）
- 所有目标的 mp3 都失败 → 整体报错（不变）
- 每个文件的 `Content-Type` 与 `Content-Length` 都正确
- `BuildFilename` 与 `BuildLyricFilename` 去掉扩展名后**逐字相等**，
  包括触发 120 rune 截断的超长输入——这是「播放器能配上对」的唯一保障

**`runner_test.go`（扩充）** —— 塞一个 fake `lyricSaver`（不碰真歌词源）：有词时 `Upload` 收到 2 个文件且第二个
`Optional=true`；无词/失败时只收到 1 个文件且任务照样成功；`Status.Lyric` 正确进快照。

**`report_test.go`（扩充）** —— 四种 `LyricState` 各渲染一次，外加 `Lyric == nil`
时该行完全不出现；动态文本（`cf-ray=…`、目标名）都经过 `EscapeMarkdownV2`。

## 实施前必须先确认的一件事

`play.php` 的 `lrc` 字段**真实形状未经实测**（现有 fixture 里是空串）：可能是
LRC 正文、可能是一个 URL、也可能是转义/编码过的文本。设计上已经对「正文」和
「URL」两种都做了处理，但实施的**第一步**应当是对线上接口发一次真实请求，
把实际形状确认下来并据此固化 fixture。若结果是第三种形状（如 base64），
只需调整 `MusicSo.Fetch` 内部的解码分支，本设计的其余部分不受影响。

## 风险

| 风险 | 缓解 |
|---|---|
| `lrc` 实际形状与预期不符 | 实施第一步实测；解析只影响 `Fetch` 内部一个分支 |
| 改 `Upload` 签名波及既有上传语义 | 「至少一个目标成功」只看必传文件；用例把既有语义逐条锁死 |
| 抽 `play` 时改坏 `Download` | 现有 `musicso_test.go` 的下载用例即回归门，不放宽 |
| 歌词站改版导致长期静默无词 | `LyricMissing` 会在进度消息里显示，且解析异常记 WARN |
