package music

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFixture 读 testdata 下的固定样本。
// testdata 是 Go 工具链约定的目录名：它不会被当成包编译，专门放测试数据。
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 fixture %s 失败: %v", name, err)
	}
	return string(b)
}

// TestParseMp3PMResults 锁死 HTML 解析行为，尤其是脏标题。
func TestParseMp3PMResults(t *testing.T) {
	got := parseMp3PMResults(context.Background(), loadFixture(t, "mp3pm_search.html"))

	// 第三条缺 data-download-url，必须被跳过 —— 没有直链的候选给模型看也没用。
	if len(got) != 2 {
		t.Fatalf("解析出 %d 条候选，want 2（缺直链的那条应被跳过）", len(got))
	}
	if got[0].ID != "4287389" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "4287389")
	}
	if got[0].Artist != "Jay Chou" {
		t.Errorf("[0].Artist = %q, want %q", got[0].Artist, "Jay Chou")
	}
	if got[0].Title != "Kai Bu Liao Kou." {
		t.Errorf("[0].Title = %q, want %q", got[0].Title, "Kai Bu Liao Kou.")
	}
	if got[0].Duration != "04:44" {
		t.Errorf("[0].Duration = %q, want %q", got[0].Duration, "04:44")
	}
	if !strings.HasPrefix(got[0].dlURL, "https://cs1.mp3.pm/download/4287389/") {
		t.Errorf("[0].dlURL = %q，前缀不对", got[0].dlURL)
	}

	// 第二条是脏标题的重点：带 HTML 实体 &#039;、斜杠、中文、括号。
	// goquery 的 Text() 会把实体解码成真正的单引号，这正是我们要的。
	wantTitle := "Superman Can't Fly / 超人不會飛 (Chao Ren Bu Hui Fei)"
	if got[1].Title != wantTitle {
		t.Errorf("[1].Title = %q, want %q", got[1].Title, wantTitle)
	}
}

// TestParseMp3PMResultsEmpty 验证：页面里没有任何 li.cplayer-sound-item 时
// 返回空切片而非 panic。站点改版会走到这条路径。
func TestParseMp3PMResultsEmpty(t *testing.T) {
	got := parseMp3PMResults(context.Background(), "<html><body><p>nothing here</p></body></html>")
	if len(got) != 0 {
		t.Fatalf("空页面应解析出 0 条，实际 %d 条", len(got))
	}
}

// newMp3PMTestServer 起一个假 mp3.pm：
//
//	POST /public/api.search.php → 回一行指向本服务器结果页的 URL
//	GET  /result                → 回 fixture HTML
//
// 用一个 mux 同时扮演两跳，才能覆盖完整的搜索流程。
func newMp3PMTestServer(t *testing.T, fixture string) *httptest.Server {
	t.Helper()
	// 先声明再赋值：handler 里要用到 srv.URL，而 URL 在 NewServer 返回后才有值。
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/public/api.search.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("搜索接口应为 POST，实际 %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败: %v", err)
		}
		if q := r.PostFormValue("q"); q != "jay chou" {
			t.Errorf("q = %q, want %q", q, "jay chou")
		}
		// 真站返回的就是一行纯文本 URL（可能带换行）。
		_, _ = w.Write([]byte(srv.URL + "/result\n"))
	})
	mux.HandleFunc("/result", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixture))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestMp3PMSearch 覆盖完整两跳。
func TestMp3PMSearch(t *testing.T) {
	srv := newMp3PMTestServer(t, loadFixture(t, "mp3pm_search.html"))

	m := NewMp3PM(srv.Client())
	m.searchAPI = srv.URL + "/public/api.search.php" // 指向假服务器

	got, err := m.Search(context.Background(), "jay chou")
	if err != nil {
		t.Fatalf("Search 意外报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search 返回 %d 条，want 2", len(got))
	}
	if got[0].ID != "4287389" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "4287389")
	}
}

// TestMp3PMSearchBadRedirect 验证：搜索接口回的不是 URL 时报错，
// 而不是拿着垃圾去 GET 得到更莫名其妙的错误。
func TestMp3PMSearchBadRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("error: rate limited"))
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	m.searchAPI = srv.URL

	if _, err := m.Search(context.Background(), "x"); err == nil {
		t.Fatal("搜索接口返回非 URL 时应报错")
	}
}

// TestMp3PMDownload 验证下载把字节原样写进 io.Writer 并返回字节数。
func TestMp3PMDownload(t *testing.T) {
	const payload = "ID3fake-mp3-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	var buf strings.Builder
	n, err := m.Download(context.Background(), Candidate{ID: "1", dlURL: srv.URL}, &buf)
	if err != nil {
		t.Fatalf("Download 意外报错: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("写入 %d 字节, want %d", n, len(payload))
	}
	if buf.String() != payload {
		t.Errorf("内容 = %q, want %q", buf.String(), payload)
	}
}

// TestMp3PMDownloadNon2xx 验证非 2xx 时报错，让上层能回灌给模型换候选。
func TestMp3PMDownloadNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	m := NewMp3PM(srv.Client())
	var buf strings.Builder
	if _, err := m.Download(context.Background(), Candidate{ID: "1", dlURL: srv.URL}, &buf); err == nil {
		t.Fatal("403 时应报错")
	}
}
