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
)

// browserUA 定义在 source.go（而不是这里）：mp3.pm 和 musicso.cc 两个站点
// 都对默认的 Go-http-client UA 不友好（前者搜索/下载会被针对，后者直接被
// Cloudflare 质询拦下），这不是某一个站的特有逻辑，是两个实现共享的常量。

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
