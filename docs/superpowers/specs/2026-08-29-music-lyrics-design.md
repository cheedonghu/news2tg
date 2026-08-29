# /music 歌词下载 —— 设计

日期：2026-08-29
范围：`internal/music`

## 问题

`/music` 只下载 mp3。歌下到网盘里没有歌词，播放器（网盘自带的、本地的）都配不上词。

现有代码里有一条现成的线索：`musicso.cc` 的 `/api/play.php` 响应**已经带了 `lrc` 字段**，
只是被 `musicSoPlayResp` 明确注释着丢弃（`internal/music/musicso.go:220`）。

## 目标与非目标

**目标**

- 给 `/music` 加歌词，产物是与 mp3 同名同目录的 `.lrc` 文件，一起传上 WebDAV。
- 把「取歌词」做成 `Source` 接口的**一项能力**：每个音源自己表态供不供词，
  不供词的音源就只下歌。

**非目标（本次明确不做）**

- 不做跨站配词：音源只为**自己站里的候选**供词（手上有 id 和会话的那条快路径）。
  从 mp3.pm 下的歌因此没有歌词。
- 不把歌词写进 mp3 的 ID3 标签。
- 不把歌词文本发到 Telegram（几十行歌词会淹掉那条进度消息）。
- LLM **完全不参与**取词。没有新工具、不动 `defaultMaxSteps` / `agentTimeout`
  那对耦合常量、不多烧 token。
- 不加任何新配置项。`cmd/news2tg/main.go` **一行都不改**。

## 关键决策与理由

### 1. 歌词是 `Source` 的一项能力，不是独立接口

`Source` 增加第五个方法：

```go
type Source interface {
    Name() string
    Hint() string
    Search(ctx context.Context, query string) ([]Candidate, error)
    Download(ctx context.Context, c Candidate, w io.Writer) (int64, error)
    // Lyric 取回该候选的歌词文本。
    // 不支持歌词的音源返回 errLyricUnsupported；支持但站点没收录这首，
    // 返回 ("", nil) —— "这首没词"是正常路径，不是错误。
    Lyric(ctx context.Context, c Candidate) (string, error)
}
```

为什么长在 `Source` 上而不是另开一个可选接口：**它逼每个新音源作者对歌词明确
表态**。可选接口那种「实现了就自动生效、没实现就静默没有」很容易在加音源时漏掉，
而漏掉的表现是"这个源下的歌永远没词"这种没人会去查的静默缺失。写进接口以后，
编译器会替我们要这个答案。

代价是接口变宽了一格，且**只供词、不供歌**的站（比如某个专门的 LRC API）没法
直接接进来——那种站不是 `Source`，没有 `Search`/`Download` 可实现。这是本次
接受的取舍：真需要时再引入独立的歌词接口，代价是那一次要动 `runner.go`。

两个实现：

- `*MusicSo` —— 复用 `play.php` 那一跳里被丢弃的 `lrc` 字段。
- `*Mp3PM` —— 返回 `errLyricUnsupported`，一行明确表态"本站不供词"。

### 2. 两种"没有歌词"必须分得开

```go
// errLyricUnsupported 是"该音源不提供歌词"的哨兵错误，供 errors.Is 判别。
// 同 errTooLarge / errSourceUnavailable 的既有风格。
var errLyricUnsupported = errors.New("该音源不提供歌词")
```

「这个音源不供词」和「供词但这首没收录」在界面上是两句不同的话。合并成一句
「无歌词」的代价很具体：日后 musicso 改版导致 `lrc` 永远解析不出来时，界面上
跟「这首歌真的没词」长得一模一样，没人会发现有东西坏了。

### 3. Runner 编排，不放进 agent

取词跟上传站在同一格：都是「目标固定、无需模型判断」的收尾动作。`runner.go`
保持唯一编排层的角色，`agent.go` 只多填两个字段（见下）。

链路：

```
agent.Fetch  →  Track（私有字段带着 src 与 cand）
                  ↓
       lyrics.Save(ctx, track, dir, filename)   ← 新增
                  ↓  写 <base>.lrc 到同一个临时目录
       uploader.Upload(ctx, files, onProgress)  ← files 从 1 个变 1~2 个
                  ↓  defer os.RemoveAll(dir) 成败都删（已有）
```

### 4. `Track` 直接带上音源，不建注册表

`Track` 新增两个包内私有字段：

```go
src  Source    // 下这首歌的那个音源，取词时直接调它
cand Candidate // 那条候选，Lyric 需要它的 id 与会话
```

`agent.doDownload` 在下载成功那一刻把两者一起填进去（此刻它手上正好都有）。

替代方案是让 Runner 也持一张 `map[string]Source` 注册表、按 `track.Source` 查。
不采用的理由：那会多出一个「查不到」的分支——一个本不该发生、却必须写代码
应付的状态。带着源走，这个状态从根上不存在。

`Candidate` 里的 `dlURL`/`dlCookie` 本来就是包内可见、绝不出包、绝不进模型
上下文，挂在 `Track` 上不改变这条约束。`agent.finalize` 里 `nt := *r.track`
是值拷贝，两个新字段（含接口值）都自动跟着走，逻辑不用改。

### 5. 歌词结果不写进 `Track`

`report.go` 有硬约定：*一个 `*Track` 一旦被塞进 `Status` 交出去，就永远不再改*。
`Fetch` 返回的 Track 是 `finalize` 新造的、尚未进过任何快照，Runner 手上这一刻
确实是能安全改它的窗口，但那个窗口在 `runner.go` 的 `st.Track = track` 一行就关死了。

与其让后来人去推敲窗口开着没有，不如**根本不往 Track 上写歌词结果**：
`.lrc` 的本地路径只作 Runner 的局部变量传给 `Upload`，歌词状态另走 `Status.Lyric`。

（注意第 4 点那两个字段不受这条约束影响：它们由 agent 在 Track 出生时填好，
此后同样再没人改过。）

## 组件改动清单

### `internal/music/source.go`

- `Source` 接口增加 `Lyric` 方法（见决策 1），并在接口注释里说明两种"没词"的约定。
- 包顶部的分工注释增加 `lyric.go` 一行。

### `internal/music/lyric.go`（新增）

```go
var errLyricUnsupported = errors.New("该音源不提供歌词")

// Lyrics 是取词并落盘这一步。无状态，抽成类型只为让 Runner 能注入 fake 单测。
type Lyrics struct{}

// Save 取词并落盘，返回 .lrc 的本地路径（无词时为空）与该步的状态。
// filename 由调用方用 BuildLyricFilename 拼好，Save 不参与命名。
// **永不返回 error**：歌词是附赠品，失败信息通过 LyricStatus.Err 呈现。
func (Lyrics) Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)
```

签名里没有 `error` 是刻意的：让调用方在**类型层面**就没有「把它当致命错误」的
选项，比靠注释约束可靠。同 `ai.DeepSeek.Summarize` 返回兜底字符串加 `nil` 的手法。

命名不在 `Save` 里做：`.lrc` 与 `.mp3` 必须同名，让两个文件名由同一处
（`upload.go` 的 `buildBase`）产出，是这条约束唯一的保障点。

`Save` 内部：`t == nil || t.src == nil` 时按 `LyricUnsupported` 返回（防御性的，
正常路径下 agent 一定填了）；否则调 `t.src.Lyric(ctx, t.cand)` →
`errors.Is(err, errLyricUnsupported)` 即 `LyricUnsupported` 直接返回；
其它错误 → `LyricFailed`；空字符串 → `LyricMissing`；
有内容 → 写 `filepath.Join(dir, filename)`（UTF-8，不加 BOM，内容原样落盘）
→ `LyricOK`；落盘出错 → `LyricFailed` 并**删掉半成品文件**。

### `internal/music/musicso.go`

- `musicSoPlayResp` 增加一个 tag 为 `json:"lrc"` 的 `LRC string` 字段，
  并把那句"其余（lrc / pic）自动丢弃"的注释改掉。
- 把 `Download` 的第一跳抽成私有方法
  `func (m *MusicSo) play(ctx context.Context, c Candidate) (musicSoPlayResp, error)`，
  `Download` 取其中的 `url`，`Lyric` 取其中的 `lrc`。候选 id 拆
  `<backend>-<id>`、`PHPSESSID`、`musicSoChallenge` 这些站点知识都留在这个方法里，
  两个调用方都不重复。
- 新增 `Lyric` 方法：取 `pr.LRC`，`TrimSpace` 后为空即 `("", nil)`（未收录），
  否则**原样返回**。已实测确认它是内联的标准 LRC 正文（见下文「实测结论」），
  不需要二次请求、不需要解码。
- `play` 方法把响应体的读取改成带上限的
  `io.ReadAll(io.LimitReader(resp.Body, maxPlayRespBytes))`，
  `maxPlayRespBytes = 1 << 20`。现有代码这里是**无上限**的 `io.ReadAll`，
  歌词进来之后响应体从几百字节涨到几 KB，顺手把这个既有的无界读一起收掉。
  上限盖住整个 JSON 响应，`Download` 与 `Lyric` 同时受益。
  实测真实响应约 3 KB，1 MB 有 300 倍余量。
- Cloudflare 质询沿用 `musicSoChallenge`：返回**明确错误**，不伪装成「没有歌词」。

### `internal/music/mp3pm.go`

新增 `Lyric` 方法，直接 `return "", errLyricUnsupported`，带一句注释说明
mp3.pm 的页面里没有歌词可抓。

### `internal/music/agent.go`

`doDownload` 里构造成功的 `Track` 时多填 `src: src, cand: c`。仅此一处。

### `internal/music/report.go`

```go
type LyricState int
const (
    LyricUnsupported LyricState = iota // 该音源不提供歌词（如 mp3pm）
    LyricMissing                       // 音源供词，但站点没收录这首
    LyricOK
    LyricFailed                        // 请求/解析/落盘出错
)
type LyricStatus struct { State LyricState; Err string }
```

`Track` 增加私有字段 `src Source` 与 `cand Candidate`（见决策 4）。
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

`<源名>` 取自 `s.Track.Source`。目标行在 `Warn` 非空时追加
`⚠️ 歌词未传上: <80 字截断>`。所有动态文本照旧过 `tools.EscapeMarkdownV2`
（整条消息是预渲染 MarkdownV2，Editor 不再整体转义）。

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

- 新增消费侧小接口（同现有 `fetcher` / `uploader` 的风格）：

  ```go
  type lyricSaver interface {
      Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)
  }
  ```

  `Runner` 增加 `lyrics lyricSaver` 字段，由 `NewRunner` 填上默认的 `Lyrics{}`。
  **`NewRunner` 的签名不变**，测试在同包内直接改这个私有字段注入 fake ——
  同 `Uploader.timeout` / `Agent.maxBytes` / `tgReporter.terminalTimeout`
  那套"抽成字段以便测试"的既有做法。这也是 `main.go` 不用改的原因。
- `Fetch` 与 `Upload` 之间插入取词，套一层 `lyricTimeout = 30 * time.Second`
  的独立超时。它就是一次 JSON 请求（可能加一次小文件 GET），不该有资格去啃外层
  `command.Bot` 那 600s 预算里 agent 和上传要用的部分。同 `agentTimeout` /
  `targetUploadTimeout` 的套路。
- 取词后 `st.Lyric = &ls; rep.Update(ctx, *st)`，让进度消息立刻反映歌词结果。
- 组装 `[]UploadFile` 传给 `Upload`。临时目录的 `defer os.RemoveAll` 已经覆盖
  `.lrc`，无需额外清理。

### `cmd/news2tg/main.go`

**不改动。** 歌词是 `Source` 的能力、`Lyrics` 由 `NewRunner` 内部装配，
接线层看不到这次变化。

### `CLAUDE.md`

补一句歌词链路的说明，与 `source.go` 顶部注释保持一致。

## 错误处理总表

| 情况 | 行为 |
|---|---|
| 音源不供词（mp3pm 下的歌） | `LyricUnsupported`，只传 mp3，任务正常 |
| 站点没收录这首的词（`lrc` 空） | `LyricMissing`，只传 mp3，任务正常 |
| 取词请求失败 / Cloudflare 质询 / 响应超 `maxPlayRespBytes` | `LyricFailed` + 原因，只传 mp3，任务正常 |
| `.lrc` 落盘失败 | `LyricFailed`，删半成品，只传 mp3，任务正常 |
| `.lrc` 上传到某目标失败 | 该目标仍 `TargetOK`，记 `Warn` |
| mp3 上传到某目标失败 | 该目标 `TargetFailed`（不变） |
| mp3 上传到**所有**目标都失败 | 整任务失败（不变） |

一句话：**歌词的任何问题都不能让一首已经下好的歌传不上去。**

## 测试

全部离线（fixture + `httptest`），不碰真网络、不碰真模型。验收命令
`go test -short ./...`（`-short` 的理由见 CLAUDE.md：不加会真调 DeepSeek 花钱）。

**`musicso_test.go`（扩充）** —— `play.php` 假服务端加歌词分支：

1. `lrc` 是内联 LRC 正文 → `Lyric` 原样返回，含 `[ti:]` 元信息行与多行 `\n`
   （fixture 取自下文实测那份真实响应，截短保留头尾）
2. `lrc` 为空串 → `("", nil)`，走「未收录」
3. `lrc` 只有空白字符 → 同样按「未收录」处理，不写出一个空 `.lrc`
4. Cloudflare 质询 → 返回错误而非空歌词
5. 响应体超过 `maxPlayRespBytes` → 报错（JSON 被截断，必然解析失败），
   且 `Download` 走同一条保护

抽出 `play` 之后 `Download` 行为一字不变——现有下载用例即回归门，不放宽。

**`mp3pm_test.go`（扩充）** —— `Lyric` 返回的错误满足
`errors.Is(err, errLyricUnsupported)`，且返回的歌词为空。

**`lyric_test.go`（新增）** —— 往 `Track.src` 里塞 fake `Source`，覆盖
`Lyrics.Save` 的四种结局：返回哨兵 → `LyricUnsupported` 且不落盘；返回空 →
`LyricMissing` 且不落盘；返回正文 → `LyricOK` 且文件名/内容正确；返回其它 error →
`LyricFailed`、path 为空、**不留半成品文件**。另验 `Save` 在所有分支
path 为空时状态都不是 `LyricOK`。

**`upload_test.go`（扩充）** —— 可选文件语义是本次风险最集中处：

- 可选文件传失败 → 目标仍 `TargetOK`，`Warn` 非空，整体不报错
- 必传文件失败 → `TargetFailed`，且**不再发起该目标的第二个 PUT**（假服务端记请求数）
- 两个目标、mp3 一成一败 → 整体成功（既有语义不回退）
- 所有目标的 mp3 都失败 → 整体报错（不变）
- 每个文件的 `Content-Type` 与 `Content-Length` 都正确
- `BuildFilename` 与 `BuildLyricFilename` 去掉扩展名后**逐字相等**，
  包括触发 120 rune 截断的超长输入——这是「播放器能配上对」的唯一保障

**`runner_test.go`（扩充）** —— 改 `Runner.lyrics` 塞 fake `lyricSaver`：
有词时 `Upload` 收到 2 个文件且第二个 `Optional=true`；无词/失败时只收到 1 个
文件且任务照样成功；`Status.Lyric` 正确进快照。

**`report_test.go`（扩充）** —— 四种 `LyricState` 各渲染一次，外加 `Lyric == nil`
时该行完全不出现；动态文本（`cf-ray=…`、目标名、源名）都经过 `EscapeMarkdownV2`。

**`agent_test.go`（扩充）** —— 下载成功后产出的 `Track` 带上了 `src` 与 `cand`，
且经 `finalize` 的值拷贝后仍然带着。

## 实测结论（2026-08-29）

对线上 `play.php` 发过真实请求，`lrc` 的形状已确认。

搜索 `/s/晴天` → HTTP 200，正常下发 `Set-Cookie: PHPSESSID=…`，未撞 Cloudflare 质询。
取其中一条候选调 `play.php`：

```
GET /api/play.php?id=0039MnYb0qxYhV&type=q   (Cookie: PHPSESSID=…)
HTTP 200，3008 字节

{"code":1,"msg":"成功",
 "url":"https://isure6.stream.qqmusic.qq.com/M800003Qui1q2u1Zho.mp3?guid=…",
 "lrc":"[ti:晴天]\n[ar:周杰伦]\n[al:叶惠美]\n[by:]\n[offset:0]\n[00:00.00]晴天 - 周杰伦 (Jay Chou)\n[00:02.25]词：周杰伦\n…",
 "pic":"https://y.gtimg.cn/music/photo_new/…"}
```

**结论：`lrc` 是内联的标准 LRC 正文。** 带 `[ti:]/[ar:]/[al:]/[offset:]` 元信息行、
`[mm:ss.xx]` 时间戳，换行在 JSON 里是 `\n`（`encoding/json` 解码后即真实换行），
UTF-8。**不是 URL，不是 base64，不需要二次请求或解码**，`json.Unmarshal` 出来
直接写进 `.lrc` 即可。

QQ（`type=q`）与网易云（`type=n`）两个后端各验一次，形状一致；网易云的时间戳
是三位小数（`[00:00.000]`），无关紧要，原样落盘。

响应键固定为 `code` / `msg` / `url` / `lrc` / `pic` 五个，与现有
`musicSoPlayResp` 的注释相符。

**因此本设计相对初稿删掉了「lrc 可能是 URL」那条分支**：两个后端的真实响应都是
内联正文，那条分支永远不会触发，却要一直背着「不能给它带 cookie」的注意事项。
万一站点日后真改成 URL 形态，它会落进「解析不出有效内容」的 WARN，日志能指到病根。

实施时把上面这份真实响应截短后固化成 `testdata/musicso_play.json`。

## 风险

| 风险 | 缓解 |
|---|---|
| `lrc` 形状日后改变（如换成 URL） | 已实测当前是内联正文并固化 fixture；真改了会落进 `LyricMissing` 并记 WARN，只影响 `Lyric` 内部一个分支 |
| 改 `Upload` 签名波及既有上传语义 | 「至少一个目标成功」只看必传文件；用例把既有语义逐条锁死 |
| 抽 `play` 时改坏 `Download` | 现有 `musicso_test.go` 的下载用例即回归门 |
| `Source` 接口变宽，只供词的站接不进来 | 已知取舍（见决策 1）；真需要时再引入独立歌词接口 |
| 歌词站改版导致长期静默无词 | `LyricMissing` 与 `LyricUnsupported` 分开显示，解析异常另记 WARN |
