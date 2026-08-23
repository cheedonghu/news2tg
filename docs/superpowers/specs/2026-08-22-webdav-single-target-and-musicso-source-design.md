# WebDAV 单目标 + musicso 音源 + 全局代理 · 设计

日期：2026-08-22
状态：待评审

## 目标

三件事，共用一次改动：

1. **WebDAV 只剩一个可用端点。** 现在的 `[music]` 段是"要么六项全配、要么整段不配"，
   只填一个目标会直接启动失败。改成"至少一个完整目标即可"。
2. **新增音源 musicso.cc。** 它把 QQ 音乐与网易云的结果合并排序后一次给出，
   歌名歌手是中文原名，正好补上 mp3.pm 的短板（俄语站，中文歌按拼音收录）。
3. **音源可开关、可排优先级。** 新增 `sources` 有序数组，顺序即优先级，不在列表里即关闭。

顺带引入一个全局 HTTP 代理开关，因为 musicso.cc 在 Cloudflare 后面、
且本项目的部署位置不确定（见"部署与网络约束"）。

## 范围与非目标

- **做**：`internal/music/musicso.go` 新音源；`[music]` 的 WebDAV 校验重写；
  `[music] sources` 有序数组；新增顶层 `[network] proxy`；agent 提示词与
  `maxSteps` 随多音源调整；`renderStatus` 修掉空时长的渲染尾巴；main 接线改为
  按配置顺序构造音源。
- **不做**：jbsou.cn 音源（评估后放弃，理由见附录）；酷狗覆盖（musicso 只有
  QQ + 网易）；WebDAV 目标改成 TOML 数组（仍是六个平铺字段）；无损/FLAC
  （始终取 mp3，保证 `.mp3` 后缀与 `audio/mpeg` 不说谎）；Cloudflare 质询的
  自动求解（只做识别与告警）。

## 关键决策与理由

| 决策 | 选择 | 理由 |
|---|---|---|
| WebDAV 目标数量 | **至少一个完整目标**，仍是六个平铺字段 | 需求只是"暂时只剩一个能用"，不是"要动态列表"；`Uploader` 本来就按 `[]Target` 循环，一行不用改 |
| 半个目标（有 name 无 url） | **仍然启动失败** | 沿用原有哲学：半配置一定是打字漏了，不该静默放行 |
| musicso 与 jbsou 二选一 | **只做 musicso** | musicso 一次 GET 即拿到会话+结果，且已合并 QQ/网易；jbsou 要 GET+POST 两跳、六个后端各查各的、`songid` 类型还不一致 |
| musicso 建模 | **一个 `Source`**，不按后端拆 | 站点自己已经合并排序；拆成多个源只会让工具数翻倍、烧 token，而模型无从判断"这首歌在哪家有" |
| 候选 ID 形态 | `q-0039MnYb0qxYhV`（后端标识拼进 id） | 照抄站点 href 自己的写法；`Candidate` 不必为此多一个私有字段 |
| 会话（PHPSESSID） | **每次 `Search` 现拿现用**，存进候选私有字段 | 会话与搜索是同一个请求，不存在"会话过期导致假零结果"的窗口；跟着候选走则天然无共享状态，并发的两次 `/music` 不会互相冲掉会话 |
| 搜索零结果 | **直接当没这首歌，不重试** | 见上，二义性已在源头消除 |
| Cloudflare 质询 | **显式识别 + 明确报错**，不当零结果 | 质询若静默表现成"搜不到"，用户拿到的是一条完全指不到病根的失败 |
| 音源开关与优先级 | **一个有序数组 `sources`** | 一个字段同时表达"开关"与"优先级"，不可能出现"开了但没优先级"或"两个源撞号"这种矛盾态 |
| 优先级如何生效 | **提示词按数组顺序列出音源**，Go 里不硬编码 | `buildSystemPrompt` 本就按切片顺序拼 Hint，只需加一句"按此顺序依次尝试" |
| `sources` 缺省 | **等价于全部启用**（默认顺序） | 现有部署的 `myconfig.toml` 没有这个字段，不能因为升级就起不来 |
| `sources = []` | **启动失败** | 显式写一个空数组一定是关错了地方 |
| 代理粒度 | **全局，要么全走要么全不走** | 用户明确要求；按主机分流交给外部代理程序（clash/v2ray 之类）的路由规则 |
| 代理落地方式 | **改 main 里的 Transport，不动任何构造函数签名** | 五个自建 client 的 `Transport` 全是 nil，会回落到 `http.DefaultTransport`；覆盖它即可 |

## 部署与网络约束（实测记录）

已在本机（中国 IP）于 2026-08-22 实测：

| 事实 | 证据 |
|---|---|
| musicso.cc 在 Cloudflare 后面，开着托管质询 | 非浏览器 UA 请求返回 `403` + `server: cloudflare` + `cf-mitigated: challenge` + `<title>Just a moment...</title>` |
| 该 403 与 IP 无关，是 UA/指纹门 | 同样的 403 在中国 IP 上用默认 curl UA 稳定复现 |
| **Go 的 `net/http` 带浏览器 UA 能通过** | 实测 `http=200`、`cf-mitigated` 为空、响应体含 `song-item`、`Set-Cookie: PHPSESSID` 正常下发 |
| CDN 直链无需任何 cookie | 裸 GET 拿到 `10792943` 字节 `audio/mpeg` |

**实际部署机已验证通过**（2026-08-22，用户在部署机上执行下面那条 curl，返回 `200`）。
因此本次上线**不需要**配 `[network] proxy`，musicso 直连即可。

**主要风险不是地域，是 Cloudflare 的 bot 评分**：它对机房/VPS 的 ASN 明显比对住宅
IP 更严，且评分是动态的——今天放行不等于永远放行（换机器、换出口 IP、CF 调整该
zone 的敏感度都可能让它翻脸）。`[network] proxy` 因此仍然要做，作为翻脸时的出口；
代码侧则必须保证质询真的发生时错误信息说得清楚（见下），而不是静默表现成"搜不到"。

换机器或出口 IP 变动后，用这条命令重新定论：

```bash
curl -s -o /dev/null -w "%{http_code}\n" \
  -A "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36" \
  https://www.musicso.cc/s/%E5%91%A8%E6%9D%B0%E4%BC%A6
```

`200` 即通；`403` 则该机器上需要配 `[network] proxy`。

## musicso.cc 接口调研结论

已实地验证（2026-08-22），实现时按此结构写，不必重新摸索。
**所有请求都必须带浏览器 UA**（复用 `internal/music/mp3pm.go` 里已有的包级
`browserUA` 常量），否则一律吃 Cloudflare 质询。

```
① GET https://www.musicso.cc/s/<PathEscape(关键词)>
   → 200 text/html，同时：
       响应头 Set-Cookie: PHPSESSID=<会话>     ← 第 ② 步要用
       响应体 每首歌一个 <div class="song-item">：
           <div class="song-name">晴天</div>
           <div class="song-artist">
               <a class="song-artist-link" …>周杰伦</a> ·
               <a class="song-album-link"  …>叶惠美</a>
           </div>
           <span class="song-source src-qq">QQ源</span>       ← src-qq / src-netease
           <a class="song-play" href="/music/0039MnYb0qxYhV-q-晴天-周杰伦.html">

   href 形如 /music/<id>-<t>-<歌名>-<歌手>.html，<t> 是 q（QQ）或 n（网易）。
   三次实测（周杰伦 / hello adele / 开不了口）均返回 15 条，未见分页需求。
   **不提供时长。**

② GET https://www.musicso.cc/api/play.php?id=<id>&type=<t>
   Cookie: PHPSESSID=<第 ① 步下发的那个>          ← 缺了它返回纯文本 "forbidden"
   → {"code":1,"msg":"成功","url":"https://aqqmusic.tc.qq.com/….mp3?vkey=…",
      "lrc":"…","pic":"…"}
   Referer / X-Requested-With 都无效，只有会话 cookie 有效。

③ GET <上一步的 url>
   → 200 audio/mpeg，**无需 cookie**。
```

## 组件设计

### `internal/music/musicso.go`（新增）

一个类型 `MusicSo`，实现现有的 `music.Source` 接口，用 main 里那个共享
`*http.Client`（**不需要 cookiejar**，会话是手动捕获并跟着候选走的）。

```go
func NewMusicSo(httpClient *http.Client) *MusicSo
func (m *MusicSo) Name() string   // "musicso"
func (m *MusicSo) Hint() string
func (m *MusicSo) Search(ctx, query) ([]Candidate, error)
func (m *MusicSo) Download(ctx, c, w) (int64, error)
```

与 `Mp3PM` 一致，`baseURL` 抽成字段（而非直接用常量）以便测试指向 httptest。

**`Search`：一个 GET 干两件事**

1. `GET baseURL + "/s/" + url.PathEscape(query)`。
2. 从响应头取 `PHPSESSID`。
3. goquery 解析 `div.song-item`：
   - `.song-name` → `Title`
   - `.song-artist-link` 首个 → `Artist`（`First()`，防御同一条目里出现多个）
   - `.song-play` 的 href 用 `^/music/([^-]+)-([a-z])-` 提取 id 与后端标识。
     QQ 的 mid 是 14 位 base62、网易是纯数字，都不含 `-`；歌名歌手里的 `-`
     在正则右侧，不影响。**不匹配的条目直接跳过**，与 `parseMp3PMResults`
     里"没有 id 或没有直链就 return"同一套防御。
4. 产出 `Candidate{ID: "<t>-<id>", Artist, Title, Duration: "", dlCookie: 会话}`。
5. 零结果返回空切片 + `nil` error（"没搜到"是正常路径，不是错误）。
   但**页面有内容却一条都没解析出来时额外打一条 WARN**，让日志能区分
   "真没这首歌"与"站点改版"——沿用 `parseMp3PMResults` 的既有做法。

**`Download`：两跳**

1. 从 `c.ID` 上按第一个 `-` 切出 `<t>` 与 `<id>`。
2. `GET /api/play.php?id=&type=`，手动带 `Cookie: PHPSESSID=<c.dlCookie>`。
3. 解析 JSON；`code != 1` 或 `url` 为空 → 返回 error（模型据此改选候选，
   现有"下载失败回灌"机制已覆盖）。
4. `GET` 那个 CDN url（**不带任何 cookie**），`io.Copy` 进 `w`。
   `limitWriter` 的 50 MiB 上限照常生效。

**Cloudflare 质询识别（两跳都要做）**

响应状态为 403 且（`cf-mitigated` 响应头非空 **或** 响应体含 `Just a moment`）时：

- 返回一个措辞明确的 error（"musicso.cc 被 Cloudflare 拦截（bot 质询）"），
  **不**降级成零结果；
- 同时 `slog.ErrorContext` 记下 `cf-ray` 响应头，便于排障。

理由：质询若静默表现成"搜不到"，模型会换几个关键词全部无果，用户最终看到的
是一条完全指不到病根的失败信息。

### `internal/music/report.go`

`renderStatus` 的曲目行现在写死 `fmt.Sprintf("%s - %s · %s", Artist, Title, Duration)`。
musicso 的 `Duration` 恒为空，会渲染出 `周杰伦 - 晴天 · ` 这样挂着一个孤零零的点的行。
改成 **`Duration` 为空则整段省略**。`Bytes`、`Source` 两段本来就是这个写法，
这里只是把漏掉的一处补齐。

### `internal/music/agent.go`

- `defaultMaxSteps` **6 → 10**。一轮 = 一次模型调用；两个音源的最坏路径是
  「musicso 搜 → 换词 → mp3pm 搜 → 换词 → 下载 → 下载失败改选 → 再下载 →
  输出 JSON」共 8 轮，留两轮余量。现在的 6 在双源场景下会稳定撞上
  "超过最大步数，模型未收敛"。
- 提示词 `整个任务最多搜索 3 次` → **最多 4 次**（两个源各一次 + 两次换词）。
- 提示词新增一句：**按下面列出的顺序依次尝试音源，前一个零结果或没有匹配项
  再换下一个**。顺序来自 `cfg.Music.Sources`，Go 里不硬编码优先级。
- 规范化那段要放宽。现在写死"音源里的罗马化写法要还原成中文原名"，
  而 musicso 给的本来就是中文，这条会诱导模型去"还原"一个已是原名的字符串。
  改成：**若音源给的是罗马化写法则还原成中文原名，已经是中文的原样保留。**

`maxCandidates`（20）不动：musicso 固定 15 条，撞不上。

### `internal/music/source.go`

`Candidate` 新增一个包内私有字段：

```go
dlCookie string // 下载所需的会话凭据；只有产出它的音源看得懂。mp3.pm 不用，恒空
```

与已有的私有 `dlURL` 同一套思路：不出包、不进模型上下文、只有属主音源解释它。

### `internal/music/upload.go`

**零改动。** 当初那句"内部按 `[]Target` 循环、不硬编码两个，加第三个网盘时
这个文件一行都不用改"的注释，这次反向兑现了。唯一微妙处是单目标时错误文案
读作"全部 1 个 WebDAV 目标上传失败"，略拗口但不算错，保留。

### `internal/config/config.go`

**新增顶层 `[network]` 段：**

```go
type Network struct {
    Proxy string `toml:"proxy"` // 空 = 全部直连
}
```

非空时用 `url.Parse` 当场校验，非法即启动失败——别等到运行时才炸。

**`Music` 新增字段：**

```go
Sources []string `toml:"sources"` // 顺序即优先级；不在列表里 = 关闭
```

合法值：`musicso`、`mp3pm`。

**`Music.validate()` 重写。** 先定义"一个目标是否完整"：该目标的 `name` 与 `url`
**两项都非空白**即完整，两项都空白即"未配置该目标"，只有一项非空白即半个目标。
规则表：

| 场景 | 结果 |
|---|---|
| 整段没碰（六个 webdav 字段与 `sources` 全空） | ok，`/music` 关闭，正常启动 |
| `webdav_user` 或 `webdav_pass` 缺失 | error |
| 目标 1、目标 2 都不完整 | error |
| `webdav_name_2` 有值但 `webdav_url_2` 空（半个目标） | error |
| 只有目标 1 完整 | ok，1 个上传目标 |
| `sources` 缺省 | ok，默认 `["musicso", "mp3pm"]` 全启用 |
| `sources = []` | error（显式写空数组一定是关错了地方） |
| `sources` 含未知名 | error，并列出合法值 |
| `sources` 有重复项 | error |

"这一段是否在用"的 `touched` 判定要**扩到 `sources` 非空**。否则只写了
`sources` 而 webdav 全忘的配置会被判成"整段没配"而静默放行——正是原注释里
花了一大段力气要避免的那种"看似正常启动、实则 `/music` 悄悄不可用"的状态。

`Configured()` 语义随之从"六项全 usable"改成
**"`user`/`pass` 有值 且 至少一个完整目标"**。

### `cmd/news2tg/main.go`

三处：

**① 代理落地（两行，不动任何构造函数签名）**

全仓库六个 HTTP 客户端构造点里，五个（`notify.Telegram` 的
`&http.Client{Timeout: 30s}`、`tgbotapi.NewBotAPI` 内部的 `&http.Client{}`、
go-openai `DefaultConfig` 的 `&http.Client{}` × 3）**`Transport` 都是 nil**，
`net/http` 会回落到 `http.DefaultTransport`。因此：

```go
// 全局兜底：覆盖那五个自建 client
http.DefaultTransport.(*http.Transport).Proxy = http.ProxyURL(u)
// 共享 client 自带显式 Transport，不吃 DefaultTransport，单独设一次
Transport: &http.Transport{ Proxy: http.ProxyURL(u), MaxIdleConns: 50, /* … */ }
```

`ai` / `agent` / `music` / `notify` / `command` 五个包一个字不改。
`proxy` 为空时两处都不设，行为与现在完全一致。

**② 音源按配置顺序构造**

一张工厂表 `map[string]func() music.Source`，按 `cfg.Music.Sources` 的**顺序**
取用，产出的切片顺序即模型看到的优先级顺序。

**③ 上传目标只收完整的**

```go
targets := make([]music.Target, 0, 2)
// 目标 1、目标 2 各自完整才 append
```

`main.go:159` 那段关于 `command.MusicRunner` 必须声明成**接口类型**（否则
nil 具体指针塞进接口后 `b.music == nil` 为 false，`/music` 会 panic）的注释与
写法**原样保留**——那个坑与本次改动无关。

### 文档

- `config.toml` 模板补 `[network]` 段与 `[music] sources`（凭据仍留空占位）。
- `CLAUDE.md` 里 `[music]` 那段现在写的是"六个平铺字段"与"要么整段不配、
  要么六项全配"，两句都已不成立，要改；同时补上 musicso 音源与 `[network]` 段。

## 错误处理

延续仓库既有约定，本次不引入新哲学：

| 情形 | 行为 |
|---|---|
| musicso 零结果 | 空切片 + `nil` error，回灌给模型换词/换源 |
| musicso 解析出 0 条但页面有内容 | 同上，但额外 WARN（疑似站点改版） |
| Cloudflare 质询 | **明确 error + ERROR 日志（带 cf-ray）**，不降级成零结果 |
| `play.php` 返回 `code != 1` | error 回灌，模型改选候选 |
| CDN 下载失败 / 超 50 MiB | 沿用现有路径：删半成品、退回上一次成功的 track、回灌 |
| 上传部分失败 | 至少一个成功即算任务成功（不变） |
| 上传全部失败 | 任务失败（不变；单目标时文案读作"全部 1 个"） |
| `[network] proxy` 非法 URL | 启动失败 |
| `[music]` 半配置 | 启动失败（不变） |

## 测试

**`internal/music/musicso_test.go`（新增）** —— 沿用 `testdata/` 放 HTML fixture
的现有惯例（`mp3pm_test.go` 用的是 `testdata/mp3pm_search.html`，本次对应新增
`testdata/musicso_search.html`），全部 httptest，不打真实网络：

- 搜索页 fixture 解析出预期条数，歌名/歌手/id/后端标识都对
- href 不匹配 `^/music/([^-]+)-([a-z])-` 的条目被跳过
- 零结果 → 空切片 + `nil` error
- 页面非空却解析出 0 条 → 空切片 + WARN 分支
- httptest 验证 `play.php` **确实带上了**搜索那一跳下发的 Cookie
- `play.php` 返回 `code != 1` → error
- CDN 那一跳**不带** cookie
- 403 + `cf-mitigated: challenge` → 返回质询专属 error，而非零结果

**`internal/config/config_test.go`** —— 上面规则表逐行一个 case（九行），
外加 `[network] proxy` 合法/非法两例。

**`internal/music/report_test.go`** —— `Duration` 为空时不渲染那个尾巴。

**`internal/music/upload_test.go`** —— 补单目标场景（成功 / 失败各一）。

**不新增**：换源逻辑纯由模型决定，Go 侧没有对应分支；`agent_test.go` 现有的
换词重搜 / 下载失败改选 / 超 50MB / 非 JSON 输出 / id 不一致 / 未收敛六个分支
已经覆盖了循环本身。也不加联网 e2e——musicso 依赖能过 Cloudflare 的出口，
放进 CI 必挂。

## 附录：为什么放弃 jbsou.cn

调研过程中先摸清了 jbsou.cn（`POST /` + `X-Requested-With`，需先 GET 首页拿
PHPSESSID，六个后端 netease/qq/kugou/kuwo/migu/qianqian），随后发现 musicso.cc
在同一件事上全面更优，遂放弃：

| | jbsou.cn | musicso.cc |
|---|---|---|
| 搜索 | GET 首页取会话 + POST + XHR 头，两跳 | 一次 `GET /s/<关键词>`，会话随搜索页一起下发 |
| 后端 | 六个，需各查各的或让模型瞎猜 | QQ + 网易**已合并排序**，15 条一页 |
| 解析 | JSON，但 `songid` 在 netease 是 number、qq/kugou 是 string | 静态 HTML，goquery 直接吃 |
| 直链 | `api.php?…&sign=…&t=…`，签名会过期**且需带会话** | `play.php` 换一次，CDN 直链**不需要 cookie** |
| 会话二义性 | 会话失效与"真没这首歌"返回一模一样的 `code:404` | 会话与搜索同一请求，不存在该窗口 |

代价是失去酷狗覆盖（musicso 只有 QQ + 网易，三个查询实测均如此）。
若日后确需酷狗，本设计的 `sources` 数组已经预留了加源的位置，
`Source` 接口与 agent 循环都不必改。
