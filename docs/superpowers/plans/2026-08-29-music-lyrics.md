# /music 歌词下载 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `/music` 在下歌的同时下歌词，产出与 mp3 同名的 `.lrc` 一起传上 WebDAV；取词做成 `Source` 接口的一项能力，音源自己表态供不供词。

**Architecture:** `Source` 接口新增 `Lyric(ctx, Candidate) (string, error)`。`*MusicSo` 复用 `/api/play.php` 响应里现被丢弃的 `lrc` 字段实现它；`*Mp3PM` 返回 `errLyricUnsupported` 哨兵明确表态不供词。`Track` 私有地带上下载它的 `Source` 与 `Candidate`，`Runner` 在 agent 返回之后、上传之前调 `Lyrics.Save` 取词落盘，再把 mp3 与 `.lrc` 一起交给改造成多文件的 `Uploader`。取词的任何失败都不阻断上传。

**Tech Stack:** Go 1.25，标准库（`net/http`、`encoding/json`、`os`、`log/slog`）+ 既有依赖。测试用 `net/http/httptest` 与 fixture，不联网、不调 LLM。

## Global Constraints

- **设计依据：** `docs/superpowers/specs/2026-08-29-music-lyrics-design.md`。有出入以 spec 为准。
- **双语教学注释：** 本仓库几乎每段代码都带中文注释解释 Go 语义与设计取舍。新增代码必须匹配这个密度与风格，不要写成裸代码。这是 house style，不是噪音。
- **`cmd/news2tg/main.go` 一行都不改。** 歌词是 `Source` 的能力、`Lyrics` 由 `NewRunner` 内部装配，接线层看不到这次变化。任何需要改 main 的做法都是走偏了。
- **不新增配置项。** 不动 `internal/config`。
- **LLM 不参与取词。** 不加工具、不动 `defaultMaxSteps`（10）与 `agentTimeout`（300s）。
- **验收命令：** `go test -short ./...`。`-short` 不可省：省掉会真调 DeepSeek API 花钱（`internal/agent/agent_test.go` 的 e2e 用例）。
- **`gofmt -l .` 不是验收门。** 本仓库因 CRLF 天然列出约 26 个文件。只需保证**你改过的文件**经过 `gofmt -w` 处理。
- **错误信息与日志用中文**，与既有代码一致。日志在任务路径上用 `slog.*Context(ctx, …)` 变体（`task_id` 靠 ctx 流动），不要用无 ctx 的 `slog.Info`。
- **哨兵错误用 `errors.Is` 判别**，音源可以用 `%w` 包任意层数。既有先例：`errTooLarge`、`errSourceUnavailable`。
- **提交信息前缀**沿用仓库习惯：`#feat` / `#fix` / `#docs` / `#test` + 中文标题，正文说清"为什么"。结尾加 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。
- **实测事实（2026-08-29）：** `play.php` 的 `lrc` 是**内联的标准 LRC 正文**（含 `[ti:]/[ar:]/[al:]/[offset:]` 元信息行与 `[mm:ss.xx]` 时间戳，JSON 里换行是 `\n`，UTF-8）。不是 URL、不是 base64。响应键固定为 `code`/`msg`/`url`/`lrc`/`pic`，实测约 3 KB。

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/music/musicso.go` | 改 | 抽出 `play` 方法（`Download` 与 `Lyric` 共用）、加 `maxPlayRespBytes`、实现 `Lyric` |
| `internal/music/mp3pm.go` | 改 | 实现 `Lyric` 返回哨兵 |
| `internal/music/source.go` | 改 | `Source` 接口加 `Lyric` 方法及其约定注释 |
| `internal/music/lyric.go` | **新建** | `errLyricUnsupported` 哨兵 + `Lyrics.Save`（取词并落盘） |
| `internal/music/report.go` | 改 | `LyricState`/`LyricStatus`、`Status.Lyric`、`TargetStatus.Warn`、`Track.src/cand`、歌词行渲染 |
| `internal/music/agent.go` | 改 | `doDownload` 成功时把 `src`/`cand` 填进 `Track` |
| `internal/music/upload.go` | 改 | `UploadFile`、`Upload` 改收文件切片、`buildBase`/`BuildLyricFilename` |
| `internal/music/runner.go` | 改 | `lyricSaver` 接口、`lyricTimeout`、编排取词并组装上传文件列表 |
| `internal/music/testdata/musicso_play.json` | **新建** | 实测响应截短后的 fixture |
| 对应 `*_test.go` | 改/新建 | 见各任务 |
| `CLAUDE.md` | 改 | 同步歌词链路说明 |

---

### Task 1: 抽出 `MusicSo.play` 并给响应体加读取上限

纯重构 + 一处加固，**不引入任何歌词逻辑**。先做这一步是为了让「`Download` 行为一字不变」这件事能被单独评审，不跟新功能混在一起。

**Files:**
- Modify: `internal/music/musicso.go`（`musicSoPlayResp` 附近、`Download` 全体）
- Test: `internal/music/musicso_test.go`

**Interfaces:**
- Consumes: 无（本任务是起点）
- Produces: `func (m *MusicSo) play(ctx context.Context, c Candidate) (musicSoPlayResp, error)` —— 成功时保证 `pr.Code == 1`；候选 id 拆分、`PHPSESSID`、Cloudflare 质询识别、JSON 解析都在里面。常量 `maxPlayRespBytes = 1 << 20`。

- [ ] **Step 1: 写失败测试 —— 超大响应必须报错**

追加到 `internal/music/musicso_test.go` 末尾：

```go
// TestMusicSoPlayRespTooLarge 验证：play.php 响应体超过上限时报错，
// 而不是拿着被截断的半个 JSON 继续往下走。
//
// 判别力：去掉 io.LimitReader 之后，服务端吐的 2 MB 会被完整读进内存，
// 那是个合法 JSON（url 字段就是超长而已），Download 会照常往下跑 ——
// 也就是说这条用例挂不挂，正好对应"上限在不在"。
func TestMusicSoPlayRespTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 造一个语法完全合法、但体积远超上限的响应。
		huge := strings.Repeat("A", 2<<20)
		fmt.Fprintf(w, `{"code":1,"msg":"成功","url":"http://x/%s.mp3","lrc":"","pic":""}`, huge)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	_, err := m.Download(context.Background(), Candidate{ID: "q-x", dlCookie: "s"}, &buf)
	if err == nil {
		t.Fatal("响应体超过上限时应当报错")
	}
	if buf.Len() != 0 {
		t.Errorf("失败时不该往 writer 里写任何东西，实际写了 %d 字节", buf.Len())
	}
}
```

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run TestMusicSoPlayRespTooLarge -v`
Expected: FAIL —— 当前无上限，2 MB 响应被完整读入并成功解析，`Download` 转而去 GET 那个不存在的 `http://x/AAA….mp3`，报的是连接错误而非上限错误；也可能因为环境不同而通过，所以**必须确认失败原因**：把 `t.Fatal` 那行改成 `t.Fatalf("err=%v", err)` 临时观察，看到的应是拨号失败一类的错误而不是"超过上限"。观察完改回去。

- [ ] **Step 3: 加常量与 `play` 方法**

在 `musicSoPlayResp` 定义**之前**插入常量，并把结构体注释里"其余（lrc / pic）"那句留到 Task 2 再改：

```go
// maxPlayRespBytes 是 /api/play.php 响应体的读取上限。
//
// 这里原先是无上限的 io.ReadAll —— 整个响应被读进内存再 json.Unmarshal。
// 一个坏掉或恶意的站点可以让它变得任意大，而我们没有任何防线。
// 上限放在 play 这一个读取点上、不下放给调用方，理由同 agent.go 的
// defaultMaxBytes：否则每多一个调用方就要重复实现一遍同样的防御。
// 实测真实响应约 3 KB，1 MB 有 300 倍余量。
const maxPlayRespBytes = 1 << 20
```

在 `musicSoPlayResp` 定义之后、`Download` 之前插入：

```go
// play 走 /api/play.php 这一跳：用会话换回该候选的播放信息。
//
// 抽成独立方法是因为它有两个调用方（Download 取其中的 url，Lyric 取其中的
// lrc），而候选 id 拆 <backend>-<id>、PHPSESSID、Cloudflare 质询识别
// 这些站点知识只该存在一份。
//
// 成功返回时保证 pr.Code == 1；至于 url / lrc 具体有没有内容，
// 由各调用方按自己的需要判断 —— 对 Download 来说空 url 是失败，
// 对 Lyric 来说空 lrc 只是"站点没收录这首"。
func (m *MusicSo) play(ctx context.Context, c Candidate) (musicSoPlayResp, error) {
	// zero 是出错时返回的零值。Go 没有"返回 null"，用一个具名零值比
	// 每处都写 musicSoPlayResp{} 更清楚。
	var zero musicSoPlayResp

	// 候选 id 的形状是 <后端标识>-<站内 id>，按第一个 '-' 切一刀。
	// SplitN(s, sep, 2) 最多切成 2 段，所以站内 id 里万一有 '-' 也不会被切碎。
	parts := strings.SplitN(c.ID, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return zero, fmt.Errorf("musicso 候选 id %q 形状不对（应为 <q|n>-<id>）", c.ID)
	}
	backend, id := parts[0], parts[1]

	playURL := fmt.Sprintf("%s/api/play.php?id=%s&type=%s",
		strings.TrimRight(m.baseURL, "/"), url.QueryEscape(id), url.QueryEscape(backend))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playURL, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("User-Agent", browserUA)
	if c.dlCookie != "" {
		// 手动拼 Cookie 头而不是用 cookiejar：会话是跟着候选走的，
		// 这样两个并发的 /music 任务不会互相把对方的会话冲掉。
		req.Header.Set("Cookie", "PHPSESSID="+c.dlCookie)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("musicso 直链解析请求失败: %w", err)
	}
	// io.LimitReader 包一层，最多只放出 maxPlayRespBytes 个字节。
	// 超限时 JSON 必然被截断、下面的 Unmarshal 必然失败 —— 这正是我们要的：
	// 宁可报一个明确的解析错误，也不要拿着半个响应继续往下走。
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxPlayRespBytes))
	// 读完就关：调用方后面可能还要发别的请求，早点把连接还回池子。
	resp.Body.Close()
	if readErr != nil {
		return zero, fmt.Errorf("读取 musicso 直链解析响应失败: %w", readErr)
	}
	if cErr := musicSoChallenge(ctx, resp, string(body)); cErr != nil {
		return zero, cErr
	}

	var pr musicSoPlayResp
	if jErr := json.Unmarshal(body, &pr); jErr != nil {
		// 会话缺失时站点回的是纯文本 "forbidden"，正好落在这里；
		// 响应超限被截断也落在这里。把原文截断带进错误信息，
		// 比一句"JSON 解析失败"有用得多。
		return zero, fmt.Errorf("musicso 直链解析返回的不是 JSON（%v）：%s",
			jErr, tools.TruncateUTF8(strings.TrimSpace(string(body)), 100))
	}
	if pr.Code != 1 {
		return zero, fmt.Errorf("musicso 直链解析失败（code=%d msg=%q）", pr.Code, pr.Msg)
	}
	return pr, nil
}
```

- [ ] **Step 4: 把 `Download` 改成调 `play`**

用下面这一版整体替换现有的 `Download`（注释里"两跳"的说明保留，因为它仍然是两跳）：

```go
// Download 走两跳：先用会话换到 CDN 直链，再裸 GET 那个直链。
//
// 为什么不能一跳：搜索结果里给的是站点自己的详情页地址，真正的音频直链
// 要现问 /api/play.php 要，而且它认搜索那一跳下发的 PHPSESSID（第一跳的
// 细节都在 play 里）。好在换来的 CDN 直链本身**不需要任何 cookie**（实测），
// 所以第二跳是裸 GET —— 也别给它带上 cookie，那等于把会话泄漏给第三方 CDN。
func (m *MusicSo) Download(ctx context.Context, c Candidate, w io.Writer) (int64, error) {
	slog.InfoContext(ctx, "开始从 musicso 下载", "id", c.ID, "title", c.Title)

	// ① 用会话换直链。
	pr, err := m.play(ctx, c)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(pr.URL) == "" {
		// code 已经在 play 里校验过是 1 了，走到这里说明站点说成功却没给地址。
		return 0, fmt.Errorf("musicso 未能给出直链（code=%d msg=%q）", pr.Code, pr.Msg)
	}

	// ② 裸 GET 直链。注意**不带** cookie。
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pr.URL, nil)
	if err != nil {
		return 0, err
	}
	dlReq.Header.Set("User-Agent", browserUA)

	dlResp, err := m.httpClient.Do(dlReq)
	if err != nil {
		return 0, fmt.Errorf("musicso 下载请求失败: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		return 0, fmt.Errorf("musicso 下载返回非 2xx 状态码 %d", dlResp.StatusCode)
	}

	// io.Copy 从 Body 流式拷到 w，不会把整个文件读进内存。
	// w 可能是带大小上限的 limitWriter，超限时它会返回错误让 Copy 提前中断；
	// 这里用 %w 包装以保留 errors.Is 的可判别性（agent 要靠它区分"过大"和别的失败）。
	n, err := io.Copy(w, dlResp.Body)
	if err != nil {
		return n, fmt.Errorf("写入 mp3 数据失败: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 5: 跑全套 musicso 测试**

Run: `go test ./internal/music -run 'TestMusicSo|TestParseMusicSo' -v`
Expected: 全部 PASS。既有 8 个用例（含 `TestMusicSoDownloadBadCode`、`TestMusicSoDownloadWithoutSession`、`TestMusicSoDownloadBadID`、两个 Cloudflare 用例）是本次重构的回归门，**一条都不许放宽**。新增的 `TestMusicSoPlayRespTooLarge` 也应 PASS。

- [ ] **Step 6: 格式化并提交**

```bash
gofmt -w internal/music/musicso.go internal/music/musicso_test.go
go vet ./internal/music
git add internal/music/musicso.go internal/music/musicso_test.go
git commit -F - <<'EOF'
#fix 抽出 MusicSo.play 并给 play.php 响应体加读取上限

Download 的第一跳（候选 id 拆分、PHPSESSID、Cloudflare 质询识别、JSON
解析）抽成独立的 play 方法，为即将到来的 Lyric 做准备——两个调用方共用
同一份站点知识，不重复。

顺带收掉这里一个既有的无界读：响应原先是无上限 io.ReadAll 进内存再解析，
现在包一层 io.LimitReader(1 MB)。实测真实响应约 3 KB，余量 300 倍。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 2: `Source` 接口新增 `Lyric`，两个音源分别实现

**Files:**
- Create: `internal/music/lyric.go`（本任务只放哨兵，`Lyrics` 留到 Task 5）
- Create: `internal/music/testdata/musicso_play.json`
- Modify: `internal/music/source.go`、`internal/music/musicso.go`、`internal/music/mp3pm.go`
- Modify（编译必需）: `internal/music/agent_test.go`（`fakeSource` 要补 `Lyric`）
- Test: `internal/music/musicso_test.go`、`internal/music/mp3pm_test.go`

**Interfaces:**
- Consumes: Task 1 的 `func (m *MusicSo) play(ctx context.Context, c Candidate) (musicSoPlayResp, error)`
- Produces:
  - `var errLyricUnsupported = errors.New("该音源不提供歌词")`
  - `Source` 接口新方法 `Lyric(ctx context.Context, c Candidate) (string, error)`
  - `func (m *MusicSo) Lyric(ctx context.Context, c Candidate) (string, error)`
  - `func (m *Mp3PM) Lyric(_ context.Context, _ Candidate) (string, error)`

- [ ] **Step 1: 建 fixture**

Create `internal/music/testdata/musicso_play.json`（实测响应截短，保留头尾结构；`\n` 保持 JSON 转义形式）：

```json
{"code":1,"msg":"成功","url":"https://isure6.stream.qqmusic.qq.com/M800003Qui1q2u1Zho.mp3?guid=cyapi&vkey=D9509A57","lrc":"[ti:晴天]\n[ar:周杰伦]\n[al:叶惠美]\n[by:]\n[offset:0]\n[00:00.00]晴天 - 周杰伦 (Jay Chou)\n[00:02.25]词：周杰伦\n[00:04.50]曲：周杰伦\n[04:22.86]但故事的最后你好像还是说了拜","pic":"https://y.gtimg.cn/music/photo_new/T002R800x800M000000MkMni19ClKG.jpg"}
```

- [ ] **Step 2: 写失败测试**

追加到 `internal/music/musicso_test.go` 末尾：

```go
// newMusicSoLyricServer 起一个只回 play.php 的假站点，响应体由调用方给定。
// 与 newMusicSoServer 分开是因为那个的 play.php 响应写死了空 lrc，
// 而这里每个用例要的 lrc 都不一样。
func newMusicSoLyricServer(t *testing.T, playBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, playBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMusicSoLyric 验证：lrc 是内联 LRC 正文时原样返回。
// fixture 取自 2026-08-29 对线上 play.php 的实测响应（截短）。
func TestMusicSoLyric(t *testing.T) {
	srv := newMusicSoLyricServer(t, loadFixture(t, "musicso_play.json"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Lyric(context.Background(), Candidate{ID: "q-0039MnYb0qxYhV", dlCookie: "s"})
	if err != nil {
		t.Fatalf("Lyric 意外报错: %v", err)
	}
	// 元信息行必须原样保留：播放器靠 [ti:]/[ar:] 显示曲目信息。
	if !strings.HasPrefix(got, "[ti:晴天]") {
		t.Errorf("歌词开头 = %q, want 以 [ti:晴天] 开头", tools.TruncateUTF8(got, 30))
	}
	// JSON 里的 \n 必须已经被 encoding/json 解码成真正的换行 ——
	// 否则写出去的 .lrc 会是一整行，播放器完全认不出来。
	if !strings.Contains(got, "\n[00:00.00]晴天 - 周杰伦 (Jay Chou)") {
		t.Errorf("歌词里没有解码后的换行 + 时间戳行，实际:\n%q", got)
	}
	if strings.Contains(got, `\n`) {
		t.Error("歌词里出现了未解码的字面 \\n，说明当成了转义文本处理")
	}
}

// TestMusicSoLyricEmpty 验证：lrc 为空串 = 站点没收录这首的词，
// 这是**正常路径**，必须返回 ("", nil) 而不是错误。
//
// 判别力：如果实现把空 lrc 当错误，Lyrics.Save 会把它翻成 LyricFailed，
// 用户看到的是"歌词获取失败"——一条把人往"是不是坏了"上带的假线索。
func TestMusicSoLyricEmpty(t *testing.T) {
	srv := newMusicSoLyricServer(t, `{"code":1,"msg":"成功","url":"http://x/a.mp3","lrc":"","pic":""}`)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Lyric(context.Background(), Candidate{ID: "q-x", dlCookie: "s"})
	if err != nil {
		t.Fatalf("空歌词不该报错: %v", err)
	}
	if got != "" {
		t.Errorf("歌词 = %q, want 空串", got)
	}
}

// TestMusicSoLyricBlank 验证：lrc 只有空白字符时同样按"未收录"处理。
// 否则会写出一个内容全是空白的 .lrc 传上网盘，播放器显示一片空白，
// 比没有歌词更糟。
func TestMusicSoLyricBlank(t *testing.T) {
	srv := newMusicSoLyricServer(t, `{"code":1,"msg":"成功","url":"http://x/a.mp3","lrc":"  \n\t ","pic":""}`)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Lyric(context.Background(), Candidate{ID: "q-x", dlCookie: "s"})
	if err != nil {
		t.Fatalf("空白歌词不该报错: %v", err)
	}
	if got != "" {
		t.Errorf("歌词 = %q, want 空串（只有空白等同于未收录）", got)
	}
}

// TestMusicSoLyricCloudflare 验证：撞上 Cloudflare 质询时返回**明确错误**，
// 不能伪装成"这首没有歌词"。理由同 TestMusicSoCloudflareChallenge。
func TestMusicSoLyricCloudflare(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.Header().Set("cf-ray", "a2f236c9695cae43-LAX")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<html><head><title>Just a moment...</title></head></html>`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Lyric(context.Background(), Candidate{ID: "q-x", dlCookie: "s"})
	if err == nil {
		t.Fatal("撞上 Cloudflare 质询时必须报错，不能静默当成没有歌词")
	}
	if got != "" {
		t.Errorf("出错时歌词应为空，实际 %q", got)
	}
	if !strings.Contains(err.Error(), "Cloudflare") {
		t.Errorf("错误信息应当点名 Cloudflare，实际: %v", err)
	}
}
```

在 `internal/music/musicso_test.go` 的 import 块里补上 `"github.com/cheedonghu/news2tg/internal/tools"`（上面用到了 `tools.TruncateUTF8`）。

追加到 `internal/music/mp3pm_test.go` 末尾：

```go
// TestMp3PMLyricUnsupported 验证：mp3.pm 明确表态不供词。
//
// 为什么要返回哨兵而不是 ("", nil)：这两件事在界面上是不同的两句话
// （"mp3pm 不提供" vs "站点未收录"）。合并的代价是日后某个源的歌词
// 悄悄坏掉时，界面上跟"这首真没词"长得一模一样，没人会发现。
func TestMp3PMLyricUnsupported(t *testing.T) {
	m := NewMp3PM(http.DefaultClient)

	got, err := m.Lyric(context.Background(), Candidate{ID: "1"})
	if !errors.Is(err, errLyricUnsupported) {
		t.Fatalf("err = %v, want errLyricUnsupported", err)
	}
	if got != "" {
		t.Errorf("歌词 = %q, want 空串", got)
	}
}
```

在 `internal/music/mp3pm_test.go` 的 import 块里补上 `"errors"` 与 `"net/http"`（若尚未引入）。

- [ ] **Step 3: 运行测试确认它失败**

Run: `go test ./internal/music -run 'Lyric' -v`
Expected: **编译失败**，报 `m.Lyric undefined` / `errLyricUnsupported undefined`。这就是本步要看到的失败。

- [ ] **Step 4: 建 `lyric.go` 放哨兵**

Create `internal/music/lyric.go`：

```go
package music

import "errors"

// errLyricUnsupported 是"该音源不提供歌词"的哨兵错误，供 errors.Is 判别。
//
// 为什么"不供词"要用一个专门的错误、而不是跟"站点没收录这首"一样返回
// ("", nil)：这两件事在进度消息里是不同的两句话 ——「mp3pm 不提供」
// 与「站点未收录」。合并成一句含糊的"无歌词"，代价是日后 musicso 改版
// 导致歌词永远解析不出来时，界面上跟"这首歌真的没词"长得一模一样，
// 没有人会发现有东西坏了。
//
// 风格与 agent.go 的 errTooLarge / errSourceUnavailable 一致：
// 音源可以用 %w 包任意层数，调用方一律用 errors.Is 判别。
var errLyricUnsupported = errors.New("该音源不提供歌词")
```

- [ ] **Step 5: 给 `Source` 接口加方法**

在 `internal/music/source.go` 的 `Source` 接口里，`Download` 之后追加：

```go
	// Lyric 取回该候选的歌词文本。
	//
	// 两种"没有歌词"必须分开表达，它们在进度消息里是不同的两句话：
	//   - 本音源压根不供词（如 mp3.pm）→ 返回 errLyricUnsupported 哨兵
	//   - 供词，但站点没收录这首 → 返回 ("", nil)，正常路径不是错误
	//
	// 歌词长在 Source 上而不是另开一个可选接口，是刻意的：可选接口那种
	// "实现了就自动生效、没实现就静默没有"很容易在加音源时漏掉，而漏掉的
	// 表现是"这个源下的歌永远没词"这种没人会去查的静默缺失。写进接口以后，
	// 编译器会替我们向每个新音源作者要这个答案。
	//
	// 代价是接口宽了一格，且**只供词、不供歌**的站（比如专门的 LRC API）
	// 没法直接接进来 —— 那种站不是 Source，没有 Search/Download 可实现。
	// 这是已知取舍，真需要那天再引入独立的歌词接口。
	Lyric(ctx context.Context, c Candidate) (string, error)
```

同时把包顶部注释的"包内分工"清单里补一行（放在 `report.go` 那行之前）：

```
//	lyric.go    取歌词并落盘（Source.Lyric 的编排层）
```

- [ ] **Step 6: 实现 `MusicSo.Lyric`**

先把 `musicSoPlayResp` 的字段与注释改成：

```go
// musicSoPlayResp 是 /api/play.php 的响应形状。
// 实测响应键固定为 code / msg / url / lrc / pic 五个；pic 用不上，
// 让 encoding/json 自动丢弃。
type musicSoPlayResp struct {
	Code int    `json:"code"` // 1 = 成功
	Msg  string `json:"msg"`
	URL  string `json:"url"` // CDN 直链
	LRC  string `json:"lrc"` // 内联的 LRC 歌词正文，可能为空
}
```

在 `Download` 之后追加，并在文件顶部的 `var _ Source = (*MusicSo)(nil)` 旁边不需要额外断言（`Source` 已含 `Lyric`，编译器自会检查）：

```go
// Lyric 取回该候选的歌词。
//
// 复用 Download 的第一跳：/api/play.php 的响应里本来就带着 lrc 字段，
// 一次请求同时给出直链和歌词。所以"从 musicso 下的歌自带歌词"这条快路径
// 不需要任何额外的搜索或匹配 —— 手上就有 id 和会话。
//
// lrc 的形状已于 2026-08-29 实测确认：**内联的标准 LRC 正文**，含
// [ti:]/[ar:]/[al:]/[offset:] 元信息行与 [mm:ss.xx] 时间戳，JSON 里
// 换行是 \n（encoding/json 解码后即真实换行），UTF-8。不是 URL、
// 不是 base64，拿到就能直接写进 .lrc，不需要二次请求或解码。
// QQ（type=q）与网易云（type=n）两个后端形状一致。
func (m *MusicSo) Lyric(ctx context.Context, c Candidate) (string, error) {
	pr, err := m.play(ctx, c)
	if err != nil {
		return "", err
	}

	// TrimSpace 后为空 = 站点没收录这首的词。这是**正常路径**，返回 nil error，
	// 由 Lyrics.Save 翻成 LyricMissing。
	// 用 TrimSpace 判断而不是 == ""，是为了顺带挡住"只有空白字符"那种情况：
	// 那会写出一个内容全是空白的 .lrc，传上网盘只会让播放器显示一片空白，
	// 比没有歌词更糟。
	if strings.TrimSpace(pr.LRC) == "" {
		slog.InfoContext(ctx, "musicso 未收录该曲歌词", "id", c.ID, "title", c.Title)
		return "", nil
	}

	// 返回未经 TrimSpace 的原文：歌词正文的首尾空白也是内容的一部分，
	// 上面那次 TrimSpace 只用来判空，不改动要落盘的东西。
	return pr.LRC, nil
}
```

- [ ] **Step 7: 实现 `Mp3PM.Lyric`**

在 `internal/music/mp3pm.go` 的 `Download` 之后追加：

```go
// Lyric 明确表态：mp3.pm 不供歌词。
//
// 实测该站的结果页和详情页里都没有歌词可抓；而"拿歌名跨站去别处配词"
// 不在本次范围内（见 source.go 对 Lyric 的说明）。
//
// 返回哨兵而不是 ("", nil)，是为了让进度消息能说出"mp3pm 不提供"，
// 而不是含糊的"没有歌词"——后者会跟"这首歌真的没词"混为一谈。
func (m *Mp3PM) Lyric(_ context.Context, _ Candidate) (string, error) {
	return "", errLyricUnsupported
}
```

- [ ] **Step 8: 给测试用的 `fakeSource` 补上 `Lyric`**

`agent_test.go` 里的 `fakeSource` 实现了 `Source`，接口变宽后它编译不过。在 `fakeSource` 的字段里加两个，并在 `Download` 方法之后追加方法：

```go
	lyric    string // Lyric 返回的歌词；空串表示"站点未收录"
	lyricErr error  // 非 nil 时 Lyric 返回它
```

```go
// Lyric 默认返回 ("", nil)，即"该源供词但这首没收录" —— 现有那十几个
// 用例都不关心歌词，这个默认值让它们一个字都不用改。
func (s *fakeSource) Lyric(_ context.Context, _ Candidate) (string, error) {
	return s.lyric, s.lyricErr
}
```

同文件里的 `failSecondSource` 是 `struct{ fakeSource }`（内嵌），会自动继承这个方法，**不用改**。

- [ ] **Step 9: 运行测试确认通过**

Run: `go test ./internal/music -run 'Lyric|TestMusicSo|TestMp3PM|TestAgent' -v`
Expected: 全部 PASS。

- [ ] **Step 10: 跑整包回归并提交**

Run: `go test ./internal/music`
Expected: PASS

```bash
gofmt -w internal/music/lyric.go internal/music/source.go internal/music/musicso.go internal/music/mp3pm.go internal/music/musicso_test.go internal/music/mp3pm_test.go internal/music/agent_test.go
go vet ./internal/music
git add internal/music/ 
git commit -F - <<'EOF'
#feat Source 接口新增 Lyric，musicso 供词、mp3pm 明确不供

歌词做成 Source 契约的一项能力而不是可选断言接口：后者"实现了就自动生效、
没实现就静默没有"，加音源时漏掉的表现是"这个源的歌永远没词"，没人会去查。
写进接口以后编译器会替我们向每个新音源作者要这个答案。

musicso 复用 play.php 那一跳里现被丢弃的 lrc 字段（实测是内联的标准 LRC
正文，含 [ti:] 元信息与时间戳，不是 URL 也不是 base64）；mp3pm 返回
errLyricUnsupported 哨兵，好让进度消息说得出"mp3pm 不提供"而不是含糊的
"没有歌词"——后者会跟"这首歌真的没词"混为一谈。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 3: 进度模型加歌词状态与渲染

**Files:**
- Modify: `internal/music/report.go`（`TargetStatus`、`Track`、`Status`、`renderStatus`）
- Test: `internal/music/report_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type LyricState int` 与常量 `LyricUnsupported` / `LyricMissing` / `LyricOK` / `LyricFailed`
  - `type LyricStatus struct { State LyricState; Err string }`
  - `Status.Lyric *LyricStatus`
  - `TargetStatus.Warn string`
  - `Track` 私有字段 `src Source`、`cand Candidate`
  - `func renderLyricLine(l LyricStatus, t *Track) string`

- [ ] **Step 1: 写失败测试**

追加到 `internal/music/report_test.go` 末尾：

```go
// TestRenderStatusLyricLines 锁死歌词那一行的四种措辞。
//
// 四种结局在界面上必须是四句不同的话：合并之后，日后音源改版导致歌词
// 永远取不到时，跟"这首歌真的没词"长得一模一样，没人会发现有东西坏了。
func TestRenderStatusLyricLines(t *testing.T) {
	base := Status{
		Query: "晴天",
		Stage: StageUploading,
		Track: &Track{Artist: "周杰伦", Title: "晴天", Bytes: 100, Source: "mp3pm"},
	}

	cases := []struct {
		name string
		l    LyricStatus
		want string
	}{
		{"成功", LyricStatus{State: LyricOK}, "✅ 歌词"},
		{"音源不供词", LyricStatus{State: LyricUnsupported}, "mp3pm 不提供"},
		{"站点未收录", LyricStatus{State: LyricMissing}, "站点未收录"},
		{"取词失败", LyricStatus{State: LyricFailed, Err: "被 Cloudflare 拦截"}, "歌词获取失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := base
			l := c.l
			s.Lyric = &l
			got := renderStatus(s)
			if !strings.Contains(got, c.want) {
				t.Errorf("渲染结果里没有 %q，实际:\n%s", c.want, got)
			}
		})
	}
}

// TestRenderStatusNoLyricLineWhenNil 验证：还没跑到取词那一步时（Lyric 为 nil），
// 歌词那一行完全不出现 —— 不能显示一个"无歌词"去误导用户。
func TestRenderStatusNoLyricLineWhenNil(t *testing.T) {
	s := Status{
		Query: "晴天",
		Stage: StageDownloading,
		Track: &Track{Artist: "周杰伦", Title: "晴天", Source: "musicso"},
	}
	if got := renderStatus(s); strings.Contains(got, "歌词") {
		t.Errorf("Lyric 为 nil 时不该出现歌词行，实际:\n%s", got)
	}
}

// TestRenderStatusLyricFailedEscapes 验证：失败原因里的 MarkdownV2 特殊字符
// 必须被转义。整条消息是预渲染的 MarkdownV2，Editor 不会再整体转义 ——
// 漏转义会让 Telegram 直接拒收整条消息，进度就此卡死。
func TestRenderStatusLyricFailedEscapes(t *testing.T) {
	l := LyricStatus{State: LyricFailed, Err: "cf-ray=a2f2_36c9 (challenge)"}
	s := Status{
		Query: "x",
		Stage: StageUploading,
		Track: &Track{Artist: "A", Title: "T", Source: "musicso"},
		Lyric: &l,
	}
	got := renderStatus(s)
	// '-'、'_'、'('、')'、'=' 都是 MarkdownV2 的保留字符，必须带反斜杠。
	if !strings.Contains(got, `cf\-ray`) {
		t.Errorf("失败原因未被转义，实际:\n%s", got)
	}
}

// TestRenderStatusTargetWarn 验证：可选文件（歌词）没传上去时，
// 目标行仍显示成功，但带一句警告 —— 不能因为附赠品失败就把目标标成失败。
func TestRenderStatusTargetWarn(t *testing.T) {
	l := LyricStatus{State: LyricOK}
	s := Status{
		Query: "x",
		Stage: StageDone,
		Track: &Track{Artist: "A", Title: "T", Source: "musicso"},
		Lyric: &l,
		Targets: []TargetStatus{
			{Name: "阿里云盘", State: TargetOK, Warn: "A - T.lrc: WebDAV 返回 507"},
		},
	}
	got := renderStatus(s)
	if !strings.Contains(got, "歌词未传上") {
		t.Errorf("目标行应带歌词未传上的警告，实际:\n%s", got)
	}
	if !strings.Contains(got, "507") {
		t.Errorf("警告里应保留原因，实际:\n%s", got)
	}
}
```

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run 'TestRenderStatusLyric|TestRenderStatusNoLyric|TestRenderStatusTargetWarn' -v`
Expected: **编译失败**，报 `LyricStatus undefined` 等。

- [ ] **Step 3: 加类型与字段**

在 `internal/music/report.go` 的 `TargetStatus` 定义**之前**插入：

```go
// LyricState 是取歌词这一步的结局。
type LyricState int

const (
	LyricUnsupported LyricState = iota // 该音源不提供歌词（如 mp3pm）
	LyricMissing                       // 音源供词，但站点没收录这首
	LyricOK                            // 拿到并落盘了
	LyricFailed                        // 请求/解析/落盘出错
)

// LyricStatus 是取歌词这一步的快照。
//
// 为什么是四态而不是一个 bool：见 lyric.go 里 errLyricUnsupported 的注释 ——
// "音源不供词"和"站点没这首的词"在界面上必须是两句不同的话。
type LyricStatus struct {
	State LyricState
	Err   string // 仅 LyricFailed 时非空
}
```

修改 `TargetStatus`：

```go
// TargetStatus 是单个上传目标的快照。
type TargetStatus struct {
	Name  string      // 展示名，如 "阿里云盘"
	State TargetState //
	Err   string      // 失败原因，成功时为空
	// Warn 是**可选文件**（目前只有歌词）没传上去时的说明。
	// 它跟 Err 是两回事：Err 非空意味着这个目标失败了，Warn 非空时目标
	// 仍然是成功的 —— 不能因为一个附赠的 .lrc 没传上去，就把已经传好的
	// 歌判成失败（延续"不为一个网盘挂掉丢弃已下好的歌"那条既有约定）。
	Warn string
}
```

在 `Track` 结构体末尾（`Tokens` 之后）追加：

```go
	// src / cand 是包内私有的取词凭据，由 agent 在下载成功那一刻填好
	// （见 agent.go 的 doDownload），此后再没人改过。
	//
	// 为什么把音源本身带在 Track 上，而不是让 Runner 另持一张
	// map[string]Source 注册表按 Source 名字去查：那样会多出一个
	// "查不到"的分支 —— 一个本不该发生、却必须写代码应付的状态。
	// 带着走，这个状态从根上就不存在。
	//
	// Candidate 里的 dlURL / dlCookie 本来就是包内可见、绝不出包、
	// 绝不进模型上下文的，挂在这里不改变那条约束。
	src  Source
	cand Candidate
```

在 `Status` 结构体里，`Targets` 之后追加：

```go
	// Lyric 是取歌词那一步的结果。nil = 还没跑到这一步（用指针的理由同 Track：
	// nil 在这里是有语义的，换成值类型就得再引入一个布尔量去人肉维持一致）。
	Lyric *LyricStatus
```

- [ ] **Step 4: 加渲染**

在 `renderStatus` 里，曲目行那个 `if s.Track != nil { … }` 块**之后**、上传区之前插入：

```go
	// 歌词行：跑到取词这一步才有。nil 表示还没轮到它，整行不出现 ——
	// 提前显示一个"无歌词"会误导用户以为已经查过了。
	if s.Lyric != nil {
		b.WriteString(renderLyricLine(*s.Lyric, s.Track) + "\n")
	}
```

在上传区的目标行里，`if t.Err != ""` 之后追加：

```go
			// Warn 与 Err 互不排斥：目标成功但歌词没传上去时只有 Warn。
			if t.Warn != "" {
				line += esc(" ⚠️ 歌词未传上: " + tools.TruncateUTF8(t.Warn, 80))
			}
```

在 `renderStatus` 之后追加：

```go
// renderLyricLine 渲染歌词那一行。
//
// 抽成独立函数是因为四个分支各有各的措辞，塞进 renderStatus 会把那个
// 已经不短的函数再撑大一截、盖住主线。
//
// 转义约定同 renderStatus：动态文本必须自己 EscapeMarkdownV2，
// 只有我们主动写的标记才留着不转义。这里的 emoji 和固定汉字不需要转义，
// 但源名和错误原因是动态的，必须过一遍。
func renderLyricLine(l LyricStatus, t *Track) string {
	esc := tools.EscapeMarkdownV2

	switch l.State {
	case LyricOK:
		return "✅ 歌词"
	case LyricUnsupported:
		// 源名取自 Track。理论上走到这一步 Track 一定非空（取词发生在下载之后），
		// 兜个底只是不想让一个渲染函数有 panic 的可能。
		src := "该音源"
		if t != nil && t.Source != "" {
			src = t.Source
		}
		return "➖ " + esc("歌词："+src+" 不提供")
	case LyricMissing:
		return "➖ " + esc("歌词：站点未收录")
	default: // LyricFailed
		return "⚠️ " + esc("歌词获取失败: "+tools.TruncateUTF8(l.Err, 80))
	}
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/music -run 'TestRenderStatus' -v`
Expected: 全部 PASS，含既有的 `TestRenderStatus`、`TestRenderStatusFailed`、`TestRenderStatusEmptyDuration` 等（它们的 `Status` 里 `Lyric` 是 nil，歌词行不出现，不受影响）。

- [ ] **Step 6: 整包回归并提交**

Run: `go test ./internal/music`
Expected: PASS

```bash
gofmt -w internal/music/report.go internal/music/report_test.go
go vet ./internal/music
git add internal/music/report.go internal/music/report_test.go
git commit -F - <<'EOF'
#feat 进度模型加歌词四态与目标行警告

LyricState 四态（不供词/未收录/成功/失败）而不是一个 bool：合并之后日后
音源改版导致歌词永远取不到时，界面上跟"这首歌真的没词"一模一样，没人会
发现坏了。Status.Lyric 用指针，nil 表示还没跑到取词那一步。

TargetStatus 加 Warn：可选文件（歌词）没传上去时目标仍算成功，只带一句
警告——不为一个附赠的 .lrc 把已经传好的歌判成失败。

Track 加两个私有字段 src/cand 承载取词凭据，消掉 Runner 侧注册表以及
"按名字查不到源"那个本不该存在的分支。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 4: agent 把音源与候选填进 `Track`

**Files:**
- Modify: `internal/music/agent.go`（`doDownload` 成功分支）
- Test: `internal/music/agent_test.go`

**Interfaces:**
- Consumes: Task 3 的 `Track.src` / `Track.cand`
- Produces: `Agent.Fetch` 返回的 `*Track` 保证 `src` 非 nil、`cand.ID` 等于实际下载的候选 id

- [ ] **Step 1: 写失败测试**

追加到 `internal/music/agent_test.go` 末尾：

```go
// TestAgentTrackCarriesSourceAndCandidate 验证：下载成功后产出的 Track
// 带着"下这首歌的那个音源"和"那条候选"，且经 finalize 的值拷贝后仍在。
//
// 这两个字段是 Runner 取歌词的唯一凭据。finalize 里造的是**新 Track**
// （nt := *r.track），所以必须确认值拷贝把它们带过去了 —— 漏了的话
// 表现是"所有歌都没有歌词"，而且不会有任何错误信息。
func TestAgentTrackCarriesSourceAndCandidate(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "ID3fake-bytes",
		results: map[string][]Candidate{
			"晴天": {{ID: "42", Artist: "周杰伦", Title: "晴天", dlURL: "u", dlCookie: "sess"}},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"晴天"}`),
		toolCallResp("c2", "download_fake", `{"id":"42"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"42"}`),
	}}

	a := newTestAgent(t, llm, src)
	track, err := a.Fetch(context.Background(), &Status{Query: "晴天"}, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}
	if track.src != Source(src) {
		t.Errorf("track.src = %v, want 下载它的那个音源", track.src)
	}
	if track.cand.ID != "42" {
		t.Errorf("track.cand.ID = %q, want 42", track.cand.ID)
	}
	// 会话必须跟着候选一起过来：musicso 的取词那一跳要用它。
	if track.cand.dlCookie != "sess" {
		t.Errorf("track.cand.dlCookie = %q, want sess", track.cand.dlCookie)
	}
}
```

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run TestAgentTrackCarriesSourceAndCandidate -v`
Expected: FAIL —— `track.src` 是 nil、`track.cand.ID` 是空串。

- [ ] **Step 3: 填字段**

在 `internal/music/agent.go` 的 `doDownload` 里，把成功分支那个 `r.track = &Track{…}` 改成：

```go
	r.track = &Track{
		LocalPath: path,
		Artist:    c.Artist, // 先填站点原始名，finalize 里会被模型规范化后的名字覆盖
		Title:     c.Title,
		Duration:  c.Duration,
		Bytes:     n,
		Source:    srcName,
		// 取词凭据：Runner 稍后拿它们去问这个音源要歌词。
		// 在这里填是因为此刻 src 和 c 正好都在手上；出了这个函数就得
		// 靠一张注册表按名字回查，那会多出一个"查不到"的分支。
		// 这两个字段跟 Track 的其它字段一样，一旦交出去就再不改动
		// （见 report.go 对 Status 快照自洽的说明）。
		src:  src,
		cand: c,
	}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/music -run TestAgent -v`
Expected: 全部 PASS。特别确认 `TestAgentFinalizeDoesNotMutatePublishedTrack` 与 `TestAgentFinalizeNoRaceWithThrottledFlush` 仍绿 —— 它们守着"发布出去的 Track 不再被改"这条约定。

- [ ] **Step 5: 带 race 检测再跑一遍并提交**

Run: `go test -race ./internal/music -run TestAgent`
Expected: PASS，无 DATA RACE。

```bash
gofmt -w internal/music/agent.go internal/music/agent_test.go
go vet ./internal/music
git add internal/music/agent.go internal/music/agent_test.go
git commit -F - <<'EOF'
#feat agent 把音源与候选填进 Track

下载成功那一刻 src 和 candidate 正好都在手上，此时填进 Track 最省事；
出了 doDownload 就得靠一张注册表按名字回查，凭空多出一个"查不到源"的
分支。finalize 造新 Track 时的值拷贝会把这两个字段一起带走，测试锁住了
这一点——漏了的表现是"所有歌都没歌词"且不报任何错。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 5: `Lyrics.Save` —— 取词并落盘

**Files:**
- Modify: `internal/music/lyric.go`
- Test: `internal/music/lyric_test.go`（新建）

**Interfaces:**
- Consumes: Task 2 的 `errLyricUnsupported` 与 `Source.Lyric`；Task 3 的 `LyricStatus`、`Track.src/cand`
- Produces: `type Lyrics struct{}` 与 `func (Lyrics) Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)`

- [ ] **Step 1: 写失败测试**

Create `internal/music/lyric_test.go`：

```go
package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// lyricSource 是一个只关心 Lyric 的假音源。
//
// 它必须把 Source 的五个方法都实现掉（接口就是这么宽），
// 但除 Lyric 之外的四个都不会被 Lyrics.Save 调到，给最省事的实现即可。
type lyricSource struct {
	lyric string
	err   error
}

func (s *lyricSource) Name() string { return "fakelyric" }
func (s *lyricSource) Hint() string { return "测试歌词源" }
func (s *lyricSource) Search(context.Context, string) ([]Candidate, error) {
	return nil, nil
}
func (s *lyricSource) Download(context.Context, Candidate, io.Writer) (int64, error) {
	return 0, nil
}
func (s *lyricSource) Lyric(context.Context, Candidate) (string, error) {
	return s.lyric, s.err
}

// newLyricTrack 造一个已下载好的 Track，src 指向给定的假音源。
func newLyricTrack(src Source) *Track {
	return &Track{Artist: "周杰伦", Title: "晴天", Source: "fakelyric", src: src, cand: Candidate{ID: "1"}}
}

// TestLyricsSaveOK：拿到歌词 → 落盘，路径与内容都对。
func TestLyricsSaveOK(t *testing.T) {
	const lrc = "[ti:晴天]\n[00:00.00]晴天 - 周杰伦"
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&lyricSource{lyric: lrc}), dir, "周杰伦 - 晴天.lrc")

	if st.State != LyricOK {
		t.Fatalf("State = %v, want LyricOK", st.State)
	}
	if want := filepath.Join(dir, "周杰伦 - 晴天.lrc"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读歌词文件失败: %v", err)
	}
	// 内容必须一字不动地落盘：时间戳和换行都是播放器要用的。
	if string(b) != lrc {
		t.Errorf("落盘内容 = %q, want %q", b, lrc)
	}
}

// TestLyricsSaveUnsupported：音源不供词 → LyricUnsupported，不落盘。
func TestLyricsSaveUnsupported(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&lyricSource{err: errLyricUnsupported}), dir, "a.lrc")

	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported", st.State)
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveUnsupportedWrapped 验证：哨兵被 %w 包过一层照样认得出来。
//
// 各音源在自己的错误里包一层说明是本仓库的常规做法（见 musicSoChallenge
// 对 errSourceUnavailable 的用法），所以判别必须走 errors.Is 而不是 ==。
func TestLyricsSaveUnsupportedWrapped(t *testing.T) {
	src := &lyricSource{err: fmt.Errorf("mp3.pm 的页面里没有歌词: %w", errLyricUnsupported)}

	_, st := Lyrics{}.Save(context.Background(), newLyricTrack(src), t.TempDir(), "a.lrc")
	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported（哨兵被包装后仍应认出）", st.State)
	}
}

// TestLyricsSaveMissing：音源供词但返回空 → LyricMissing，不落盘。
//
// 判别力：如果实现把空歌词也写出去，网盘里会多一个 0 字节的 .lrc，
// 播放器认出它、显示一片空白，比没有歌词更糟。
func TestLyricsSaveMissing(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&lyricSource{lyric: ""}), dir, "a.lrc")

	if st.State != LyricMissing {
		t.Fatalf("State = %v, want LyricMissing", st.State)
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveBlankIsMissing：只有空白字符同样算未收录，不落盘。
func TestLyricsSaveBlankIsMissing(t *testing.T) {
	dir := t.TempDir()

	_, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&lyricSource{lyric: "   \n\t"}), dir, "a.lrc")

	if st.State != LyricMissing {
		t.Fatalf("State = %v, want LyricMissing", st.State)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveFailed：取词报错 → LyricFailed 且带原因，不落盘。
func TestLyricsSaveFailed(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&lyricSource{err: errors.New("被 Cloudflare 拦截")}), dir, "a.lrc")

	if st.State != LyricFailed {
		t.Fatalf("State = %v, want LyricFailed", st.State)
	}
	if st.Err == "" {
		t.Error("LyricFailed 必须带上失败原因，否则进度消息说不出病根")
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveNilSource：防御性路径 —— src 为 nil 时按"不供词"处理，不 panic。
func TestLyricsSaveNilSource(t *testing.T) {
	_, st := Lyrics{}.Save(context.Background(), &Track{Artist: "A", Title: "T"}, t.TempDir(), "a.lrc")
	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported", st.State)
	}

	// Track 本身为 nil 也不能炸。
	if _, st2 := Lyrics{}.Save(context.Background(), nil, t.TempDir(), "a.lrc"); st2.State != LyricUnsupported {
		t.Fatalf("nil Track 时 State = %v, want LyricUnsupported", st2.State)
	}
}

// assertDirEmpty 断言目录里一个文件都没有。
// 非 LyricOK 的每条路径都必须做到这一点：临时目录随后会被整个上传流程
// 扫一遍，留下半成品只会让一个残缺的 .lrc 被传上网盘。
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("目录里不该有文件，实际有 %d 个: %v", len(entries), entries)
	}
}
```

（import 里的 `io` 是 `lyricSource.Download` 的签名需要的，`fmt` 是包装哨兵那条用例需要的。）

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run TestLyricsSave -v`
Expected: **编译失败**，报 `Lyrics undefined`。

- [ ] **Step 3: 实现 `Lyrics.Save`**

在 `internal/music/lyric.go` 里，哨兵之后追加（并把 import 补成 `context`、`errors`、`log/slog`、`os`、`path/filepath`、`strings`）：

```go
// Lyrics 是"取词并落盘"这一步。
//
// 它是**无状态**的：取词凭据全挂在 Track 上（src / cand），落盘位置由调用方给。
// 既然无状态，为什么还抽成一个类型而不是包级函数？为了 Runner 能通过一个
// 小接口注入 fake 来单测编排逻辑 —— 同 fetcher / uploader 那两个消费侧接口
// 的做法。包级函数是没法替换的。
type Lyrics struct{}

// Save 取词并落盘，返回 .lrc 的本地路径（没有歌词时是空串）与这一步的状态。
//
// filename 由调用方用 BuildLyricFilename 拼好，Save 不参与命名 ——
// .lrc 与 .mp3 必须逐字同名（含截断结果）播放器才配得上对，让两个名字
// 出自同一处（upload.go 的 buildBase）是这条约束唯一的保障点。
//
// **永不返回 error**，这是刻意的：歌词是附赠品，一首已经下好的歌不该因为
// 没配上词就传不上去。签名里干脆不给 error，调用方在类型层面就没有
// "把它当致命错误"的选项 —— 比靠注释约束可靠，同 ai.DeepSeek.Summarize
// 返回兜底字符串加 nil 的手法。失败信息通过 LyricStatus.Err 呈现。
func (Lyrics) Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus) {
	// 防御性的：正常路径下 agent 一定填了 src（见 agent.go 的 doDownload）。
	// 这里兜底只是不想让一条取词路径有 panic 掉整个进程的可能 ——
	// /music 那个 goroutine 是 detached 的、没有 recover。
	if t == nil || t.src == nil {
		return "", LyricStatus{State: LyricUnsupported}
	}

	lrc, err := t.src.Lyric(ctx, t.cand)
	if err != nil {
		// errors.Is 沿着 %w 包装链找哨兵，所以音源里包了几层都认得出来。
		if errors.Is(err, errLyricUnsupported) {
			return "", LyricStatus{State: LyricUnsupported}
		}
		slog.WarnContext(ctx, "取歌词失败", "source", t.Source, "id", t.cand.ID, "err", err)
		return "", LyricStatus{State: LyricFailed, Err: err.Error()}
	}

	// 空（或只有空白）= 站点没收录这首的词。不落盘：一个内容空白的 .lrc
	// 传上网盘只会让播放器显示一片空白，比没有歌词更糟。
	if strings.TrimSpace(lrc) == "" {
		return "", LyricStatus{State: LyricMissing}
	}

	path := filepath.Join(dir, filename)
	// 0o600 与 agent 下载临时文件的权限一致：临时目录里的东西不必给别人看。
	if wErr := os.WriteFile(path, []byte(lrc), 0o600); wErr != nil {
		// 删掉可能的半成品（WriteFile 可能已经建好文件才写失败）：
		// 残缺的 .lrc 传上网盘比没有更糟，同 doDownload 删下载半成品的理由。
		// os.IsNotExist 过滤掉"本来就没建成"这种正常情况，免得刷无用的告警。
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.WarnContext(ctx, "删除歌词半成品失败", "path", path, "err", rmErr)
		}
		slog.ErrorContext(ctx, "写歌词文件失败", "path", path, "err", wErr)
		return "", LyricStatus{State: LyricFailed, Err: wErr.Error()}
	}

	slog.InfoContext(ctx, "歌词已保存", "path", path, "bytes", len(lrc))
	return path, LyricStatus{State: LyricOK}
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/music -run TestLyricsSave -v`
Expected: 全部 PASS（7 个用例）。

- [ ] **Step 5: 整包回归并提交**

Run: `go test ./internal/music`
Expected: PASS

```bash
gofmt -w internal/music/lyric.go internal/music/lyric_test.go
go vet ./internal/music
git add internal/music/lyric.go internal/music/lyric_test.go
git commit -F - <<'EOF'
#feat Lyrics.Save：取词并落盘，四种结局都不阻断

签名里不给 error 是刻意的：歌词是附赠品，一首已经下好的歌不该因为没配上
词就传不上去。不给 error，调用方在类型层面就没有"把它当致命错误"的选项，
比靠注释约束可靠——同 DeepSeek.Summarize 返回兜底字符串加 nil 的手法。

非 LyricOK 的每条路径都保证不在临时目录里留文件：空白歌词不落盘（一个
内容空白的 .lrc 会让播放器显示一片空白，比没有更糟），写失败删半成品。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 6: 文件名拆出共用主体

**Files:**
- Modify: `internal/music/upload.go`（`BuildFilename` 附近）
- Test: `internal/music/upload_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func buildBase(artist, title string) string`（私有）、`func BuildLyricFilename(artist, title string) string`；`BuildFilename` 签名与行为不变

- [ ] **Step 1: 写失败测试**

追加到 `internal/music/upload_test.go` 末尾：

```go
// TestBuildLyricFilenameMatchesMP3 是这次改动里最要紧的一条断言：
// .lrc 与 .mp3 去掉扩展名之后必须**逐字相等**，包括触发截断的超长输入。
//
// 为什么单独锁死：播放器是靠"同目录同名"把歌词配给音频的。一旦两个名字
// 的截断结果差一个字，歌词就永远配不上 —— 而且不会有任何报错，
// 用户只会觉得"下了歌词但没用"。
func TestBuildLyricFilenameMatchesMP3(t *testing.T) {
	cases := []struct{ artist, title string }{
		{"周杰伦", "晴天"},
		{"", ""},                                   // 两个都空 → unknown 兜底
		{"A/B", "C:D"},                             // 危险字符要被同样清洗
		{strings.Repeat("周", 200), "晴天"},          // 超长，必然触发 120 rune 截断
		{"周杰伦", strings.Repeat("晴", 200)},        // 截断落在歌名一侧
	}
	for _, c := range cases {
		mp3 := BuildFilename(c.artist, c.title)
		lrc := BuildLyricFilename(c.artist, c.title)

		baseMP3 := strings.TrimSuffix(mp3, ".mp3")
		baseLRC := strings.TrimSuffix(lrc, ".lrc")
		if baseMP3 != baseLRC {
			t.Errorf("主体不一致:\n mp3 = %q\n lrc = %q", baseMP3, baseLRC)
		}
		if !strings.HasSuffix(mp3, ".mp3") {
			t.Errorf("BuildFilename 结果 %q 应以 .mp3 结尾", mp3)
		}
		if !strings.HasSuffix(lrc, ".lrc") {
			t.Errorf("BuildLyricFilename 结果 %q 应以 .lrc 结尾", lrc)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run TestBuildLyricFilenameMatchesMP3 -v`
Expected: **编译失败**，报 `BuildLyricFilename undefined`。

- [ ] **Step 3: 拆分实现**

用下面这段替换 `internal/music/upload.go` 里现有的 `BuildFilename`：

```go
// buildBase 拼出文件名主体（不含扩展名）：清洗危险字符、兜底、按 rune 截断。
//
// 抽出来是为了 .mp3 与 .lrc 共用**同一个**主体。两个文件名必须逐字一致
// （含截断结果），播放器才能靠"同目录同名"把歌词配给音频；差一个字就永远
// 配不上，而且不会有任何报错。这个函数就是那条约束唯一的保障点。
//
// 为什么必须清洗：歌名里的 '/' 会被 WebDAV 当成路径分隔符，把文件写到一个
// 意料之外的子目录里（甚至 404）。其余字符是 Windows 文件名非法字符，
// 网盘客户端同步下来会出问题。
func buildBase(artist, title string) string {
	a := strings.TrimSpace(filenameReplacer.Replace(artist))
	tt := strings.TrimSpace(filenameReplacer.Replace(title))
	base := strings.TrimSpace(a + " - " + tt)

	// 歌手和歌名都空时会剩下一个孤零零的 "-"，兜个底避免出现 " - .mp3"。
	if strings.Trim(base, " -") == "" {
		base = "unknown"
	}
	// TruncateUTF8 按 rune 截断，绝不会把一个中文字符切成两半。
	return tools.TruncateUTF8(base, maxFilenameRunes)
}

// BuildFilename 拼出 "<歌手> - <歌名>.mp3"。
func BuildFilename(artist, title string) string {
	return buildBase(artist, title) + ".mp3"
}

// BuildLyricFilename 拼出 "<歌手> - <歌名>.lrc"，与 BuildFilename 同主体。
func BuildLyricFilename(artist, title string) string {
	return buildBase(artist, title) + ".lrc"
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/music -run 'TestBuild' -v`
Expected: 全部 PASS，含既有的 `BuildFilename` 用例（行为未变）。

- [ ] **Step 5: 提交**

```bash
gofmt -w internal/music/upload.go internal/music/upload_test.go
go vet ./internal/music
git add internal/music/upload.go internal/music/upload_test.go
git commit -F - <<'EOF'
#feat 文件名拆出共用主体，新增 BuildLyricFilename

播放器靠"同目录同名"把歌词配给音频，两个名字的截断结果差一个字就永远
配不上，而且不会有任何报错——用户只会觉得"下了歌词但没用"。让两个名字
出自同一个 buildBase 是这条约束唯一的保障点，测试拿超长输入锁死了它。
BuildFilename 的签名与行为不变。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 7: `Uploader` 改成传多个文件

本任务**不引入歌词**，只把 `Upload` 从"一个文件"改成"一个文件切片"并落实可选文件语义。歌词在 Task 8 才接上来。

**Files:**
- Modify: `internal/music/upload.go`（`Upload`、`putOne`，新增 `UploadFile`、`putAll`）
- Modify: `internal/music/runner.go`（`uploader` 接口与调用点，改成传单元素切片）
- Test: `internal/music/upload_test.go`（8 处调用点迁移 + 新用例）
- Modify: `internal/music/runner_test.go`（`fakeUploader` 跟着改签名）

**Interfaces:**
- Consumes: 无
- Produces:
  - `type UploadFile struct { LocalPath, Filename, ContentType string; Optional bool }`
  - `func (u *Uploader) Upload(ctx context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error)`
  - `runner.go` 的 `uploader` 接口同步改成这个签名

- [ ] **Step 1: 加测试辅助并迁移既有调用点**

在 `internal/music/upload_test.go` 的 `writeTempMP3` 之后加一个辅助函数：

```go
// mp3Only 把"一个 mp3 文件"包成 Upload 现在要的切片形态。
// 既有那些只关心单文件行为的用例用它迁移，改动量最小。
func mp3Only(path, filename string) []UploadFile {
	return []UploadFile{{LocalPath: path, Filename: filename, ContentType: "audio/mpeg"}}
}
```

把文件里 8 处 `u.Upload(context.Background(), <path>, <filename>, <cb>)` 逐一改成
`u.Upload(context.Background(), mp3Only(<path>, <filename>), <cb>)`。行号参考（改动后会漂移，按内容定位）：72、144、181、217、242、266、340、368。

同时把 `internal/music/runner_test.go` 的 `fakeUploader` 改成：

```go
// fakeUploader 替代真实 Uploader。
type fakeUploader struct {
	states   []TargetStatus
	err      error
	gotFiles []UploadFile // 记录 Runner 传进来的文件列表，用于断言组装逻辑
}

func (f *fakeUploader) Upload(_ context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	f.gotFiles = files
	if onProgress != nil {
		onProgress(f.states)
	}
	return f.states, f.err
}

// mp3Name 返回文件列表里那个 mp3 的文件名，没有则返回空串。
// 既有用例断言的是"上传文件名对不对"，用这个小助手迁移最省事。
func (f *fakeUploader) mp3Name() string {
	for _, x := range f.gotFiles {
		if !x.Optional {
			return x.Filename
		}
	}
	return ""
}
```

把 `runner_test.go` 里所有 `u.gotFile` 改成 `u.mp3Name()`（两处：`TestRunnerHappyPath` 的断言、`TestRunnerFetchFailure` 的 `if u.gotFile != ""`；后者改成 `if len(u.gotFiles) != 0`）。

- [ ] **Step 2: 写新的失败测试**

追加到 `internal/music/upload_test.go` 末尾：

```go
// TestUploadOptionalFileFailureKeepsTargetOK 是本次改动的核心语义：
// 可选文件（歌词）传失败时，目标仍然算成功，只带一句 Warn。
//
// 判别力：把 Optional 判断去掉后，这条会变成 TargetFailed —— 也就是
// 一个附赠的 .lrc 没传上去就把已经传好的歌判成失败，正是要避免的事。
func TestUploadOptionalFileFailureKeepsTargetOK(t *testing.T) {
	// 服务端：.mp3 收下，.lrc 一律 507。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, ".lrc") {
			w.WriteHeader(http.StatusInsufficientStorage)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{{Name: "阿里云盘", URL: srv.URL + "/dav"}}, "alist", "pw")

	files := []UploadFile{
		{LocalPath: writeTempMP3(t, "ID3fake"), Filename: "A - T.mp3", ContentType: "audio/mpeg"},
		{LocalPath: writeTempMP3(t, "[ti:T]"), Filename: "A - T.lrc", ContentType: "text/plain; charset=utf-8", Optional: true},
	}
	got, err := u.Upload(context.Background(), files, nil)
	if err != nil {
		t.Fatalf("可选文件失败不该让整体报错: %v", err)
	}
	if len(got) != 1 || got[0].State != TargetOK {
		t.Fatalf("目标状态 = %+v, want TargetOK", got)
	}
	if got[0].Warn == "" {
		t.Error("可选文件失败必须留下 Warn，否则用户不知道歌词没传上去")
	}
	if got[0].Err != "" {
		t.Errorf("目标成功时 Err 应为空，实际 %q", got[0].Err)
	}
}

// TestUploadRequiredFailureSkipsRest 验证：必传文件失败后，
// **不再**发起该目标后续文件的 PUT —— 这个目标已经废了，
// 继续传歌词只是白占带宽和一次请求。
func TestUploadRequiredFailureSkipsRest(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden) // mp3 就失败
	}))
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{{Name: "A", URL: srv.URL + "/dav"}}, "alist", "pw")

	files := []UploadFile{
		{LocalPath: writeTempMP3(t, "x"), Filename: "a.mp3", ContentType: "audio/mpeg"},
		{LocalPath: writeTempMP3(t, "y"), Filename: "a.lrc", ContentType: "text/plain; charset=utf-8", Optional: true},
	}
	got, err := u.Upload(context.Background(), files, nil)
	if err == nil {
		t.Fatal("唯一目标的必传文件失败时整体应报错")
	}
	if got[0].State != TargetFailed {
		t.Errorf("目标状态 = %v, want TargetFailed", got[0].State)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Errorf("必传文件失败后不该再传后续文件，实际发了 %d 次请求: %v", len(paths), paths)
	}
}

// TestUploadFileContentTypes 验证：每个文件用自己的 Content-Type，
// 不再是写死的 audio/mpeg。歌词是文本，声明成 audio/mpeg 会让某些
// WebDAV 后端存成二进制、下载回来变成乱码。
func TestUploadFileContentTypes(t *testing.T) {
	var mu sync.Mutex
	types := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		types[filepath.Ext(r.URL.Path)] = r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{{Name: "A", URL: srv.URL + "/dav"}}, "alist", "pw")

	files := []UploadFile{
		{LocalPath: writeTempMP3(t, "x"), Filename: "a.mp3", ContentType: "audio/mpeg"},
		{LocalPath: writeTempMP3(t, "y"), Filename: "a.lrc", ContentType: "text/plain; charset=utf-8", Optional: true},
	}
	if _, err := u.Upload(context.Background(), files, nil); err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if types[".mp3"] != "audio/mpeg" {
		t.Errorf(".mp3 的 Content-Type = %q, want audio/mpeg", types[".mp3"])
	}
	if types[".lrc"] != "text/plain; charset=utf-8" {
		t.Errorf(".lrc 的 Content-Type = %q, want text/plain; charset=utf-8", types[".lrc"])
	}
}
```

- [ ] **Step 3: 运行测试确认它失败**

Run: `go test ./internal/music -run TestUpload -v`
Expected: **编译失败**，报 `UploadFile undefined` 与 `Upload` 参数不匹配。

- [ ] **Step 4: 改 `upload.go`**

在 `Target` 定义之后插入：

```go
// UploadFile 是一次任务里要传给每个目标的一个文件。
type UploadFile struct {
	LocalPath   string // 本地路径
	Filename    string // 传到目标上的文件名（会被 url.PathEscape 转义）
	ContentType string // 如 "audio/mpeg" / "text/plain; charset=utf-8"
	// Optional 为 true 表示这个文件传失败**不算目标失败**。
	// 歌词就是这样的附赠品：不能因为一个 .lrc 没传上去，就把已经传好的
	// 歌判成失败 —— 那和"不为一个网盘挂掉丢弃已下好的歌"是同一条约定。
	Optional bool
}
```

把 `Upload` 的签名与 goroutine 体改成：

```go
// Upload 并发把 files 传到所有目标。
//
// files 的顺序有意义：每个目标**顺序**传自己那份，排在前面的先落地。
// 调用方应把要紧的文件放在前面（mp3 在前、歌词在后）——ctx 中途断掉时，
// 先传完的是要紧那个。
//
// onProgress 在初始时以及**每个目标完成时**各回调一次，参数是当前所有目标
// 状态的快照副本（不是内部切片本身，调用方随便读不用担心竞态）。可传 nil。
//
// 返回值：最终状态切片总是返回（哪怕全失败，调用方要靠它渲染每个目标的成败）；
// error 仅在**所有**目标都失败时才非 nil —— 至少一个成功就算任务成功，
// 延续仓库"AI/digest 失败不阻断推送"的既有约定，不为一个网盘挂掉丢弃已下好的歌。
// 注意"目标失败"只由**必传**文件决定：可选文件失败只记进 TargetStatus.Warn。
func (u *Uploader) Upload(ctx context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
```

（函数体前半部分 `states` 初始化、`mu`、`notify`、`wg` 全部保持不变。）

goroutine 里把 `err := u.putOne(cctx, tg, localPath, filename)` 到 `notify()` 之间那段改成：

```go
			warn, err := u.putAll(cctx, tg, files)

			mu.Lock()
			states[idx].Warn = warn
			if err != nil {
				states[idx].State = TargetFailed
				states[idx].Err = err.Error()
			} else {
				states[idx].State = TargetOK
			}
			mu.Unlock()

			if err != nil {
				slog.ErrorContext(ctx, "WebDAV 上传失败", "target", tg.Name, "err", err)
			} else {
				slog.InfoContext(ctx, "WebDAV 上传成功", "target", tg.Name, "files", len(files), "warn", warn)
			}
			notify()
```

在 `Upload` 之后、`putOne` 之前插入：

```go
// putAll 把 files 依次传到一个目标。
//
// 返回 (可选文件的失败说明, 必传文件的失败)。error 非 nil 表示这个目标失败了。
//
// 为什么顺序传而不是并发：同一个目标就那么点带宽，并发只会互相抢；而且
// 顺序执行才能兑现"必传文件失败后不再传后面的"——那个目标已经废了，
// 继续传歌词是白占一次请求。
func (u *Uploader) putAll(ctx context.Context, t Target, files []UploadFile) (string, error) {
	var warn string
	for _, f := range files {
		err := u.putOne(ctx, t, f)
		if err == nil {
			continue
		}
		if !f.Optional {
			// 必传文件失败：这个目标已经废了，不必再传后面的。
			return warn, err
		}
		// 可选文件失败：目标仍算成功，只留一句说明。
		// 多个可选文件都失败时后一条覆盖前一条 —— 目前只有一个可选文件
		// （歌词），真出现多个再改成拼接也不迟。
		slog.ErrorContext(ctx, "可选文件上传失败", "target", t.Name, "file", f.Filename, "err", err)
		warn = f.Filename + ": " + err.Error()
	}
	return warn, nil
}
```

把 `putOne` 改成收 `UploadFile`：

```go
// putOne 把单个文件 PUT 到单个目标。
func (u *Uploader) putOne(ctx context.Context, t Target, uf UploadFile) error {
	// 每个文件各自打开一次：*os.File 内部有共享的读偏移量，
	// 多个 goroutine 拿同一个 *os.File 当 body 会互相把对方的读位置搅乱。
	f, err := os.Open(uf.LocalPath)
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
	dst := strings.TrimRight(t.URL, "/") + "/" + url.PathEscape(uf.Filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, dst, f)
	if err != nil {
		return err
	}
	// 显式给出长度：不设的话 net/http 会走 chunked 传输，
	// 部分 WebDAV 服务端（含某些 alist 后端）会直接拒绝。
	req.ContentLength = fi.Size()
	req.SetBasicAuth(u.user, u.pass)
	// 每个文件用自己的类型：歌词是文本，声明成 audio/mpeg 会让某些
	// WebDAV 后端存成二进制、下载回来变成乱码。
	req.Header.Set("Content-Type", uf.ContentType)

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
```

- [ ] **Step 5: 改 `runner.go` 让它继续编译**

把 `uploader` 接口改成：

```go
type uploader interface {
	Upload(ctx context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error)
}
```

把 `Run` 里的调用改成（歌词在 Task 8 才加进来）：

```go
	// 目前只有 mp3 一个文件；Task 8 会在这里追加歌词。
	files := []UploadFile{{
		LocalPath:   track.LocalPath,
		Filename:    filename,
		ContentType: "audio/mpeg",
	}}

	targets, upErr := r.uploader.Upload(ctx, files, func(ts []TargetStatus) {
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/music -run 'TestUpload|TestRunner' -v`
Expected: 全部 PASS。既有的 `TestUploadBothSucceed`、`TestUploadAllFailed`、部分失败等用例是回归门，**不许放宽**。

- [ ] **Step 7: 带 race 检测跑并提交**

Run: `go test -race ./internal/music`
Expected: PASS，无 DATA RACE（`TestRunnerConcurrentUploadNoRace` 用真实 `*Uploader`，会覆盖到并发写 `states[idx].Warn` 这条新路径）。

```bash
gofmt -w internal/music/upload.go internal/music/upload_test.go internal/music/runner.go internal/music/runner_test.go
go vet ./internal/music
git add internal/music/upload.go internal/music/upload_test.go internal/music/runner.go internal/music/runner_test.go
git commit -F - <<'EOF'
#feat Uploader 改成传多个文件，可选文件失败不判目标失败

Upload 从"一个文件"改成 []UploadFile，每个目标顺序传自己那份，mp3 在前
歌词在后——ctx 中途断掉时先落地的是要紧那个。必传文件失败后不再传该目标
后续文件（目标已经废了，继续传只是白占一次请求）。

Optional 文件失败只记进 TargetStatus.Warn，目标仍算成功：不能因为一个
附赠的 .lrc 没传上去，就把已经传好的歌判成失败。Content-Type 从写死的
audio/mpeg 挪进 UploadFile——歌词是文本，声明成音频会被某些 WebDAV 后端
存成二进制、下载回来变成乱码。

本次不接歌词，Runner 仍只传单元素切片。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 8: Runner 串起取词与上传

**Files:**
- Modify: `internal/music/runner.go`
- Test: `internal/music/runner_test.go`

**Interfaces:**
- Consumes: Task 5 的 `Lyrics.Save`；Task 6 的 `BuildLyricFilename`；Task 7 的 `UploadFile`
- Produces: `const lyricTimeout = 30 * time.Second`；`type lyricSaver interface{ Save(...) }`；`Runner.lyrics lyricSaver` 字段（由 `NewRunner` 填 `Lyrics{}`，**签名不变**）

- [ ] **Step 1: 写失败测试**

在 `internal/music/runner_test.go` 的 `fakeUploader` 之后加 fake，并追加用例：

```go
// fakeLyrics 替代真实 Lyrics，让 Runner 的编排逻辑能脱离网络单测。
type fakeLyrics struct {
	path    string
	status  LyricStatus
	gotDir  string
	gotName string
}

func (f *fakeLyrics) Save(_ context.Context, _ *Track, dir, filename string) (string, LyricStatus) {
	f.gotDir = dir
	f.gotName = filename
	return f.path, f.status
}

// newTestRunnerWithLyrics 组装注入了三个 fake 的 Runner。
func newTestRunnerWithLyrics(f *fakeFetcher, u *fakeUploader, l *fakeLyrics, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, lyrics: l, editor: e}
}

// TestRunnerUploadsLyricAsOptional：有歌词时，上传列表是 mp3 + lrc 两个文件，
// 且 lrc 必须标成 Optional —— 否则它传失败会把整个目标判成失败。
func TestRunnerUploadsLyricAsOptional(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "周杰伦", Title: "晴天", Bytes: 100, Source: "musicso"}}
	u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
	l := &fakeLyrics{path: "/tmp/x.lrc", status: LyricStatus{State: LyricOK}}

	if err := newTestRunnerWithLyrics(f, u, l, &fakeEditor{}).Run(context.Background(), 1, "晴天"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	if len(u.gotFiles) != 2 {
		t.Fatalf("上传文件数 = %d, want 2（mp3 + lrc）: %+v", len(u.gotFiles), u.gotFiles)
	}
	// mp3 必须排在前面：ctx 中途断掉时先落地的应该是要紧那个。
	if u.gotFiles[0].Filename != "周杰伦 - 晴天.mp3" || u.gotFiles[0].Optional {
		t.Errorf("第一个文件 = %+v, want 必传的 mp3", u.gotFiles[0])
	}
	if u.gotFiles[1].Filename != "周杰伦 - 晴天.lrc" {
		t.Errorf("第二个文件名 = %q, want 周杰伦 - 晴天.lrc", u.gotFiles[1].Filename)
	}
	if !u.gotFiles[1].Optional {
		t.Error("歌词必须标成 Optional，否则它传失败会把整个目标判成失败")
	}
	if u.gotFiles[1].ContentType != "text/plain; charset=utf-8" {
		t.Errorf("歌词 Content-Type = %q, want text/plain; charset=utf-8", u.gotFiles[1].ContentType)
	}
	// 取词必须发生在 agent 的临时目录里，才会被 defer os.RemoveAll 清掉。
	if l.gotDir != f.gotDir {
		t.Errorf("取词目录 = %q, want 与下载同一个临时目录 %q", l.gotDir, f.gotDir)
	}
	if l.gotName != "周杰伦 - 晴天.lrc" {
		t.Errorf("传给 Save 的文件名 = %q, want 周杰伦 - 晴天.lrc", l.gotName)
	}
}

// TestRunnerNoLyricStillUploads：没有歌词时只传 mp3，任务照样成功。
// 这是"歌词的任何问题都不能让一首已经下好的歌传不上去"在编排层的兑现。
func TestRunnerNoLyricStillUploads(t *testing.T) {
	for _, st := range []LyricStatus{
		{State: LyricUnsupported},
		{State: LyricMissing},
		{State: LyricFailed, Err: "被 Cloudflare 拦截"},
	} {
		f := &fakeFetcher{track: &Track{Artist: "A", Title: "T", Source: "mp3pm"}}
		u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
		l := &fakeLyrics{path: "", status: st}

		if err := newTestRunnerWithLyrics(f, u, l, &fakeEditor{}).Run(context.Background(), 1, "x"); err != nil {
			t.Fatalf("歌词状态 %v 时 Run 不该报错: %v", st.State, err)
		}
		if len(u.gotFiles) != 1 {
			t.Errorf("歌词状态 %v 时上传文件数 = %d, want 1", st.State, len(u.gotFiles))
		}
	}
}

// TestRunnerReportsLyricStatus：歌词状态必须进快照，用户才看得到。
func TestRunnerReportsLyricStatus(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T", Source: "mp3pm"}}
	u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
	l := &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}
	fe := &fakeEditor{}

	if err := newTestRunnerWithLyrics(f, u, l, fe).Run(context.Background(), 1, "x"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	fe.mu.Lock()
	defer fe.mu.Unlock()
	all := strings.Join(append(append([]string{}, fe.sends...), fe.edits...), "\n")
	if !strings.Contains(all, "mp3pm 不提供") {
		t.Errorf("终态消息里应说明歌词为何没有，实际:\n%s", all)
	}
}

// TestNewRunnerWiresDefaultLyrics 验证：NewRunner 装配了默认的 Lyrics，
// 使用方（main）不必知道歌词的存在，也就不用改一行接线代码。
func TestNewRunnerWiresDefaultLyrics(t *testing.T) {
	r := NewRunner(nil, nil, nil)
	if r.lyrics == nil {
		t.Fatal("NewRunner 必须填上默认的 Lyrics，否则 Run 会空指针")
	}
}
```

同时把既有的 `newTestRunner` 改成也填上一个不做事的 lyrics，让老用例继续能跑：

```go
// newTestRunner 组装注入了 fake 的 Runner。
// lyrics 给一个"该音源不供词"的 fake：老用例都不关心歌词，
// 这个默认值让它们一个字都不用改。
func newTestRunner(f *fakeFetcher, u *fakeUploader, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, lyrics: &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}, editor: e}
}
```

`TestRunnerConcurrentUploadNoRace` 里直接构造 `&Runner{…}` 的那处也要补上 `lyrics: &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}`。

- [ ] **Step 2: 运行测试确认它失败**

Run: `go test ./internal/music -run TestRunner -v`
Expected: **编译失败**，报 `unknown field lyrics in struct literal`。

- [ ] **Step 3: 改 `runner.go`**

在 `agentTimeout` 之后加常量：

```go
// lyricTimeout 是取歌词那一步的超时。
//
// 它就是一次 JSON 请求（musicso 的 play.php），几百毫秒的事，不该有资格
// 去啃外层 command.Bot 那 600s 预算里 agent 和上传要用的部分。
// 同 agentTimeout / targetUploadTimeout 的套路：每一步各管各的时限，
// 一步卡住不拖垮后面的步骤。
const lyricTimeout = 30 * time.Second
```

在 `uploader` 接口之后加：

```go
// lyricSaver 同 fetcher / uploader，也是消费侧接口：Runner 只依赖
// "取词并落盘"这一件能力，测试塞个 fake 就能脱离网络验编排。
//
// 注意它**不返回 error** —— 歌词是附赠品，编排层在类型层面就没有
// "把它当致命错误"的选项（见 lyric.go 对 Save 的说明）。
type lyricSaver interface {
	Save(ctx context.Context, t *Track, dir, filename string) (string, LyricStatus)
}
```

`Runner` 结构体加字段，`NewRunner` 填默认值：

```go
// Runner 把 agent、取词、上传、进度上报串成一次完整的 /music 任务。
type Runner struct {
	fetcher  fetcher
	uploader uploader
	lyrics   lyricSaver
	editor   notify.Editor // 用来给每次任务新建一个 Reporter
}

// NewRunner 构造函数。收具体类型、存接口，是仓库里一贯的注入风格。
//
// lyrics 不出现在参数里、而是在这里装配默认实现：Lyrics 是无状态的，
// 没有任何依赖需要从外面注入，让 main 去 new 一个空结构体传进来纯属噪音。
// 测试要替换它时在同包内直接改这个私有字段即可 —— 同 Uploader.timeout /
// Agent.maxBytes / tgReporter.terminalTimeout 那套"抽成字段以便测试"的做法。
// 这也是本次改动不用碰 cmd/news2tg/main.go 的原因。
func NewRunner(a *Agent, u *Uploader, e notify.Editor) *Runner {
	return &Runner{fetcher: a, uploader: u, lyrics: Lyrics{}, editor: e}
}
```

`Run` 里把 `filename := …` 到 `files := …` 之间那段改成：

```go
	// 文件名用模型规范化后的歌手/歌名，清洗掉危险字符。
	// .lrc 与 .mp3 共用同一个主体（见 buildBase），播放器才配得上对。
	filename := BuildFilename(track.Artist, track.Title)
	lrcName := BuildLyricFilename(track.Artist, track.Title)

	// 先把选定的曲目显示出来，此刻还没进入上传阶段。
	st.Track = track
	rep.Update(ctx, *st)

	// 取歌词。单独套一层短超时（它只是一次 JSON 请求），且**永不阻断** ——
	// Save 的签名里根本没有 error，拿不到词照样把歌传上去。
	lctx, lcancel := context.WithTimeout(ctx, lyricTimeout)
	lrcPath, lyricSt := r.lyrics.Save(lctx, track, dir, lrcName)
	lcancel() // 不用 defer：后面的上传不该再受取词那层超时约束
	st.Lyric = &lyricSt

	st.Stage = StageUploading
	rep.Update(ctx, *st)

	slog.InfoContext(ctx, "开始上传音乐到 WebDAV",
		"file", filename, "bytes", track.Bytes, "lyric", lyricSt.State)

	// mp3 排在第一个：Uploader 按顺序传，ctx 中途断掉时先落地的是要紧那个。
	files := []UploadFile{{
		LocalPath:   track.LocalPath,
		Filename:    filename,
		ContentType: "audio/mpeg",
	}}
	if lrcPath != "" {
		// Optional：歌词传不上去不该把这个目标判成失败
		// （见 upload.go 里 UploadFile.Optional 的说明）。
		files = append(files, UploadFile{
			LocalPath:   lrcPath,
			Filename:    lrcName,
			ContentType: "text/plain; charset=utf-8",
			Optional:    true,
		})
	}
```

把原先那句 `st.Track, st.Stage = track, StageUploading` 与它后面那次 `rep.Update`、以及原来的 `slog.InfoContext(ctx, "开始上传音乐到 WebDAV", …)` 一并删掉（已被上面这段取代）。

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/music -run TestRunner -v`
Expected: 全部 PASS，含既有的 happy path / fetch 失败 / 上传全失败 / 部分失败四个用例。

- [ ] **Step 5: 整包 + race 回归并提交**

Run: `go test -race ./internal/music`
Expected: PASS，无 DATA RACE

```bash
gofmt -w internal/music/runner.go internal/music/runner_test.go
go vet ./internal/music
git add internal/music/runner.go internal/music/runner_test.go
git commit -F - <<'EOF'
#feat Runner 串起取词：mp3 与 .lrc 一起传上 WebDAV

取词插在 agent 返回与上传之间，套 30s 独立超时（它只是一次 JSON 请求，
不该去啃外层 600s 预算里 agent 和上传要用的部分）。歌词以 Optional 文件
追加在 mp3 之后：排在后面是因为 ctx 中途断掉时先落地的应该是要紧那个，
标 Optional 是因为它传失败不该把整个目标判成失败。

lyrics 不进 NewRunner 的参数表而是在里面装配默认实现：Lyrics 无状态、
没有要从外面注入的依赖，让 main 去 new 一个空结构体传进来纯属噪音。
测试在同包内直接改私有字段替换它——同 Uploader.timeout 那套做法。
这也是整个改动不用碰 main.go 的原因。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

### Task 9: 文档同步与全量验收

**Files:**
- Modify: `CLAUDE.md`（`music` 那一段、`Conventions` 不动）
- Modify: `internal/music/source.go`（包顶部分工注释，若 Task 2 已加则核对一遍）

**Interfaces:**
- Consumes: 前八个任务的全部产出
- Produces: 无代码产出

- [ ] **Step 1: 更新 `CLAUDE.md`**

在 `CLAUDE.md` 的 `**music**` 那一条里，「**上传不是工具**」那句之前插入下面这段（保持既有的中文行文与换行宽度）：

```
歌词是 `Source` 接口的**一项能力**（`Lyric`）而不是独立的可选接口：
可选断言那种「实现了就自动生效、没实现就静默没有」很容易在加音源时漏掉，
表现为"这个源的歌永远没词"这种没人会去查的静默缺失，写进接口以后编译器
会替我们向每个新音源作者要这个答案。`MusicSo` 复用 `play.php` 那一跳里
的 `lrc` 字段（实测是内联的标准 LRC 正文）供词；`Mp3PM` 返回
`errLyricUnsupported` 哨兵明确表态不供，好让进度消息说得出「mp3pm 不提供」
而不是跟「站点未收录」混为一谈。取词由 `music.Lyrics`（`lyric.go`）在
agent 返回后、上传前执行，`.lrc` 与 mp3 **同名同目录**（两个名字都出自
`buildBase`，差一个字播放器就配不上对）一起传上 WebDAV，并且标成
`UploadFile.Optional` —— 歌词传失败不把目标判成失败。
**歌词的任何问题都不阻断已下好的歌上传**，沿用「AI/digest 失败不阻断推送」
那条约定。因此从 mp3.pm 下的歌没有歌词，这是设计如此，不是缺陷。
```

- [ ] **Step 2: 核对包顶部分工注释**

确认 `internal/music/source.go` 顶部的"包内分工"清单里有 `lyric.go` 一行（Task 2 Step 5 加过）。没有就补上：

```
//	lyric.go    取歌词并落盘（Source.Lyric 的编排层）
```

- [ ] **Step 3: 全量验收**

依次运行，全部必须通过：

```bash
gofmt -w internal/music/ CLAUDE.md 2>/dev/null; gofmt -l internal/music/
go vet ./...
go build -o bin/news2tg ./cmd/news2tg
go test -short ./...
go test -race ./internal/music
```

Expected:
- `gofmt -l internal/music/` 输出为空（**只看这个目录**；仓库全局因 CRLF 天然列出约 26 个文件，不是验收门）
- `go vet ./...` 无输出
- 构建成功
- `go test -short ./...` 全部 ok
- `-race` 无 DATA RACE

**如果 `go build` 报 `cmd/news2tg/main.go` 相关错误，说明前面某处走偏了** —— 本次设计的硬约束是 main.go 一行都不改。回头查是不是把 `Lyrics` 塞进了 `NewRunner` 的参数表。

- [ ] **Step 4: 提交**

```bash
git add CLAUDE.md internal/music/source.go
git commit -F - <<'EOF'
#docs CLAUDE.md 同步歌词链路

说明歌词为何长在 Source 接口上而不是可选断言接口、两种"没有歌词"为何要
分开、以及"从 mp3.pm 下的歌没有歌词"是设计如此而不是缺陷——后一条不写
下来，日后一定会被当成 bug 报上来。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
```

---

## 附：人工验收（可选，需要真网络与真配置）

自动化测试全部离线。想在真环境上验一次的话：

1. 确保 `myconfig.toml`（或你在用的那份）里 `[music]` 段配好，且 `sources` 含 `musicso`。
2. 启动服务，向 bot 发 `/music 晴天 周杰伦`。
3. 期望：进度消息里出现 `✅ 歌词`，WebDAV 目标目录下同时出现
   `周杰伦 - 晴天.mp3` 与 `周杰伦 - 晴天.lrc`，后者内容以 `[ti:` 开头、多行带时间戳。
4. 再发一次一首 musicso 搜不到、只能从 mp3.pm 下的歌，期望进度消息里是
   `➖ 歌词：mp3pm 不提供`，且只上传了 mp3。
