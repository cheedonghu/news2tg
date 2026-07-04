package digest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url" // 安全拼 query 参数
)

// defaultDigestURL 是本机 Python sidecar 的正文抽取端点。
// （原先写死在 monitor 包的 hnDigestURL 常量，现随实现一起迁到这里。）
const defaultDigestURL = "http://127.0.0.1:50051/digest"

// Python 用本机 Python sidecar（hacker-news-digest）抽取网页正文，实现 Fetcher 接口。
// 字段全小写 = 包外不可见，外部只能用 NewPython 构造、用接口方法操作。
type Python struct {
	httpClient *http.Client // 指针：复用 main 里创建的共享连接池
}

// NewPython 构造函数。注意没有返回 error：这里只是配置，没真发请求。
func NewPython(httpClient *http.Client) *Python {
	return &Python{httpClient: httpClient}
}

// Fetch 调本机 sidecar 拿正文。签名匹配 Fetcher.Fetch，所以 *Python 隐式实现接口。
func (p *Python) Fetch(ctx context.Context, originURL string) (string, error) {
	slog.InfoContext(ctx, "开始获取源网址摘要", "originNewsURL", originURL)

	// url.Parse 把字符串解析成 *url.URL，方便安全拼参数。
	u, err := url.Parse(defaultDigestURL)
	if err != nil {
		return "", err
	}
	// 取 query，加参数，再写回。url.Values 是 map[string][]string 的别名。
	q := u.Query()
	q.Set("newsUrl", originURL)
	u.RawQuery = q.Encode() // Encode 会自动做 URL escape

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用python接口获取帖子摘要失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("提取摘要文本失败: %w", err)
	}
	return string(body), nil
}
