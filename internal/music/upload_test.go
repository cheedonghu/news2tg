package music

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// writeTempMP3 在 t.TempDir() 里造一个假 mp3 文件，返回路径。
// t.TempDir() 给每个测试独立目录，结束后由 testing 包自动清理。
func writeTempMP3(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "download.mp3")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时 mp3 失败: %v", err)
	}
	return p
}

// davRecorder 是一个假 WebDAV 服务端，记录收到的 PUT。
type davRecorder struct {
	mu     sync.Mutex
	paths  []string // 收到的请求路径
	bodies []string // 收到的请求体
	authOK bool     // 是否所有请求都带对了 Basic Auth
	status int      // 返回的状态码，0 视作 201
}

func (d *davRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		d.mu.Lock()
		d.paths = append(d.paths, r.URL.Path)
		d.bodies = append(d.bodies, string(body))
		d.authOK = ok && user == "alist" && pass == "pw"
		code := d.status
		d.mu.Unlock()
		if code == 0 {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}
}

// TestUploadBothSucceed 验证：两个目标都成功时无错误，两边都收到了文件。
func TestUploadBothSucceed(t *testing.T) {
	rec := &davRecorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "阿里云盘", URL: srv.URL + "/dav/aliyun/Music"},
		{Name: "OneDrive", URL: srv.URL + "/dav/onedrive/Music"},
	}, "alist", "pw")

	path := writeTempMP3(t, "ID3fake")
	got, err := u.Upload(context.Background(), path, "周杰伦 - 晴天.mp3", nil)
	if err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("返回 %d 个目标状态, want 2", len(got))
	}
	for _, s := range got {
		if s.State != TargetOK {
			t.Errorf("目标 %s 状态 = %d, want TargetOK；err=%s", s.Name, s.State, s.Err)
		}
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) != 2 {
		t.Fatalf("服务端收到 %d 个 PUT, want 2", len(rec.paths))
	}
	if !rec.authOK {
		t.Error("请求未携带正确的 Basic Auth")
	}
	for _, b := range rec.bodies {
		if b != "ID3fake" {
			t.Errorf("收到的内容 = %q, want %q", b, "ID3fake")
		}
	}
	// 两个目标各自的路径前缀必须不同，且文件名被 URL 转义过（含中文和空格）。
	joined := strings.Join(rec.paths, " ")
	if !strings.Contains(joined, "/dav/aliyun/Music/") || !strings.Contains(joined, "/dav/onedrive/Music/") {
		t.Errorf("PUT 路径不对: %v", rec.paths)
	}
}

// TestUploadPartialFailure 验证：一成一败仍算任务成功，且失败原因带在对应目标上。
// 这是「不为一个网盘挂掉丢弃已下好的歌」这条设计的守门测试。
func TestUploadPartialFailure(t *testing.T) {
	okSrv := httptest.NewServer((&davRecorder{}).handler())
	t.Cleanup(okSrv.Close)
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusInsufficientStorage}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(okSrv.Client(), []Target{
		{Name: "阿里云盘", URL: okSrv.URL + "/dav"},
		{Name: "OneDrive", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", nil)
	if err != nil {
		t.Fatalf("一成一败应算成功，却返回错误: %v", err)
	}
	if got[0].State != TargetOK {
		t.Errorf("阿里云盘状态 = %d, want TargetOK", got[0].State)
	}
	if got[1].State != TargetFailed {
		t.Fatalf("OneDrive 状态 = %d, want TargetFailed", got[1].State)
	}
	if !strings.Contains(got[1].Err, "507") {
		t.Errorf("OneDrive 错误信息 %q 应包含状态码 507", got[1].Err)
	}
}

// TestUploadAllFail 验证：全失败才算任务失败。
func TestUploadAllFail(t *testing.T) {
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusUnauthorized}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(badSrv.Client(), []Target{
		{Name: "A", URL: badSrv.URL + "/dav"},
		{Name: "B", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", nil)
	if err == nil {
		t.Fatal("全部目标失败时应返回错误")
	}
	for _, s := range got {
		if s.State != TargetFailed {
			t.Errorf("目标 %s 状态 = %d, want TargetFailed", s.Name, s.State)
		}
	}
}

// TestUploadProgressCallback 验证：每个目标完成都会回调一次，且回调拿到的是
// 完整状态切片（而不是单个目标）—— Reporter 要靠它渲染整块上传区。
func TestUploadProgressCallback(t *testing.T) {
	srv := httptest.NewServer((&davRecorder{}).handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "A", URL: srv.URL + "/dav"},
		{Name: "B", URL: srv.URL + "/dav"},
	}, "alist", "pw")

	var mu sync.Mutex
	var calls int
	_, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", func(ts []TargetStatus) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if len(ts) != 2 {
			t.Errorf("回调收到 %d 个目标状态, want 2（应为完整切片）", len(ts))
		}
	})
	if err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// 初始一次 + 每个目标完成各一次 = 3 次。
	if calls != 3 {
		t.Errorf("onProgress 被调用 %d 次, want 3（初始 1 次 + 两个目标各 1 次）", calls)
	}
}

// TestBuildFilename 锁死文件名清洗：路径分隔符必须被替换，否则 WebDAV 路径会被打歪。
func TestBuildFilename(t *testing.T) {
	cases := []struct {
		name   string
		artist string
		title  string
		want   string
	}{
		{"普通中文", "周杰伦", "晴天", "周杰伦 - 晴天.mp3"},
		{"歌手名带斜杠", "AC/DC", "Back in Black", "AC_DC - Back in Black.mp3"},
		{"歌名带全套危险字符", "A", `a/b\c:d*e?f"g<h>i|j`, "A - a_b_c_d_e_f_g_h_i_j.mp3"},
		{"换行被换成空格", "A", "a\nb", "A - a b.mp3"},
		{"两端空白被裁掉", "  周杰伦  ", "  晴天  ", "周杰伦 - 晴天.mp3"},
		{"歌手歌名全空 → 兜底", "", "", "unknown.mp3"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if got := BuildFilename(c.artist, c.title); got != c.want {
				t.Errorf("BuildFilename(%q, %q) = %q, want %q", c.artist, c.title, got, c.want)
			}
		})
	}
}

// TestBuildFilenameTruncatesByRune 验证超长歌名按 rune 截断，不产生乱码。
// 按字节切会把一个中文字符切成两半，得到 invalid UTF-8。
func TestBuildFilenameTruncatesByRune(t *testing.T) {
	long := strings.Repeat("很长的歌名", 100) // 500 个中文字符
	got := BuildFilename("歌手", long)

	if !strings.HasSuffix(got, ".mp3") {
		t.Fatalf("结果应以 .mp3 结尾: %q", got)
	}
	base := strings.TrimSuffix(got, ".mp3")
	if n := len([]rune(base)); n > maxFilenameRunes {
		t.Errorf("截断后 %d 个字符, want <= %d", n, maxFilenameRunes)
	}
	// 按字节切会把一个中文字符切成两半，产生非法 UTF-8；按 rune 切不会。
	if !utf8.ValidString(base) {
		t.Errorf("截断后不是合法 UTF-8: %q", base)
	}
}
