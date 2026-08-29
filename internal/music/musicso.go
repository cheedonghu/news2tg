package music

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	// 第三方包：HTML 解析（类似 jQuery 的 API），mp3pm.go 与 hackernews.go 已在用。
	"github.com/PuerkitoBio/goquery"

	"github.com/cheedonghu/news2tg/internal/tools"
)

const (
	// musicSoName 是源标识，同时决定工具名 search_musicso / download_musicso。
	musicSoName = "musicso"
	// musicSoBaseURL 是站点根地址。搜索走 /s/<关键词>，解析直链走 /api/play.php。
	musicSoBaseURL = "https://www.musicso.cc"
)

// 编译期断言：*MusicSo 必须满足 Source。接口漏实现会在编译阶段就报错。
var _ Source = (*MusicSo)(nil)

// musicSoHrefRe 从搜索结果的详情页链接里抠出 id 与后端标识。
//
// href 形如 /music/0039MnYb0qxYhV-q-晴天-周杰伦.html：
// 第一段是 id，第二段是单字母的后端标识（q = QQ 音乐，n = 网易云），
// 再往后才是歌名和歌手 —— 而它们**可能自带连字符**（"晴天 - Live"）。
// 所以必须从左往右锚定着取前两段，绝不能按 '-' 全切再猜。
// [^-]+ 对 id 是安全的：QQ 的 mid 是 14 位 base62、网易的是纯数字，都不含 '-'。
var musicSoHrefRe = regexp.MustCompile(`^/music/([^-]+)-([a-z])-`)

// MusicSo 是 musicso.cc 的 Source 实现。
//
// 这个站把 QQ 音乐和网易云的搜索结果**合并排序后一次给出**，
// 所以这里是一个 Source 而不是按后端拆成好几个 —— 模型本来也无从判断
// "这首歌在哪家有"，拆开只会让工具数翻倍、白烧 token。
type MusicSo struct {
	httpClient *http.Client
	// baseURL 单独抽成字段（而非直接用常量）是为了测试能指向 httptest，
	// 与 mp3pm.go 里 searchAPI、digest/jina.go 里 baseURL 的做法一致。
	baseURL string
}

// NewMusicSo 构造函数。httpClient 复用 main 里创建的共享连接池。
func NewMusicSo(httpClient *http.Client) *MusicSo {
	return &MusicSo{httpClient: httpClient, baseURL: musicSoBaseURL}
}

// Name 返回源标识。
func (m *MusicSo) Name() string { return musicSoName }

// Hint 告诉模型这个站怎么搜才有效。
// 和 mp3.pm 的规则正好相反：那边是俄语站、中文歌按拼音收录，这边是中文站。
func (m *MusicSo) Hint() string {
	return "中文音乐站，聚合 QQ 音乐与网易云，歌名歌手都是中文原名。" +
		"中文歌请直接用中文关键词搜索（歌名，或「歌名 歌手」），不要转成拼音或英文译名。" +
		"英文歌用原文英文名搜索。该站不提供时长信息。"
}

// Search 只需一次 GET：搜索页在返回结果 HTML 的同时下发本轮会话 cookie。
//
// 会话为什么必须每次现拿：站点的直链解析接口 /api/play.php 认 PHPSESSID，
// 而 PHP 会话默认 24 分钟就过期，本进程却是常驻的、可能几天才来一次 /music。
// 只在启动时拿一次的话，绝大多数真实调用都会撞在过期会话上。
// 好在会话和搜索是同一个请求，"现拿现用"不额外多一跳。
func (m *MusicSo) Search(ctx context.Context, query string) ([]Candidate, error) {
	slog.InfoContext(ctx, "开始在 musicso 搜索", "query", query)

	// 关键词走 path 而不是 query：站点的搜索地址就是 /s/<关键词>。
	// url.PathEscape 负责把空格、中文等转义成合法的路径片段。
	dst := strings.TrimRight(m.baseURL, "/") + "/s/" + url.PathEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dst, nil)
	if err != nil {
		return nil, err
	}
	// 站点在 Cloudflare 后面：不带浏览器 UA 一律被质询拦下。
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("musicso 搜索请求失败: %w", err)
	}
	// defer 在函数返回时执行，保证连接被释放回连接池。
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 musicso 搜索页失败: %w", err)
	}

	// 质询要在状态码判断**之前**认：有些质询是 200 + 挑战页，
	// 光看状态码会把它当成一个正常但没有结果的页面放过去。
	if err := musicSoChallenge(ctx, resp, string(body)); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("musicso 搜索页返回非 2xx 状态码 %d", resp.StatusCode)
	}

	// 捞出本轮会话。拿不到不在这里报错：也许站点改了 cookie 名，
	// 真正的后果会在 Download 那一跳以明确的 403 呈现出来；
	// 但先 WARN 一条，让日志能指向病根。
	session := musicSoSession(resp)
	if session == "" {
		slog.WarnContext(ctx, "musicso 搜索页未下发 PHPSESSID，下载多半会失败")
	}

	return parseMusicSoResults(ctx, string(body), session), nil
}

// musicSoSession 从响应里取出 PHPSESSID 的值。
// resp.Cookies() 解析的是 Set-Cookie 响应头，返回 []*http.Cookie。
func musicSoSession(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "PHPSESSID" {
			return c.Value
		}
	}
	return ""
}

// musicSoChallenge 识别 Cloudflare 的 bot 质询。
//
// 为什么必须单独识别、而不是让它自然地表现成"零结果"：
// 质询页是一个没有任何 song-item 的 HTML，若只看"解析出几条"，
// 它会静默返回空切片 —— 模型于是换几个关键词全部无果，用户最终看到的是
// "这首歌搜不到"，一条完全指不到病根的失败信息。站点是否可达这件事，
// 必须以它自己的面目报出来。
//
// 两条特征分别覆盖两种形态：cf-mitigated 响应头（CF 的正式标记），
// 以及挑战页标题（响应头被中间层剥掉时的兜底）。
func musicSoChallenge(ctx context.Context, resp *http.Response, body string) error {
	mitigated := resp.Header.Get("cf-mitigated")
	if mitigated == "" && !strings.Contains(body, "Just a moment") {
		return nil
	}
	ray := resp.Header.Get("cf-ray")
	slog.ErrorContext(ctx, "musicso 被 Cloudflare 拦截",
		"status", resp.StatusCode, "cf-mitigated", mitigated, "cf-ray", ray)
	// %w 包一层 errSourceUnavailable：这是站点整体拒绝服务，换关键词毫无
	// 意义，agent.go 的 doSearch 靠 errors.Is 认出这个哨兵，把回灌文案
	// 从"换个关键词再试"改成"换个音源"，别让模型在这条死路上空转。
	return fmt.Errorf("musicso 被 Cloudflare 拦截（bot 质询，cf-ray=%s）；"+
		"该出口 IP 已被判定为机器人，需要更换出口或配置 [network] proxy: %w", ray, errSourceUnavailable)
}

// parseMusicSoResults 从搜索结果页 HTML 里抽出候选列表。
//
// 页面结构（实测）：每首歌一个 <div class="song-item">，
// 歌名/歌手在子元素里，id 与后端标识编码在详情页链接的 href 上。
// 也就是说搜索结果页一次性给全，不需要再进详情页。
//
// 返回值不带 error：解析不出东西时返回空切片，由调用方当作"零结果"处理 ——
// 对模型来说"没搜到"和"页面变了"都该走同一条"换关键词重试"的路。
// 但两者对我们排障的意义不同，所以下面额外打一条 WARN 区分。
func parseMusicSoResults(ctx context.Context, htmlBody, cookie string) []Candidate {
	// goquery 从 io.Reader 解析；strings.NewReader 把字符串包成 Reader。
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		slog.ErrorContext(ctx, "解析 musicso 结果页 HTML 失败", "err", err)
		return nil
	}

	var out []Candidate
	// Each 的回调签名是 func(下标 int, 元素 *goquery.Selection)；_ 表示丢弃下标。
	doc.Find("div.song-item").Each(func(_ int, s *goquery.Selection) {
		// Attr 返回 (值, 是否存在)；没有播放链接的条目拿不到 id，跳过。
		href, ok := s.Find("a.song-play").First().Attr("href")
		if !ok {
			return
		}
		// FindStringSubmatch 返回 [整体匹配, 第1组, 第2组…]；不匹配返回 nil。
		mm := musicSoHrefRe.FindStringSubmatch(href)
		if mm == nil {
			// href 形状不对（站点改版，或这条本来就是个占位），
			// 模型选了也下不下来，直接丢掉。
			return
		}
		id, backend := mm[1], mm[2]

		// First() 防御同一条目里意外出现多个同类元素；
		// Text() 会把 HTML 实体（&#039;）解码成真正的字符。
		title := strings.TrimSpace(s.Find(".song-name").First().Text())
		artist := strings.TrimSpace(s.Find("a.song-artist-link").First().Text())
		if title == "" {
			return // 连歌名都没有的条目对模型毫无意义
		}

		out = append(out, Candidate{
			// id 里带上后端标识，下载时才知道该往 play.php 传哪个 type。
			// 拼在一起而不是给 Candidate 再加一个私有字段：这个形状
			// 照抄站点 href 自己的写法，模型原样照抄即可，Go 侧一分为二。
			ID:       backend + "-" + id,
			Artist:   artist,
			Title:    title,
			Duration: "", // 站点不提供时长；renderStatus 会因此省略那一段
			dlCookie: cookie,
		})
	})

	// 页面有内容却一条都没解析出来 —— 大概率是站点改版了，而不是"真没这首歌"。
	// 单独告警，让日志能区分这两种情况，否则改版会静默表现成"永远搜不到"。
	if len(out) == 0 && strings.TrimSpace(htmlBody) != "" {
		slog.WarnContext(ctx, "musicso 结果页解析出 0 条候选（可能站点改版）", "bodyLen", len(htmlBody))
	}
	return out
}

// maxPlayRespBytes 是 /api/play.php 响应体的读取上限。
//
// 这里原先是无上限的 io.ReadAll —— 整个响应被读进内存再 json.Unmarshal。
// 一个坏掉或恶意的站点可以让它变得任意大，而我们没有任何防线。
// 上限放在 play 这一个读取点上、不下放给调用方，理由同 agent.go 的
// defaultMaxBytes：否则每多一个调用方就要重复实现一遍同样的防御。
// 实测真实响应约 3 KB，1 MB 有 300 倍余量。
const maxPlayRespBytes = 1 << 20

// musicSoPlayResp 是 /api/play.php 的响应形状。
// 实测响应键固定为 code / msg / url / lrc / pic 五个；pic 用不上，
// 让 encoding/json 自动丢弃。
type musicSoPlayResp struct {
	Code int    `json:"code"` // 1 = 成功
	Msg  string `json:"msg"`
	URL  string `json:"url"` // CDN 直链
	LRC  string `json:"lrc"` // 内联的 LRC 歌词正文，可能为空
}

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
