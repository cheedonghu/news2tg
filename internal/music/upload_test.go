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

// mp3Only 把"一个 mp3 文件"包成 Upload 现在要的切片形态。
// 既有那些只关心单文件行为的用例用它迁移，改动量最小。
func mp3Only(path, filename string) []UploadFile {
	return []UploadFile{{LocalPath: path, Filename: filename, ContentType: "audio/mpeg"}}
}

// davRecorder 是一个假 WebDAV 服务端，记录收到的 PUT。
type davRecorder struct {
	mu    sync.Mutex
	paths []string // 收到的请求路径（**已解码**：r.URL.Path 是解转义之后的）
	// rawPaths 是**未解码**的请求路径（r.URL.EscapedPath()）。
	// 两个都记是必要的：paths 能断言"路径末段就是清洗后的文件名"，
	// 但它天然看不出转义有没有做 —— 中文和空格解码后跟没转义时长得一模一样。
	// 真正把 url.PathEscape 锁死的是 rawPaths。
	rawPaths []string
	bodies   []string // 收到的请求体
	authOK   bool     // 是否所有请求都带对了 Basic Auth
	status   int      // 返回的状态码，0 视作 201
}

func (d *davRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		d.mu.Lock()
		d.paths = append(d.paths, r.URL.Path)
		d.rawPaths = append(d.rawPaths, r.URL.EscapedPath())
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
	const filename = "周杰伦 - 晴天.mp3" // 中文 + 空格，两种都必须被转义
	got, err := u.Upload(context.Background(), mp3Only(path, filename), nil)
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
	// 两个目标各自的路径前缀必须不同。
	joined := strings.Join(rec.paths, " ")
	if !strings.Contains(joined, "/dav/aliyun/Music/") || !strings.Contains(joined, "/dav/onedrive/Music/") {
		t.Errorf("PUT 路径不对: %v", rec.paths)
	}
	// spec 的测试项"PUT 路径就是清洗后的文件名"：只查目录前缀是查不到的，
	// 必须把末段一起断死 —— 解码后的路径末段必须**恰好**是那个文件名，
	// 不多不少，也没有被拆成子目录。
	wantSuffix := "/" + filename
	for i, p := range rec.paths {
		if !strings.HasSuffix(p, wantSuffix) {
			t.Errorf("第 %d 个 PUT 的解码路径 %q 末段不是清洗后的文件名 %q", i, p, filename)
		}
	}
	// 顺带记一笔：中文和空格**证明不了** url.PathEscape 的存在。
	// net/http 在写请求时会调 URL.EscapedPath()，发现 RawPath 不是合法转义就
	// 自己按 Path 重新转义一遍，于是"不转义"和"转义了"最终打到线上的字节
	// 完全相同（实测过）。真正只有 PathEscape 才能救的是下面那个用例里的
	// '#' 和 '%'，锁死转义行为的责任在它身上，别指望这里。
	for i, raw := range rec.rawPaths {
		if strings.ContainsAny(raw, " ") {
			t.Errorf("第 %d 个 PUT 的原始路径 %q 里不该出现裸空格", i, raw)
		}
	}
}

// TestUploadEscapesPathOnlySpecials 是补给 Minor E 的回归测试，
// 也是真正把 url.PathEscape 锁死的那一个。
//
// 为什么不用中文/空格来测：net/http 写请求时会自己补转义，
// 有没有 PathEscape 打到线上的字节一模一样，断言没有判别力（见上一个用例的注释）。
//
// '#' 和 '%' 才是分水岭，而且它们**都不在 filenameReplacer 的替换表里**，
// 也就是说完全可能出现在真实歌名里（"Sonata in C# minor"、"100% Love"）：
//   - 少了 PathEscape，'#' 会被 url.Parse 当成 fragment 分隔符，
//     PUT 实际落到 /dav/Sonata in C —— 文件名被悄悄截断，传上去的是个错名文件；
//   - '%' 更直接，http.NewRequest 当场报 invalid URL escape，整个目标上传失败。
func TestUploadEscapesPathOnlySpecials(t *testing.T) {
	for _, filename := range []string{"Sonata in C# minor.mp3", "100% Love.mp3"} {
		t.Run(filename, func(t *testing.T) {
			rec := &davRecorder{}
			srv := httptest.NewServer(rec.handler())
			t.Cleanup(srv.Close)

			u := NewUploader(srv.Client(), []Target{{Name: "A", URL: srv.URL + "/dav"}}, "alist", "pw")

			got, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), filename), nil)
			if err != nil {
				t.Fatalf("Upload 意外报错: %v", err)
			}
			if got[0].State != TargetOK {
				t.Fatalf("目标状态 = %d, want TargetOK；err=%s", got[0].State, got[0].Err)
			}

			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.paths) != 1 {
				t.Fatalf("服务端收到 %d 个 PUT, want 1", len(rec.paths))
			}
			// 解码回来必须一字不差 —— 说明 '#' / '%' 是作为**文件名的一部分**
			// 送过去的，而不是被 URL 语法吃掉。
			if want := "/dav/" + filename; rec.paths[0] != want {
				t.Errorf("PUT 路径 = %q, want %q", rec.paths[0], want)
			}
		})
	}
}

// TestUploadSeedsPendingState 是补给 Minor D 的回归测试：
// 第一次 onProgress 回调（goroutine 还一个都没起）里，所有目标都必须是
// TargetPending。改造前这里被直接初始化成 TargetRunning —— 一是在撒谎，
// 二是让 TargetPending / renderStatus 的 ⬜ 分支成了永远走不到的死代码。
func TestUploadSeedsPendingState(t *testing.T) {
	srv := httptest.NewServer((&davRecorder{}).handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "A", URL: srv.URL + "/dav"},
		{Name: "B", URL: srv.URL + "/dav"},
	}, "alist", "pw")

	var mu sync.Mutex
	var first []TargetStatus
	_, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), "a.mp3"), func(ts []TargetStatus) {
		mu.Lock()
		defer mu.Unlock()
		if first == nil {
			first = ts
		}
	})
	if err != nil {
		t.Fatalf("Upload 意外报错: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(first) != 2 {
		t.Fatalf("首次回调收到 %d 个目标状态, want 2", len(first))
	}
	for _, s := range first {
		if s.State != TargetPending {
			t.Errorf("目标 %s 的初始状态 = %d, want TargetPending（此刻还没有 goroutine 起来）", s.Name, s.State)
		}
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

	got, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), "a.mp3"), nil)
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

	got, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), "a.mp3"), nil)
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
	_, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), "a.mp3"), func(ts []TargetStatus) {
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

// TestUploadSingleTargetSucceeds 验证只有一个目标时上传正常。
// 这条路径在校验改成"至少一个完整目标"之前，配置层根本到不了。
func TestUploadSingleTargetSucceeds(t *testing.T) {
	rec := &davRecorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "阿里云盘", URL: srv.URL + "/dav/aliyun/Music"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "ID3fake"), "a.mp3"), nil)
	if err != nil {
		t.Fatalf("单目标上传意外失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 个目标状态, want 1", len(got))
	}
	if got[0].State != TargetOK {
		t.Errorf("状态 = %d, want TargetOK；err=%s", got[0].State, got[0].Err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) != 1 {
		t.Errorf("服务端收到 %d 个 PUT, want 1", len(rec.paths))
	}
}

// TestUploadSingleTargetFails 验证只有一个目标且它失败时，整体判定为失败。
// "至少一个成功即算成功"在只有一个目标时退化成"它必须成功"。
func TestUploadSingleTargetFails(t *testing.T) {
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusInsufficientStorage}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(badSrv.Client(), []Target{
		{Name: "阿里云盘", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), mp3Only(writeTempMP3(t, "x"), "a.mp3"), nil)
	if err == nil {
		t.Fatal("唯一的目标失败时应当返回 error")
	}
	if len(got) != 1 || got[0].State != TargetFailed {
		t.Fatalf("got = %+v, want 单个 TargetFailed", got)
	}
}

// TestBuildLyricFilenameMatchesMP3 是这次改动里最要紧的一条断言：
// .lrc 与 .mp3 去掉扩展名之后必须**逐字相等**，包括触发截断的超长输入。
//
// 为什么单独锁死：播放器是靠"同目录同名"把歌词配给音频的。一旦两个名字
// 的截断结果差一个字，歌词就永远配不上 —— 而且不会有任何报错，
// 用户只会觉得"下了歌词但没用"。
func TestBuildLyricFilenameMatchesMP3(t *testing.T) {
	cases := []struct{ artist, title string }{
		{"周杰伦", "晴天"},
		{"", ""},                          // 两个都空 → unknown 兜底
		{"A/B", "C:D"},                    // 危险字符要被同样清洗
		{strings.Repeat("周", 200), "晴天"},  // 超长，必然触发 120 rune 截断
		{"周杰伦", strings.Repeat("晴", 200)}, // 截断落在歌名一侧
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
