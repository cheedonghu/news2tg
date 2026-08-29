package music

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// fakeLLM 按预设脚本逐轮返回响应，不发任何网络请求。
// scripts[i] 是第 i 轮 CreateChatCompletion 的返回。
type fakeLLM struct {
	scripts []openai.ChatCompletionResponse
	calls   int
	err     error // 非 nil 时直接返回错误
}

func (f *fakeLLM) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if f.err != nil {
		return openai.ChatCompletionResponse{}, f.err
	}
	if f.calls >= len(f.scripts) {
		return openai.ChatCompletionResponse{}, errors.New("fakeLLM 脚本用尽")
	}
	resp := f.scripts[f.calls]
	f.calls++
	return resp, nil
}

// toolCallResp 造一个"模型要求调工具"的响应。
func toolCallResp(id, name, args string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{{
				ID:       id,
				Type:     openai.ToolTypeFunction,
				Function: openai.FunctionCall{Name: name, Arguments: args},
			}},
		}}},
		Usage: openai.Usage{TotalTokens: 100},
	}
}

// finalResp 造一个"模型给出最终回答"的响应。
func finalResp(content string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleAssistant, Content: content,
		}}},
		Usage: openai.Usage{TotalTokens: 50},
	}
}

// fakeSource 是可编程的假音源。
type fakeSource struct {
	name      string
	hint      string                 // 空则回落到默认文案；提示词测试要靠它区分不同音源
	results   map[string][]Candidate // 关键词 → 候选；未命中的关键词返回零结果
	searchErr error
	payload   string // Download 写出的内容
	dlErr     error
	dlCalls   []string // 记录被下载的 id
	lyric     string   // Lyric 返回的歌词；空串表示"站点未收录"
	lyricErr  error    // 非 nil 时 Lyric 返回它
}

func (s *fakeSource) Name() string { return s.name }

// Hint 默认返回一句占位文案，这样现有那十几个用例一个字都不用改；
// 只有关心"各源 Hint 是否分别进了提示词"的用例才需要显式设置 hint。
func (s *fakeSource) Hint() string {
	if s.hint != "" {
		return s.hint
	}
	return "测试音源"
}

func (s *fakeSource) Search(_ context.Context, query string) ([]Candidate, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.results[query], nil
}

func (s *fakeSource) Download(_ context.Context, c Candidate, w io.Writer) (int64, error) {
	s.dlCalls = append(s.dlCalls, c.ID)
	if s.dlErr != nil {
		return 0, s.dlErr
	}
	n, err := io.WriteString(w, s.payload)
	return int64(n), err
}

// Lyric 默认返回 ("", nil)，即"该源供词但这首没收录" —— 现有那十几个
// 用例都不关心歌词，这个默认值让它们一个字都不用改。
func (s *fakeSource) Lyric(_ context.Context, _ Candidate) (string, error) {
	return s.lyric, s.lyricErr
}

// nopReporter 是不做任何事的 Reporter，用于不关心进度的用例。
type nopReporter struct{ snapshots []Status }

func (r *nopReporter) Update(_ context.Context, s Status) { r.snapshots = append(r.snapshots, s) }
func (r *nopReporter) Done(_ context.Context, s Status)   { r.snapshots = append(r.snapshots, s) }

// newTestAgent 组装一个注入了 fake 的 agent。
func newTestAgent(t *testing.T, llm chatCompleter, src Source) *Agent {
	t.Helper()
	a := NewAgent("test-key", "test-model", []Source{src})
	a.llm = llm // 覆盖掉真实客户端，绝不发网络请求
	return a
}

// TestAgentHappyPath：搜一次就搜中，下载，输出 JSON。
func TestAgentHappyPath(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "ID3fake-bytes",
		results: map[string][]Candidate{
			"Jay Chou Qing Tian": {{ID: "1", Artist: "Jay Chou", Title: "Qing Tian", Duration: "03:58", dlURL: "u"}},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"Jay Chou Qing Tian"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	st := &Status{Query: "晴天 周杰伦"}
	rep := &nopReporter{}

	track, err := a.Fetch(context.Background(), st, t.TempDir(), rep)
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}
	if track.Artist != "周杰伦" || track.Title != "晴天" {
		t.Errorf("规范化结果 = %q - %q, want 周杰伦 - 晴天", track.Artist, track.Title)
	}
	if track.Duration != "03:58" {
		t.Errorf("Duration = %q, want 03:58", track.Duration)
	}
	if track.Bytes != int64(len(src.payload)) {
		t.Errorf("Bytes = %d, want %d", track.Bytes, len(src.payload))
	}
	// token 是逐轮累加的：100 + 100 + 50。
	if track.Tokens != 250 {
		t.Errorf("Tokens = %d, want 250", track.Tokens)
	}
	// 文件真的落盘了吗？
	b, rErr := os.ReadFile(track.LocalPath)
	if rErr != nil {
		t.Fatalf("读下载文件失败: %v", rErr)
	}
	if string(b) != src.payload {
		t.Errorf("落盘内容 = %q, want %q", b, src.payload)
	}
	if st.Searches != 1 {
		t.Errorf("st.Searches = %d, want 1", st.Searches)
	}
}

// TestAgentRetriesAfterZeroResults：第一次搜中文零结果，模型换成拼音再搜就中。
// 这是 mp3.pm 搜中文歌的常规路径，必须覆盖。
func TestAgentRetriesAfterZeroResults(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "bytes",
		results: map[string][]Candidate{
			// 只有拼音关键词有结果；"周杰伦 晴天" 命不中 → 零结果
			"Jay Chou Qing Tian": {{ID: "7", Artist: "Jay Chou", Title: "Qing Tian", Duration: "03:58", dlURL: "u"}},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"周杰伦 晴天"}`),
		toolCallResp("c2", "search_fake", `{"query":"Jay Chou Qing Tian"}`),
		toolCallResp("c3", "download_fake", `{"id":"7"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"7"}`),
	}}

	a := newTestAgent(t, llm, src)
	st := &Status{Query: "晴天"}
	track, err := a.Fetch(context.Background(), st, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}
	if track.Title != "晴天" {
		t.Errorf("Title = %q, want 晴天", track.Title)
	}
	if st.Searches != 2 {
		t.Errorf("st.Searches = %d, want 2（换词重搜过一次）", st.Searches)
	}
}

// TestAgentDownloadFailureThenAnotherCandidate：下载失败后模型改选另一个候选。
func TestAgentDownloadFailureThenAnotherCandidate(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		payload: "bytes",
		dlErr:   errors.New("403 forbidden"),
		results: map[string][]Candidate{
			"q": {
				{ID: "1", Artist: "A", Title: "T1", Duration: "01:00", dlURL: "u1"},
				{ID: "2", Artist: "A", Title: "T2", Duration: "02:00", dlURL: "u2"},
			},
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`), // 失败
		toolCallResp("c3", "download_fake", `{"id":"2"}`), // 仍失败（dlErr 是常驻的）
		finalResp(`{"artist":"A","title":"T2","id":"2"}`),
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	// 两次下载都失败 → 最终没有 track，必须报错而不是拿着空文件继续。
	if err == nil {
		t.Fatal("下载全失败时应报错")
	}
	// 关键断言：失败被回灌给模型了，所以它有机会尝试第二个候选。
	if len(src.dlCalls) != 2 {
		t.Errorf("下载被调用 %d 次, want 2（第一次失败后模型应能改选）", len(src.dlCalls))
	}
}

// TestAgentTooLarge：超过 50MB 上限时中断下载并删掉半成品。
func TestAgentTooLarge(t *testing.T) {
	// payload 比上限大一点点即可触发；不用真造 50MB，把 agent 的上限压小。
	src := &fakeSource{
		name:    "fake",
		payload: strings.Repeat("x", 1024),
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	a.maxBytes = 100 // 压到 100 字节，payload 1024 字节必定超限

	dir := t.TempDir()
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, dir, &nopReporter{})
	if err == nil {
		t.Fatal("下载超限后没有可用文件，应报错")
	}
	// 半成品必须被删掉，不能在临时目录里留个残缺 mp3。
	entries, rErr := os.ReadDir(dir)
	if rErr != nil {
		t.Fatalf("读临时目录失败: %v", rErr)
	}
	if len(entries) != 0 {
		t.Errorf("临时目录应为空（半成品被删），实际有 %d 个文件", len(entries))
	}
}

// TestAgentNonJSONFinal：模型最终输出不是 JSON → 任务失败，错误里带上原始输出。
func TestAgentNonJSONFinal(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp("我下载好了，这首歌很好听！"),
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("非 JSON 输出应报错")
	}
	if !strings.Contains(err.Error(), "很好听") {
		t.Errorf("错误信息应带上模型原始输出便于排查，实际: %v", err)
	}
}

// TestAgentNoDownloadBeforeFinal：模型没下载就给结论 → 失败（没有文件可传）。
func TestAgentNoDownloadBeforeFinal(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	if _, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{}); err == nil {
		t.Fatal("没调下载工具就给结论时应报错")
	}
}

// TestAgentIDMismatch：最终 JSON 的 id 与实际下载的不一致 → 失败。
// 模型自相矛盾时，文件名与文件内容大概率对不上，宁可失败也不要传错东西。
func TestAgentIDMismatch(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {
			{ID: "1", Artist: "A", Title: "T1", dlURL: "u1"},
			{ID: "2", Artist: "A", Title: "T2", dlURL: "u2"},
		}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"A","title":"T2","id":"2"}`), // 说下了 2，实际下的是 1
	}}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("id 不一致时应报错")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Errorf("错误信息应说明是 id 不一致，实际: %v", err)
	}
}

// TestAgentMaxStepsExhausted：模型反复调工具不收敛 → 报"未收敛"。
func TestAgentMaxStepsExhausted(t *testing.T) {
	src := &fakeSource{
		name:    "fake",
		results: map[string][]Candidate{},
	}
	// 每轮都只搜索、从不收敛；脚本给足 maxSteps 轮。
	scripts := make([]openai.ChatCompletionResponse, defaultMaxSteps)
	for i := range scripts {
		scripts[i] = toolCallResp("c", "search_fake", `{"query":"q"}`)
	}
	llm := &fakeLLM{scripts: scripts}

	a := newTestAgent(t, llm, src)
	_, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err == nil {
		t.Fatal("超过最大步数时应报错")
	}
	if !strings.Contains(err.Error(), "未收敛") {
		t.Errorf("错误信息应说明未收敛，实际: %v", err)
	}
}

// TestAgentUnknownCandidateID：模型编了个不存在的 id → 回灌提示而非崩溃。
func TestAgentUnknownCandidateID(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"999"}`), // 编的
		toolCallResp("c3", "download_fake", `{"id":"1"}`),   // 改正
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	track, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("模型自我纠正后应成功: %v", err)
	}
	if track.Title != "T" {
		t.Errorf("Title = %q, want T", track.Title)
	}
	// 只有合法的那次真正走到了音源。
	if len(src.dlCalls) != 1 {
		t.Errorf("下载被调用 %d 次, want 1（不存在的 id 不该打到音源）", len(src.dlCalls))
	}
}

// slowFinalLLM 在**最终那一轮**（脚本的最后一条）返回前先睡一会儿。
//
// 目的：把 doDownload 之后、finalize 之前的那段窗口撑开，让 Reporter 的节流
// 补发（time.AfterFunc 的 goroutine）稳定地落在这中间 —— 也就是让 flush 里
// 的 renderStatus 去读那个刚被塞进快照的 *Track，随后 finalize 再去写它。
type slowFinalLLM struct {
	scripts []openai.ChatCompletionResponse
	calls   int
	delay   time.Duration
}

func (f *slowFinalLLM) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if f.calls >= len(f.scripts) {
		return openai.ChatCompletionResponse{}, errors.New("slowFinalLLM 脚本用尽")
	}
	resp := f.scripts[f.calls]
	f.calls++
	if f.calls == len(f.scripts) {
		time.Sleep(f.delay)
	}
	return resp, nil
}

// TestAgentFinalizeNoRaceWithThrottledFlush 是补给 Critical 1 的回归测试。
//
// 缺陷：Status 按值传给 Reporter，但 Status.Track 是**指针**。doDownload 把
// r.track 塞进快照后，节流会把这份快照存进 pending；finalize 随后原地改同一个
// Track 的 Artist/Title/Tokens，而 flush 正在 time.AfterFunc 的 goroutine 里
// 通过 renderStatus 读它 —— 两个 goroutine、同一块内存、无任何同步。
// 字符串是 ptr+len 两个字，撕裂读可能让 renderStatus 拿到越界长度直接 panic，
// 而 /music 的 goroutine 是 detached 的、没有 recover，等于把进程干掉。
//
// 为什么现有测试测不到：agent_test 一律用 nopReporter（根本没有第二个
// goroutine），report_test 造的 Status 又没人去改它的 Track。必须把**真的
// tgReporter** 接到**真的 Agent.Fetch** 后面，才能撞上这条缝。
//
// 判别力（须在 -race 下跑）：把 finalize 换回原来的
// `r.track.Artist = ...; r.track.Title = ...; r.track.Tokens = ...`，
// 本用例稳定报 DATA RACE（写在 agent.go 的 finalize，读在 report.go 的
// renderStatus）；换成造新 Track 的写法后稳定通过。
func TestAgentFinalizeNoRaceWithThrottledFlush(t *testing.T) {
	// 多跑几轮：单轮未必每次都让调度器把 flush 排到 finalize 之前。
	for round := 0; round < 3; round++ {
		src := &fakeSource{
			name:    "fake",
			payload: "ID3fake-bytes",
			results: map[string][]Candidate{
				"q": {{ID: "1", Artist: "Jay Chou", Title: "Qing Tian", Duration: "03:58", dlURL: "u"}},
			},
		}
		// 最后一轮（finalResp）之前睡 120ms：节流窗口只有 5ms，
		// 补发必定在这 120ms 里发生，也就必定早于 finalize。
		llm := &slowFinalLLM{
			delay: 120 * time.Millisecond,
			scripts: []openai.ChatCompletionResponse{
				toolCallResp("c1", "search_fake", `{"query":"q"}`),
				toolCallResp("c2", "download_fake", `{"id":"1"}`),
				finalResp(`{"artist":"周杰伦","title":"晴天","id":"1"}`),
			},
		}

		a := newTestAgent(t, llm, src)
		// 真 tgReporter + 极短节流：让 flush 在另一个 goroutine 里真的跑起来。
		rep := newTGReporter(&fakeEditor{}, 1, 5*time.Millisecond)

		track, err := a.Fetch(context.Background(), &Status{Query: "晴天"}, t.TempDir(), rep)
		if err != nil {
			t.Fatalf("round %d: Fetch 意外报错: %v", round, err)
		}
		if track.Artist != "周杰伦" || track.Title != "晴天" {
			t.Errorf("round %d: 规范化结果 = %q - %q, want 周杰伦 - 晴天", round, track.Artist, track.Title)
		}
		// 再多等一个节流窗口，让可能还在路上的补发也跑完 —— 否则
		// -race 可能在竞争真正发生之前就随测试结束而收工。
		time.Sleep(30 * time.Millisecond)
	}
}

// TestAgentFinalizeDoesNotMutatePublishedTrack 从另一个角度锁死同一条不变量：
// finalize 返回的必须是一个**新** Track，已经发布进快照的那个不能被改动。
// 上面那个用例靠 -race 才能发现问题，这个用例不依赖竞态检测器，任何时候都成立。
func TestAgentFinalizeDoesNotMutatePublishedTrack(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "bytes",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "Jay Chou", Title: "Qing Tian", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"周杰伦","title":"晴天","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	rep := &nopReporter{}
	track, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), rep)
	if err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}

	// 找到下载完成那一刻发布出去的快照（Bytes 已经有值的那个），
	// 它引用的 Track 必须还保留站点原始名，没被 finalize 改成规范化后的名字。
	var published *Track
	for _, s := range rep.snapshots {
		if s.Track != nil && s.Track.Bytes > 0 {
			published = s.Track
		}
	}
	if published == nil {
		t.Fatal("没有捕获到下载完成后的快照")
	}
	if published == track {
		t.Fatal("finalize 返回的必须是新 Track，不能是已经发布进快照的那个指针")
	}
	if published.Artist != "Jay Chou" || published.Title != "Qing Tian" {
		t.Errorf("已发布快照里的 Track 被原地改动了: %q - %q", published.Artist, published.Title)
	}
}

// TestAgentFailedDownloadKeepsEarlierSuccess 是补给 Important 2 的回归测试。
//
// 缺陷：所有下载尝试都写同一个 download.mp3，失败分支又无条件 os.Remove，
// 于是"下载 1 成功 → 模型改主意去下 2 → 2 失败"这条**设计里明确支持的路径**
// （spec：下载失败 → 回灌，模型可改选另一个候选）会把 1 那个已经下好的文件
// 一并删掉。而 r.track / r.downloadedID 不失效，模型退回去输出 {"id":"1"} 时
// finalize 的每一道校验都能过，返回一个 LocalPath 已不存在的 Track，
// 两个 WebDAV 目标各自 os.Open 失败，用户最终看到的是
// "全部 2 个 WebDAV 目标上传失败" —— 把 agent 层的 bug 甩锅给网络。
//
// 判别力：修复前 Fetch 会成功返回，但 os.Stat(track.LocalPath) 报文件不存在，
// 下面的断言直接失败。
func TestAgentFailedDownloadKeepsEarlierSuccess(t *testing.T) {
	// 第二次下载失败：靠 dlCalls 的长度区分是第几次调用。
	src := &failSecondSource{fakeSource: fakeSource{
		name:    "fake",
		payload: "ID3-good-bytes",
		results: map[string][]Candidate{"q": {
			{ID: "1", Artist: "A", Title: "T1", Duration: "01:00", dlURL: "u1"},
			{ID: "2", Artist: "A", Title: "T2", Duration: "02:00", dlURL: "u2"},
		}},
	}}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`), // 成功
		toolCallResp("c3", "download_fake", `{"id":"2"}`), // 失败
		finalResp(`{"artist":"A","title":"T1","id":"1"}`), // 退回第一首
	}}

	a := newTestAgent(t, llm, src)
	track, err := a.Fetch(context.Background(), &Status{Query: "x"}, t.TempDir(), &nopReporter{})
	if err != nil {
		t.Fatalf("退回第一次成功下载的候选时不该报错: %v", err)
	}
	// 核心断言：第一次下好的文件必须还在，而且内容完整。
	b, rErr := os.ReadFile(track.LocalPath)
	if rErr != nil {
		t.Fatalf("第一次成功下载的文件被第二次失败的尝试删掉了: %v", rErr)
	}
	if string(b) != src.payload {
		t.Errorf("文件内容 = %q, want %q", b, src.payload)
	}
}

// failSecondSource 是"第一次下载成功、第二次失败"的音源。
type failSecondSource struct {
	fakeSource
}

func (s *failSecondSource) Download(ctx context.Context, c Candidate, w io.Writer) (int64, error) {
	s.dlCalls = append(s.dlCalls, c.ID)
	if len(s.dlCalls) >= 2 {
		// 写一半再失败，模拟传输中断 —— 半成品必须被删掉，
		// 但只能删它自己那个。
		_, _ = io.WriteString(w, "half")
		return 0, errors.New("连接中断")
	}
	n, err := io.WriteString(w, s.payload)
	return int64(n), err
}

// TestFinalizeRejectsMissingFile 锁死 finalize 的 os.Stat 兜底：
// 已下载的文件若不在了，必须在这里失败并说清楚原因，而不是把一个指向
// 空气的 Track 传下去、让上传层报出"全部 N 个目标上传失败"这种假线索。
//
// 直接构造 run 而不走完整 Fetch：文件名按候选区分之后，很难再从外部
// 自然地制造出"文件不见了"的状态（外部删除、磁盘故障仍然可能），
// 但这道防线本身必须有测试守着。
func TestFinalizeRejectsMissingFile(t *testing.T) {
	r := &run{
		track:        &Track{LocalPath: filepath.Join(t.TempDir(), "根本不存在.mp3")},
		downloadedID: "1",
	}
	_, err := r.finalize(`{"artist":"A","title":"T","id":"1"}`, 10)
	if err == nil {
		t.Fatal("文件不存在时 finalize 必须报错")
	}
	if !strings.Contains(err.Error(), "不可用") {
		t.Errorf("错误信息应说明是文件不可用（而不是上传失败），实际: %v", err)
	}
}

// TestAgentDoesNotResendFirstFrame 是补给 Minor B 的回归测试。
//
// 缺陷：Runner 已经用同一份 st 发过首帧了，Fetch 进来又发一遍
// （st.Stage = StageSearching + rep.Update），内容一字不差。第二帧必然落进
// 节流窗口存成 pending；只要第一轮模型调用超过 3s（很常见），补发就会送出
// 一次内容完全没变的 Edit —— Telegram 直接回 "message is not modified"，
// 记一条错误日志，还白占一次 1.5s 的全局发送锁，每次 /music 都要来一遍。
//
// 断言 Fetch 报出的第一帧必须**已经带上搜索计数**（也就是来自 doSearch，
// 而不是那帧跟 Runner 重复的空状态）。
func TestAgentDoesNotResendFirstFrame(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{"q": {{ID: "1", Artist: "A", Title: "T", dlURL: "u"}}},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"q"}`),
		toolCallResp("c2", "download_fake", `{"id":"1"}`),
		finalResp(`{"artist":"A","title":"T","id":"1"}`),
	}}

	a := newTestAgent(t, llm, src)
	rep := &nopReporter{}
	// st 就是 Runner 交过来的那一份：Runner 已经拿它发过首帧了。
	st := &Status{Query: "x", Stage: StageSearching}
	if _, err := a.Fetch(context.Background(), st, t.TempDir(), rep); err != nil {
		t.Fatalf("Fetch 意外报错: %v", err)
	}

	if len(rep.snapshots) == 0 {
		t.Fatal("Fetch 一帧都没报")
	}
	if rep.snapshots[0].Searches == 0 {
		t.Errorf("Fetch 报的第一帧是与 Runner 首帧重复的空状态（Searches=0），"+
			"会触发 message is not modified；实际首帧: %+v", rep.snapshots[0])
	}
}

// TestAgentZeroResultResetsFound 是补给 Minor C(b) 的回归测试：
// "搜到 12 条 → 换词重搜 → 零结果"之后，Found 必须归零，
// 否则进度消息会一直挂着"搜索 2 次 · 12 个候选"这种不实描述。
func TestAgentZeroResultResetsFound(t *testing.T) {
	src := &fakeSource{
		name: "fake", payload: "b",
		results: map[string][]Candidate{
			"hit": {
				{ID: "1", Artist: "A", Title: "T1", dlURL: "u1"},
				{ID: "2", Artist: "A", Title: "T2", dlURL: "u2"},
			},
			// "miss" 不在表里 → 零结果
		},
	}
	llm := &fakeLLM{scripts: []openai.ChatCompletionResponse{
		toolCallResp("c1", "search_fake", `{"query":"hit"}`),  // 2 个候选
		toolCallResp("c2", "search_fake", `{"query":"miss"}`), // 零结果
		finalResp("放弃"), // 随便给个非 JSON 结论，让 Fetch 收场
	}}

	a := newTestAgent(t, llm, src)
	st := &Status{Query: "x"}
	_, _ = a.Fetch(context.Background(), st, t.TempDir(), &nopReporter{})

	if st.Searches != 2 {
		t.Fatalf("st.Searches = %d, want 2", st.Searches)
	}
	if st.Found != 0 {
		t.Errorf("零结果之后 st.Found = %d, want 0（不能留着上一次的 2）", st.Found)
	}
}

// TestBuildToolDefs 验证：每个源自动生成两个工具，名字按约定拼。
func TestBuildToolDefs(t *testing.T) {
	defs := buildToolDefs([]Source{&fakeSource{name: "alpha"}, &fakeSource{name: "beta"}})
	if len(defs) != 4 {
		t.Fatalf("2 个源应生成 4 个工具，实际 %d 个", len(defs))
	}
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Function.Name] = true
	}
	for _, want := range []string{"search_alpha", "download_alpha", "search_beta", "download_beta"} {
		if !names[want] {
			t.Errorf("缺少工具 %q，实际有: %v", want, names)
		}
	}
}

// TestBuildSystemPromptOrdersSources 验证：系统提示词按传入顺序列出音源，
// 并明确要求模型按这个顺序依次尝试。
//
// 优先级就是靠这个落地的 —— Go 里没有任何硬编码的音源排序，
// 顺序完全来自 cfg.Music.sources。所以这条断言守的是整个优先级机制。
func TestBuildSystemPromptOrdersSources(t *testing.T) {
	p := buildSystemPrompt([]Source{
		&fakeSource{name: "musicso", hint: "中文站提示"},
		&fakeSource{name: "mp3pm", hint: "俄语站提示"},
	})

	iMusicSo := strings.Index(p, "musicso")
	iMp3PM := strings.Index(p, "mp3pm")
	if iMusicSo < 0 || iMp3PM < 0 {
		t.Fatalf("提示词里应当列出两个音源:\n%s", p)
	}
	if iMusicSo > iMp3PM {
		t.Errorf("音源顺序被打乱：musicso 应当排在 mp3pm 之前\n%s", p)
	}
	// 光列出来不够，得明确告诉模型这是有先后的。
	if !strings.Contains(p, "顺序") {
		t.Errorf("提示词没有要求模型按顺序尝试音源:\n%s", p)
	}
	// 各源的 Hint 必须都在（"某个站怎么搜才有效"这条知识跟着站点实现走）。
	for _, want := range []string{"中文站提示", "俄语站提示"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词里缺少 Hint %q:\n%s", want, p)
		}
	}
}

// TestBuildSystemPromptNormalizationIsConditional 验证：规范化那段不再无条件
// 要求"把罗马化还原成中文"。
//
// musicso 给的本来就是中文原名，无条件的措辞会诱导模型去"还原"一个
// 已经是原名的字符串，白白引入出错机会。
func TestBuildSystemPromptNormalizationIsConditional(t *testing.T) {
	p := buildSystemPrompt([]Source{&fakeSource{name: "musicso", hint: "h"}})
	if !strings.Contains(p, "已经是中文") {
		t.Errorf("提示词应当说明「已经是中文的原样保留」:\n%s", p)
	}
}

// TestDefaultMaxStepsFitsMultipleSources 守住步数上限。
//
// 一轮 = 一次模型调用。两个音源的最坏路径是：
// musicso 搜 → 换词 → mp3pm 搜 → 换词 → 下载 → 下载失败改选 → 再下载 → 输出 JSON
// 共 8 轮。原来的 6 在双源场景下会稳定撞上"未收敛"。
func TestDefaultMaxStepsFitsMultipleSources(t *testing.T) {
	const worstCasePath = 8
	if defaultMaxSteps < worstCasePath {
		t.Fatalf("defaultMaxSteps = %d，装不下双音源最坏路径的 %d 轮", defaultMaxSteps, worstCasePath)
	}
}
