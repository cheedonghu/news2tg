package music

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

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
	results   map[string][]Candidate // 关键词 → 候选；未命中的关键词返回零结果
	searchErr error
	payload   string // Download 写出的内容
	dlErr     error
	dlCalls   []string // 记录被下载的 id
}

func (s *fakeSource) Name() string { return s.name }
func (s *fakeSource) Hint() string { return "测试音源" }

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
