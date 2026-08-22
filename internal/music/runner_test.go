package music

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
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
		p := dir + string(os.PathSeparator) + downloadFileName
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
	gotFile  string // 记录 Runner 传进来的文件名，用于断言 BuildFilename 被用上
	gotLocal string
}

func (f *fakeUploader) Upload(_ context.Context, localPath, filename string, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	f.gotLocal = localPath
	f.gotFile = filename
	if onProgress != nil {
		onProgress(f.states)
	}
	return f.states, f.err
}

// newTestRunner 组装注入了 fake 的 Runner。
func newTestRunner(f *fakeFetcher, u *fakeUploader, e *fakeEditor) *Runner {
	return &Runner{fetcher: f, uploader: u, editor: e}
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

	if u.gotFile != "周杰伦 - 晴天.mp3" {
		t.Errorf("上传文件名 = %q, want %q", u.gotFile, "周杰伦 - 晴天.mp3")
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
	if u.gotFile != "" {
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
