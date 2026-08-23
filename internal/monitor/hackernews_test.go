package monitor

import (
	"context"
	"testing"
)

// hnItemHTML 是一段裁剪过的 HN 帖子页 HTML，只保留解析要用到的结构。
// 用固定字符串而不是真实网络请求：这样测试不依赖外网，也不碰 Python sidecar。
const hnItemHTML = `<html><body><table><tbody>
<tr class="athing"><td class="title">
<span class="titleline"><a href="https://example.com/post">A Real Post Title</a>
<span class="sitebit comhead"> (example.com)</span></span></td></tr>
<tr><td class="subtext"><span class="age" title="2026-08-01T00:00:00">3 hours ago</span></td></tr>
</tbody></table></body></html>`

func TestGetNewsOriginInfo(t *testing.T) {
	ctx := context.Background()
	originURL, postTitle, err := getNewsOriginInfo(ctx, hnItemHTML)
	if err != nil {
		t.Fatalf("getNewsOriginInfo 报错: %v", err)
	}
	if originURL != "https://example.com/post" {
		t.Errorf("originURL = %q, want %q", originURL, "https://example.com/post")
	}
	if postTitle != "A Real Post Title" {
		t.Errorf("postTitle = %q, want %q", postTitle, "A Real Post Title")
	}
}

// 相对链接（如 "item?id=123"，Ask HN 这类没有外链的帖子）应被判为异常，
// 与改造前 getNewsOriginURL 的行为保持一致。
func TestGetNewsOriginInfoRejectsRelativeHref(t *testing.T) {
	const html = `<html><body><span class="titleline"><a href="item?id=123">Ask HN: something</a></span></body></html>`
	if _, _, err := getNewsOriginInfo(context.Background(), html); err == nil {
		t.Fatalf("相对链接应该返回 error")
	}
}
