package digest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestJinaFetch 真实访问 r.jina.ai，验证目标网页能被正常提取并返回非空正文。
// 这是联网集成测试：用 `go test -short` 可跳过，避免离线/CI 无网时失败。
func TestJinaFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过联网测试（-short）")
	}

	const originURL = "https://luyublog.com/PluginOfPikPak/"

	// 免 key 走公开端点；给一个宽松超时，jina 抓取+清洗偶尔较慢。
	j := NewJina(&http.Client{Timeout: 60 * time.Second}, "")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body, err := j.Fetch(ctx, originURL)
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if strings.TrimSpace(body) == "" {
		t.Fatalf("Fetch 返回空内容")
	}

	// 打印前若干字符，方便人工确认提取效果（go test -v 可见）。
	preview := []rune(body)
	if len(preview) > 200 {
		preview = preview[:200]
	}
	t.Logf("jina 提取到 %d 字节，前 200 字符:\n%s", len(body), string(preview))
}
