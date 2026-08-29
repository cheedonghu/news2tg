package music

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseMusicSoResults 锁死 HTML 解析行为，尤其是两条畸形条目必须被跳过。
func TestParseMusicSoResults(t *testing.T) {
	got := parseMusicSoResults(context.Background(), loadFixture(t, "musicso_search.html"), "sess123")

	// 5 条 song-item 里，第 4 条 href 形状不对、第 5 条压根没有 song-play，
	// 两条都拿不到 id，模型选了也下不下来，必须在这里就被丢掉。
	if len(got) != 3 {
		t.Fatalf("解析出 %d 条候选，want 3（两条畸形条目应被跳过）\n%+v", len(got), got)
	}

	if got[0].ID != "q-0039MnYb0qxYhV" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "q-0039MnYb0qxYhV")
	}
	if got[0].Artist != "周杰伦" || got[0].Title != "晴天" {
		t.Errorf("[0] = {%q, %q}, want {周杰伦, 晴天}", got[0].Artist, got[0].Title)
	}
	// 站点不提供时长，必须是空串 —— renderStatus 依赖它来省略那一段。
	if got[0].Duration != "" {
		t.Errorf("[0].Duration = %q, want 空串（站点不提供时长）", got[0].Duration)
	}
	// 会话必须跟着候选走，下载时才拿得到。
	if got[0].dlCookie != "sess123" {
		t.Errorf("[0].dlCookie = %q, want %q", got[0].dlCookie, "sess123")
	}

	// 网易源的 id 是纯数字，前缀是 n。
	if got[1].ID != "n-3339230677" {
		t.Errorf("[1].ID = %q, want %q", got[1].ID, "n-3339230677")
	}

	// 歌名里含连字符的条目：正则只能吃掉最左边的 id 和 type，
	// 不能被歌名里的 '-' 带偏。
	if got[2].ID != "q-004Fs2FP1EvZYc" {
		t.Errorf("[2].ID = %q, want %q", got[2].ID, "q-004Fs2FP1EvZYc")
	}
	if got[2].Title != "晴天 - Live" {
		t.Errorf("[2].Title = %q, want %q", got[2].Title, "晴天 - Live")
	}
}

// TestParseMusicSoResultsEmpty 验证：没有任何 song-item 时返回空切片而不是报错。
// "没搜到"是正常路径，交给模型换关键词，不该当成错误中断。
func TestParseMusicSoResultsEmpty(t *testing.T) {
	got := parseMusicSoResults(context.Background(), "<html><body>没有找到</body></html>", "s")
	if len(got) != 0 {
		t.Fatalf("空页面应解析出 0 条，实际 %d 条", len(got))
	}
}

// newMusicSoServer 起一个假的 musicso 站点。
//
// 它同时充当断言器：把每一跳收到的 Cookie 记下来，测试据此验证
// "play.php 确实带上了搜索那一跳下发的会话"和"CDN 那一跳没带 cookie"。
func newMusicSoServer(t *testing.T, fixture string) (*httptest.Server, *musicSoCalls) {
	t.Helper()
	calls := &musicSoCalls{}
	mux := http.NewServeMux()

	// ① 搜索页：下发会话 + 返回结果页 HTML
	mux.HandleFunc("/s/", func(w http.ResponseWriter, r *http.Request) {
		calls.searchUA = r.Header.Get("User-Agent")
		calls.searchPath = r.URL.Path
		http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "the-session", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, fixture)
	})

	// ② 解析直链：必须带会话
	mux.HandleFunc("/api/play.php", func(w http.ResponseWriter, r *http.Request) {
		calls.playCookie = r.Header.Get("Cookie")
		calls.playQuery = r.URL.RawQuery
		if !strings.Contains(calls.playCookie, "PHPSESSID=the-session") {
			_, _ = io.WriteString(w, "forbidden")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// 直链指回本服务器的 /cdn，方便一路测到底。
		fmt.Fprintf(w, `{"code":1,"msg":"成功","url":%q,"lrc":"","pic":""}`, calls.base+"/cdn/song.mp3")
	})

	// ③ CDN：不该带 cookie
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, r *http.Request) {
		calls.cdnCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "ID3fake-mp3-bytes")
	})

	srv := httptest.NewServer(mux)
	calls.base = srv.URL
	t.Cleanup(srv.Close)
	return srv, calls
}

// musicSoCalls 记录假服务端观察到的请求细节，供断言使用。
//
// 这里不像 upload_test.go 的 davRecorder 那样加锁：那边的多个 PUT 是**并发**
// 发出的，这边 Search / Download 里的每一跳都是顺序发的，而且所有断言都发生在
// 对应方法返回之后 —— HTTP 往返完成本身就建立了 happens-before 关系。
type musicSoCalls struct {
	base       string
	searchUA   string
	searchPath string
	playCookie string
	playQuery  string
	cdnCookie  string
}

// TestMusicSoSearch 覆盖搜索这一跳：URL 形状、浏览器 UA、候选内容。
func TestMusicSoSearch(t *testing.T) {
	srv, calls := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Search(context.Background(), "晴天 周杰伦")
	if err != nil {
		t.Fatalf("Search 意外报错: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search 返回 %d 条, want 3", len(got))
	}
	// 关键词必须走 path 而不是 query（站点的搜索是 /s/<关键词>）。
	if !strings.HasPrefix(calls.searchPath, "/s/") {
		t.Errorf("搜索路径 = %q, want /s/ 前缀", calls.searchPath)
	}
	// 不带浏览器 UA 会被 Cloudflare 拦下，这条是硬要求。
	if calls.searchUA != browserUA {
		t.Errorf("搜索 UA = %q, want browserUA", calls.searchUA)
	}
	// 会话必须被捕获并写进每一条候选。
	if got[0].dlCookie != "the-session" {
		t.Errorf("候选未带上会话，dlCookie = %q", got[0].dlCookie)
	}
}

// TestMusicSoDownload 覆盖下载两跳，并锁死两条关键的 cookie 语义：
// play.php 必须带会话，CDN 必须不带。
func TestMusicSoDownload(t *testing.T) {
	srv, calls := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	n, err := m.Download(context.Background(),
		Candidate{ID: "q-0039MnYb0qxYhV", dlCookie: "the-session"}, &buf)
	if err != nil {
		t.Fatalf("Download 意外报错: %v", err)
	}
	const payload = "ID3fake-mp3-bytes"
	if n != int64(len(payload)) || buf.String() != payload {
		t.Errorf("下载内容 = %q (%d 字节), want %q", buf.String(), n, payload)
	}
	// id 与 type 必须被正确拆开塞进 query。
	if !strings.Contains(calls.playQuery, "id=0039MnYb0qxYhV") || !strings.Contains(calls.playQuery, "type=q") {
		t.Errorf("play.php query = %q, want 含 id=0039MnYb0qxYhV 与 type=q", calls.playQuery)
	}
	// CDN 那一跳带上 cookie 不会立刻出错，但会把会话泄漏给第三方 CDN，
	// 而且真实 CDN 对多余的 Cookie 头有时会直接 400。
	if calls.cdnCookie != "" {
		t.Errorf("CDN 请求不该带 cookie，实际 %q", calls.cdnCookie)
	}
}

// TestMusicSoDownloadWithoutSession 验证：会话缺失时 play.php 返回 forbidden，
// 必须变成 error 回灌给模型，而不是把 "forbidden" 当成 mp3 写进文件。
func TestMusicSoDownloadWithoutSession(t *testing.T) {
	srv, _ := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "q-0039MnYb0qxYhV", dlCookie: ""}, &buf); err == nil {
		t.Fatal("缺会话时应当报错")
	}
	if buf.Len() != 0 {
		t.Errorf("失败时不该往 writer 里写任何东西，实际写了 %q", buf.String())
	}
}

// TestMusicSoDownloadBadCode 验证：play.php 返回 code != 1 时报错，
// 让 agent 能把失败回灌给模型去改选候选。
func TestMusicSoDownloadBadCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"获取失败","url":""}`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "q-x", dlCookie: "s"}, &buf); err == nil {
		t.Fatal("code != 1 时应当报错")
	}
}

// TestMusicSoDownloadBadID 验证：候选 id 不是 <t>-<id> 形状时直接报错，
// 不要拿着半个 id 去打站点得到更莫名其妙的错误。
func TestMusicSoDownloadBadID(t *testing.T) {
	m := NewMusicSo(http.DefaultClient)
	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "没有连字符", dlCookie: "s"}, &buf); err == nil {
		t.Fatal("畸形 id 应当报错")
	}
}

// TestMusicSoCloudflareChallenge 验证：撞上 Cloudflare 质询时返回**明确的错误**，
// 而不是降级成"零结果"。
//
// 判别力：质询页是一个没有任何 song-item 的 HTML，如果实现只看
// "解析出几条"，它会静默返回空切片 —— 模型于是换几个关键词全部无果，
// 用户最终看到的是"这首歌搜不到"，一条完全指不到病根的失败信息。
func TestMusicSoCloudflareChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.Header().Set("cf-ray", "a2f236c9695cae43-LAX")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	_, err := m.Search(context.Background(), "晴天")
	if err == nil {
		t.Fatal("撞上 Cloudflare 质询时必须报错，不能静默当成零结果")
	}
	if !strings.Contains(err.Error(), "Cloudflare") {
		t.Errorf("错误信息应当点名 Cloudflare，实际: %v", err)
	}
}

// TestMusicSoChallengeWithout403 验证：有些质询是 200 + 挑战页，
// 光看状态码会漏掉，必须也认响应体特征。
func TestMusicSoChallengeWithout403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		_, _ = io.WriteString(w, `<html><head><title>Just a moment...</title></head></html>`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	if _, err := m.Search(context.Background(), "晴天"); err == nil {
		t.Fatal("cf-mitigated 存在时即便状态码是 200 也应报错")
	}
}

// TestMusicSoPlayRespTooLarge 验证：play.php 响应体超过上限时报错，
// 而不是拿着被截断的半个 JSON 继续往下走。
//
// 判别力全在"直链指回本服务器"这一点上：
//   - 有上限时，2 MB 的响应在 1 MB 处被截断 → JSON 不完整 → 解析失败 → Download 报错
//   - 没有上限时，整个 JSON 被完整读入、解析成功，直链又是**能连上的**，
//     Download 会一路成功返回 nil —— 用例于是失败
//
// 如果直链写成 http://x/... 这种连不上的地址，没有上限时 Download 也会因为
// 拨号失败而返回 error，用例照样通过 —— 那就测不出上限在不在了。
func TestMusicSoPlayRespTooLarge(t *testing.T) {
	mux := http.NewServeMux()
	// base 在 httptest.NewServer 返回后才知道，而处理函数是在收到请求时
	// 才读它的，所以这样赋值是安全的。newMusicSoServer 也是这个写法。
	var base string
	mux.HandleFunc("/api/play.php", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 语法完全合法、但体积远超上限：把 2 MB 填进 lrc 字段，
		// 这也更贴近真实场景（歌词才是这个响应里可能变大的那部分）。
		fmt.Fprintf(w, `{"code":1,"msg":"成功","url":%q,"lrc":%q,"pic":""}`,
			base+"/cdn/song.mp3", strings.Repeat("A", 2<<20))
	})
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ID3fake-mp3-bytes")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	_, err := m.Download(context.Background(), Candidate{ID: "q-x", dlCookie: "s"}, &buf)
	if err == nil {
		t.Fatal("响应体超过上限时应当报错（当前实现把整个 2 MB 读进来并成功下载了，说明上限没生效）")
	}
	if buf.Len() != 0 {
		t.Errorf("失败时不该往 writer 里写任何东西，实际写了 %d 字节", buf.Len())
	}
}
