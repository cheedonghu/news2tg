package digest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// jinaBaseURL 是 jina reader 的默认端点。
// 用法：GET https://r.jina.ai/<目标网址>，返回该网页清洗后的正文（markdown/纯文本）。
const jinaBaseURL = "https://r.jina.ai"

// Jina 用 jina reader（r.jina.ai）抽取网页正文，实现 Fetcher 接口。
// 相比 Python sidecar，它是远程服务、覆盖面更广，常作为兜底渠道。
type Jina struct {
	httpClient *http.Client // 复用 main 里创建的共享连接池
	apiToken   string       // jina api key；空串则走免认证公开端点（有更严的速率限制）
	baseURL    string       // 端点前缀，单独抽成字段是为了测试能指向 httptest
}

// NewJina 构造函数。apiToken 可为空（不带认证）。
func NewJina(httpClient *http.Client, apiToken string) *Jina {
	return &Jina{
		httpClient: httpClient,
		apiToken:   apiToken,
		baseURL:    jinaBaseURL,
	}
}

// Fetch 拼出 <baseURL>/<originURL> 并 GET，返回 jina 抽取的正文。
// 签名匹配 Fetcher.Fetch，所以 *Jina 隐式实现接口。
func (j *Jina) Fetch(ctx context.Context, originURL string) (string, error) {
	slog.InfoContext(ctx, "开始用 jina 获取源网址摘要", "originNewsURL", originURL)

	// jina 约定把完整目标网址（含 scheme）直接拼在端点后面。
	// 去掉 baseURL 末尾可能的 "/"，避免出现两个斜杠。
	target := strings.TrimRight(j.baseURL, "/") + "/" + originURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	// 有 key 才加认证头；jina 允许免 key 调用，所以这里是可选的。
	if j.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+j.apiToken)
	}

	resp, err := j.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 jina 接口获取帖子摘要失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("提取 jina 摘要文本失败: %w", err)
	}
	// 非 2xx 视作失败，让上层（agent）据此回退或回灌给模型。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina 返回非 2xx 状态码 %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}
