package music

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeFetcher 替代真实 agent，让 Runner 的编排逻辑能脱离 LLM 单测。
type fakeFetcher struct {
	track   *Track
	err     error
	gotDir  string // 记录 Runner 传进来的临时目录，用于断言清理行为
	gotStat *Status
}

func (f *fakeFetcher) Fetch(_ context.Context, st *Status, dir string, _ Reporter) (*Track, error) {
	f.gotDir = dir
	f.gotStat = st
	if f.err != nil {
		return nil, f.err
	}
	// 真 agent 会把文件落在 dir 里，这里也造一个，好验证 Runner 会清理它。
	if f.track != nil && f.track.LocalPath == "" {
		p := dir + string(os.PathSeparator) + tempFileName("fake", "1")
		if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
			return nil, err
		}
		f.track.LocalPath = p
	}
	st.Track = f.track
	return f.track, nil
}

// fakeUploader 替代真实 Uploader。
type fakeUploader struct {
	states   []TargetStatus
	err      error
	gotFiles []UploadFile // 记录 Runner 传进来的文件列表，用于断言组装逻辑
}

func (f *fakeUploader) Upload(_ context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	f.gotFiles = files
	if onProgress != nil {
		onProgress(f.states)
	}
	return f.states, f.err
}

// mp3Name 返回文件列表里那个 mp3 的文件名，没有则返回空串。
// 既有用例断言的是"上传文件名对不对"，用这个小助手迁移最省事。
func (f *fakeUploader) mp3Name() string {
	for _, x := range f.gotFiles {
		if !x.Optional {
			return x.Filename
		}
	}
	return ""
}

// fakeLyrics 替代真实 Lyrics，让 Runner 的编排逻辑能脱离网络单测。
type fakeLyrics struct {
	path    string
	status  LyricStatus
	gotDir  string
	gotName string
	// gotTrack 记下 Runner 实际传进来的 *Track，用于断言"Runner 有没有把
	// Fetch 返回的那个 Track 转手交给 Save"这一跳。这一跳此前没有任何
	// 断言守着（参数直接写成 `_ *Track` 丢弃），哪天这个实参被写错成
	// nil 或别的 Track，所有测试照样全绿，线上表现是每首歌都悄悄没歌词。
	gotTrack *Track
}

func (f *fakeLyrics) Save(_ context.Context, t *Track, dir, filename string) (string, LyricStatus) {
	f.gotTrack = t
	f.gotDir = dir
	f.gotName = filename
	return f.path, f.status
}

// newTestRunnerWithLyrics 组装注入了三个 fake 的 Runner。
func newTestRunnerWithLyrics(f *fakeFetcher, u *fakeUploader, l *fakeLyrics, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, lyrics: l, editor: e}
}

// newTestRunner 组装注入了 fake 的 Runner。
// lyrics 给一个"该音源不供词"的 fake：老用例都不关心歌词，
// 这个默认值让它们一个字都不用改。
func newTestRunner(f *fakeFetcher, u *fakeUploader, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, lyrics: &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}, editor: e}
}

// TestRunnerHappyPath：下载成功 + 上传成功 → 无错误，文件名用规范化后的歌手歌名，
// 临时目录被清理，终态被发出。
func TestRunnerHappyPath(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "周杰伦", Title: "晴天", Duration: "03:58", Bytes: 100, Source: "mp3pm"}}
	u := &fakeUploader{states: []TargetStatus{
		{Name: "阿里云盘", State: TargetOK},
		{Name: "OneDrive", State: TargetOK},
	}}
	fe := &fakeEditor{}

	if err := newTestRunner(f, u, fe).Run(context.Background(), 1, "晴天 周杰伦"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	if u.mp3Name() != "周杰伦 - 晴天.mp3" {
		t.Errorf("上传文件名 = %q, want %q", u.mp3Name(), "周杰伦 - 晴天.mp3")
	}
	// 临时目录必须被删掉，不能在系统临时目录里堆 mp3。
	if _, err := os.Stat(f.gotDir); !os.IsNotExist(err) {
		t.Errorf("临时目录 %q 未被清理", f.gotDir)
	}
	// 进度快照的 Query 必须是用户原始输入。
	if f.gotStat.Query != "晴天 周杰伦" {
		t.Errorf("Status.Query = %q, want %q", f.gotStat.Query, "晴天 周杰伦")
	}
	// 至少发过一次消息（首发）+ 终态编辑。
	sends, edits := fe.counts()
	if sends != 1 || edits < 1 {
		t.Errorf("SendEditable=%d Edit=%d，期望 1 次首发 + 至少 1 次编辑", sends, edits)
	}
}

// TestRunnerFetchFailure：agent 失败 → 返回错误，终态写明原因，临时目录仍被清理。
func TestRunnerFetchFailure(t *testing.T) {
	f := &fakeFetcher{err: errors.New("模型未收敛")}
	u := &fakeUploader{}
	fe := &fakeEditor{}

	err := newTestRunner(f, u, fe).Run(context.Background(), 1, "不存在的歌")
	if err == nil {
		t.Fatal("agent 失败时 Run 应返回错误")
	}
	if len(u.gotFiles) != 0 {
		t.Error("agent 失败后不该再尝试上传")
	}
	if _, sErr := os.Stat(f.gotDir); !os.IsNotExist(sErr) {
		t.Errorf("失败路径下临时目录 %q 也必须被清理", f.gotDir)
	}
	// 终态消息里要能看到失败原因。
	fe.mu.Lock()
	defer fe.mu.Unlock()
	all := strings.Join(append(append([]string{}, fe.sends...), fe.edits...), "\n")
	if !strings.Contains(all, "模型未收敛") {
		t.Errorf("终态消息里应包含失败原因，实际:\n%s", all)
	}
}

// TestRunnerUploadAllFailed：上传全失败 → 返回错误。
func TestRunnerUploadAllFailed(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T"}}
	u := &fakeUploader{
		states: []TargetStatus{{Name: "A", State: TargetFailed, Err: "401"}},
		err:    errors.New("全部 1 个 WebDAV 目标上传失败"),
	}

	if err := newTestRunner(f, u, &fakeEditor{}).Run(context.Background(), 1, "x"); err == nil {
		t.Fatal("上传全失败时 Run 应返回错误")
	}
}

// TestRunnerUploadPartialFailedStillSucceeds：一成一败 → Run 不报错。
// 这是「不为一个网盘挂掉丢弃已下好的歌」在编排层的兑现。
func TestRunnerUploadPartialFailedStillSucceeds(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T"}}
	u := &fakeUploader{states: []TargetStatus{
		{Name: "A", State: TargetOK},
		{Name: "B", State: TargetFailed, Err: "507"},
	}} // err 为 nil：Uploader 的语义是"至少一个成功就不报错"

	if err := newTestRunner(f, u, &fakeEditor{}).Run(context.Background(), 1, "x"); err != nil {
		t.Fatalf("一成一败时 Run 不该报错: %v", err)
	}
}

// TestRunnerConcurrentUploadNoRace 是 code review 补的回归测试：上面四个用例
// 都用 fakeUploader，它同步调一次 onProgress，完全绕开了真实 *Uploader 里
// "每个目标一个 goroutine、onProgress 在这些 goroutine 里并发触发"这条路径，
// 所以测不出 Runner 回调里并发读写 st.Targets 的数据竞争。
//
// 这里换成真实的 *Uploader 打真实的 httptest 服务端，至少 3 个目标、
// 服务端故意加一点延迟让完成时间靠得更近，并重复跑几轮，
// 让 `go test -race` 有足够高的概率真正撞上并发窗口。
//
// 判别力已人工验证：把 runner.go 里 onProgress 回调的 stMu.Lock/Unlock
// 临时去掉、snapshot 直接用 *st 后重跑本测试 + -race，会稳定报出
// "DATA RACE" 且落点正是 st.Targets 的读写（runner.go 的回调那两行）；
// 恢复加锁后同样的命令稳定通过。修复报告里贴了两次的完整输出。
func TestRunnerConcurrentUploadNoRace(t *testing.T) {
	// 服务端每次请求都象征性停一下：把三次 PUT 的完成时间拉近，
	// 提高 onProgress 被多个 goroutine 同时调用的概率。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)

	targets := []Target{
		{Name: "阿里云盘", URL: srv.URL + "/dav/a"},
		{Name: "OneDrive", URL: srv.URL + "/dav/b"},
		{Name: "第三方", URL: srv.URL + "/dav/c"},
	}

	// 跑几轮：单轮 3 个目标的并发窗口未必每次都被调度器实际交叉执行到，
	// 多轮能显著提高 -race 检测到真实交叠的概率。
	for i := 0; i < 5; i++ {
		up := NewUploader(srv.Client(), targets, "alist", "pw")
		f := &fakeFetcher{track: &Track{Artist: "A", Title: "T", Bytes: 7}}
		fe := &fakeEditor{}
		r := &Runner{fetcher: f, uploader: up, lyrics: &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}, editor: fe}

		if err := r.Run(context.Background(), 1, "x"); err != nil {
			t.Fatalf("第 %d 轮 Run 意外报错: %v", i, err)
		}
	}
}

// TestRunnerUploadsLyricAsOptional：有歌词时，上传列表是 mp3 + lrc 两个文件，
// 且 lrc 必须标成 Optional —— 否则它传失败会把整个目标判成失败。
func TestRunnerUploadsLyricAsOptional(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "周杰伦", Title: "晴天", Bytes: 100, Source: "musicso"}}
	u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
	l := &fakeLyrics{path: "/tmp/x.lrc", status: LyricStatus{State: LyricOK}}

	if err := newTestRunnerWithLyrics(f, u, l, &fakeEditor{}).Run(context.Background(), 1, "晴天"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	if len(u.gotFiles) != 2 {
		t.Fatalf("上传文件数 = %d, want 2（mp3 + lrc）: %+v", len(u.gotFiles), u.gotFiles)
	}
	// mp3 必须排在前面：ctx 中途断掉时先落地的应该是要紧那个。
	if u.gotFiles[0].Filename != "周杰伦 - 晴天.mp3" || u.gotFiles[0].Optional {
		t.Errorf("第一个文件 = %+v, want 必传的 mp3", u.gotFiles[0])
	}
	if u.gotFiles[1].Filename != "周杰伦 - 晴天.lrc" {
		t.Errorf("第二个文件名 = %q, want 周杰伦 - 晴天.lrc", u.gotFiles[1].Filename)
	}
	if !u.gotFiles[1].Optional {
		t.Error("歌词必须标成 Optional，否则它传失败会把整个目标判成失败")
	}
	if u.gotFiles[1].ContentType != "text/plain; charset=utf-8" {
		t.Errorf("歌词 Content-Type = %q, want text/plain; charset=utf-8", u.gotFiles[1].ContentType)
	}
	// 取词必须发生在 agent 的临时目录里，才会被 defer os.RemoveAll 清掉。
	if l.gotDir != f.gotDir {
		t.Errorf("取词目录 = %q, want 与下载同一个临时目录 %q", l.gotDir, f.gotDir)
	}
	if l.gotName != "周杰伦 - 晴天.lrc" {
		t.Errorf("传给 Save 的文件名 = %q, want 周杰伦 - 晴天.lrc", l.gotName)
	}
	// 核心断言：Runner 传给 Save 的 *Track 必须就是 Fetch 返回的那一个
	// （也就是 f.track，fakeFetcher 里那个）。这一跳此前完全没有断言，
	// 万一 Runner 里传错成 nil 或另造一个 Track，取词凭据（src/cand）
	// 就全丢了，但因为 Save 从不返回 error，测试和线上都不会有任何报错，
	// 只会表现成"每首歌都悄悄没有歌词"。
	if l.gotTrack != f.track {
		t.Errorf("传给 Save 的 Track = %p, want Fetch 返回的那个 Track %p", l.gotTrack, f.track)
	}
}

// TestRunnerNoLyricStillUploads：没有歌词时只传 mp3，任务照样成功。
// 这是"歌词的任何问题都不能让一首已经下好的歌传不上去"在编排层的兑现。
func TestRunnerNoLyricStillUploads(t *testing.T) {
	for _, st := range []LyricStatus{
		{State: LyricUnsupported},
		{State: LyricMissing},
		{State: LyricFailed, Err: "被 Cloudflare 拦截"},
	} {
		f := &fakeFetcher{track: &Track{Artist: "A", Title: "T", Source: "mp3pm"}}
		u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
		l := &fakeLyrics{path: "", status: st}

		if err := newTestRunnerWithLyrics(f, u, l, &fakeEditor{}).Run(context.Background(), 1, "x"); err != nil {
			t.Fatalf("歌词状态 %v 时 Run 不该报错: %v", st.State, err)
		}
		if len(u.gotFiles) != 1 {
			t.Errorf("歌词状态 %v 时上传文件数 = %d, want 1", st.State, len(u.gotFiles))
		}
	}
}

// TestRunnerReportsLyricStatus：歌词状态必须进快照，用户才看得到。
func TestRunnerReportsLyricStatus(t *testing.T) {
	f := &fakeFetcher{track: &Track{Artist: "A", Title: "T", Source: "mp3pm"}}
	u := &fakeUploader{states: []TargetStatus{{Name: "阿里云盘", State: TargetOK}}}
	l := &fakeLyrics{status: LyricStatus{State: LyricUnsupported}}
	fe := &fakeEditor{}

	if err := newTestRunnerWithLyrics(f, u, l, fe).Run(context.Background(), 1, "x"); err != nil {
		t.Fatalf("Run 意外报错: %v", err)
	}

	fe.mu.Lock()
	defer fe.mu.Unlock()
	all := strings.Join(append(append([]string{}, fe.sends...), fe.edits...), "\n")
	if !strings.Contains(all, "mp3pm 不提供") {
		t.Errorf("终态消息里应说明歌词为何没有，实际:\n%s", all)
	}
}

// TestNewRunnerWiresDefaultLyrics 验证：NewRunner 装配了默认的 Lyrics，
// 使用方（main）不必知道歌词的存在，也就不用改一行接线代码。
func TestNewRunnerWiresDefaultLyrics(t *testing.T) {
	r := NewRunner(nil, nil, nil)
	if r.lyrics == nil {
		t.Fatal("NewRunner 必须填上默认的 Lyrics，否则 Run 会空指针")
	}
}
