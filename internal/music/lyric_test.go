package music

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// newLyricTrack 造一个已下载好的 Track，src 指向给定的假音源。
//
// 假音源直接复用 agent_test.go 里的 fakeSource（它已经有 lyric/lyricErr
// 两个字段，&fakeSource{lyric: ...} 就够表达本文件所有用例），不再另开
// 一份 lyricSource —— 同一个 Source 接口在同一个包里重复实现两份纯属浪费，
// 且改起来要同步改两处。
func newLyricTrack(src Source) *Track {
	return &Track{Artist: "周杰伦", Title: "晴天", Source: "fakelyric", src: src, cand: Candidate{ID: "1"}}
}

// TestLyricsSaveOK：拿到歌词 → 落盘，路径与内容都对。
func TestLyricsSaveOK(t *testing.T) {
	const lrc = "[ti:晴天]\n[00:00.00]晴天 - 周杰伦"
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&fakeSource{lyric: lrc}), dir, "周杰伦 - 晴天.lrc")

	if st.State != LyricOK {
		t.Fatalf("State = %v, want LyricOK", st.State)
	}
	if want := filepath.Join(dir, "周杰伦 - 晴天.lrc"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读歌词文件失败: %v", err)
	}
	// 内容必须一字不动地落盘：时间戳和换行都是播放器要用的。
	if string(b) != lrc {
		t.Errorf("落盘内容 = %q, want %q", b, lrc)
	}
}

// TestLyricsSaveUnsupported：音源不供词 → LyricUnsupported，不落盘。
func TestLyricsSaveUnsupported(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&fakeSource{lyricErr: errLyricUnsupported}), dir, "a.lrc")

	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported", st.State)
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveUnsupportedWrapped 验证：哨兵被 %w 包过一层照样认得出来。
//
// 各音源在自己的错误里包一层说明是本仓库的常规做法（见 musicSoChallenge
// 对 errSourceUnavailable 的用法），所以判别必须走 errors.Is 而不是 ==。
func TestLyricsSaveUnsupportedWrapped(t *testing.T) {
	src := &fakeSource{lyricErr: fmt.Errorf("mp3.pm 的页面里没有歌词: %w", errLyricUnsupported)}

	_, st := Lyrics{}.Save(context.Background(), newLyricTrack(src), t.TempDir(), "a.lrc")
	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported（哨兵被包装后仍应认出）", st.State)
	}
}

// TestLyricsSaveMissing：音源供词但返回空 → LyricMissing，不落盘。
//
// 判别力：如果实现把空歌词也写出去，网盘里会多一个 0 字节的 .lrc，
// 播放器认出它、显示一片空白，比没有歌词更糟。
func TestLyricsSaveMissing(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&fakeSource{lyric: ""}), dir, "a.lrc")

	if st.State != LyricMissing {
		t.Fatalf("State = %v, want LyricMissing", st.State)
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveBlankIsMissing：只有空白字符同样算未收录，不落盘。
func TestLyricsSaveBlankIsMissing(t *testing.T) {
	dir := t.TempDir()

	_, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&fakeSource{lyric: "   \n\t"}), dir, "a.lrc")

	if st.State != LyricMissing {
		t.Fatalf("State = %v, want LyricMissing", st.State)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveFailed：取词报错 → LyricFailed 且带原因，不落盘。
func TestLyricsSaveFailed(t *testing.T) {
	dir := t.TempDir()

	path, st := Lyrics{}.Save(context.Background(),
		newLyricTrack(&fakeSource{lyricErr: errors.New("被 Cloudflare 拦截")}), dir, "a.lrc")

	if st.State != LyricFailed {
		t.Fatalf("State = %v, want LyricFailed", st.State)
	}
	if st.Err == "" {
		t.Error("LyricFailed 必须带上失败原因，否则进度消息说不出病根")
	}
	if path != "" {
		t.Errorf("path = %q, want 空串", path)
	}
	assertDirEmpty(t, dir)
}

// TestLyricsSaveNilSource：防御性路径 —— src 为 nil 时按"不供词"处理，不 panic。
func TestLyricsSaveNilSource(t *testing.T) {
	_, st := Lyrics{}.Save(context.Background(), &Track{Artist: "A", Title: "T"}, t.TempDir(), "a.lrc")
	if st.State != LyricUnsupported {
		t.Fatalf("State = %v, want LyricUnsupported", st.State)
	}

	// Track 本身为 nil 也不能炸。
	//
	// 这里用 (Lyrics{}) 加括号：Lyrics{} 若直接出现在 if 的 init 子句里，
	// Go 语法会把紧随其后的 { 当成 if 语句体的开始（跟 "if T{}.M() {" 这种
	// 经典歧义一样），加括号消掉这层歧义，跟本文件其它地方裸写 Lyrics{}.Save(...)
	// 不冲突——那些都是独立语句，不在 if 的头部。
	if _, st2 := (Lyrics{}).Save(context.Background(), nil, t.TempDir(), "a.lrc"); st2.State != LyricUnsupported {
		t.Fatalf("nil Track 时 State = %v, want LyricUnsupported", st2.State)
	}
}

// assertDirEmpty 断言目录里一个文件都没有。
// 非 LyricOK 的每条路径都必须做到这一点：临时目录随后会被整个上传流程
// 扫一遍，留下半成品只会让一个残缺的 .lrc 被传上网盘。
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("目录里不该有文件，实际有 %d 个: %v", len(entries), entries)
	}
}
