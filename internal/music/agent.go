package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/cheedonghu/news2tg/internal/tools"
)

const (
	// defaultMaxSteps 限制 agent 循环步数，防止模型反复调工具不收敛。
	// 比 internal/agent 的 3 大：这里要容纳"搜索 → 零结果 → 换词搜 → 再换词搜 → 下载 → 给结论"。
	defaultMaxSteps = 6
	// defaultMaxBytes 是单曲下载大小上限，防御模型选中一个错误条目把磁盘写爆。
	// 50 << 20 = 50 MiB；正常 mp3 在 3-15 MB 之间。
	defaultMaxBytes = 50 << 20
	// maxCandidates 是一次搜索回给模型的候选数上限。
	// 搜出上百条时全塞进上下文纯属烧 token，前 20 条足够模型挑。
	maxCandidates = 20
	// downloadFileName 是临时目录里的固定文件名。
	// 用固定名而非歌名：此刻还没拿到模型规范化后的名字，
	// 真正的文件名在上传时由 BuildFilename 决定。
	downloadFileName = "download.mp3"
)

// errTooLarge 是下载超限的哨兵错误，供 errors.Is 判别。
var errTooLarge = errors.New("下载内容超过大小上限")

// chatCompleter 抽象 LLM 聊天补全调用：*openai.Client 天然满足，
// 测试时注入 fake，避免真发网络请求。同 internal/agent 的做法。
type chatCompleter interface {
	CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// Agent 持有 LLM 客户端、模型名、音源注册表和工具定义。
type Agent struct {
	llm       chatCompleter
	model     string
	sources   map[string]Source // 源名 → 音源
	toolDefs  []openai.Tool     // 传给模型的工具 schema
	sysPrompt string
	maxSteps  int
	maxBytes  int64 // 抽成字段而非直接用常量，方便测试压小
}

// NewAgent 构造函数。
// apiKey 复用 DeepSeek 的 key；model 由 main 从 [deepseek] agent_model 传入，
// 必须支持 function calling，否则下面的 toolDefs 形同虚设。
func NewAgent(apiKey, model string, sources []Source) *Agent {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = "https://api.deepseek.com/v1" // 同 ai/deepseek.go：DeepSeek 兼容 OpenAI 协议

	// 建注册表：工具名里的源标识 → 音源实例。
	reg := make(map[string]Source, len(sources))
	for _, s := range sources {
		reg[s.Name()] = s
	}

	return &Agent{
		llm:       openai.NewClientWithConfig(cfg),
		model:     model,
		sources:   reg,
		toolDefs:  buildToolDefs(sources),
		sysPrompt: buildSystemPrompt(sources),
		maxSteps:  defaultMaxSteps,
		maxBytes:  defaultMaxBytes,
	}
}

// buildSystemPrompt 拼系统提示词。
// 各音源的特性说明（Hint）在这里被收集进来 —— "某个站怎么搜才有效"这条知识
// 跟着站点实现走，加音源时提示词自动跟着长，不用手改这段。
func buildSystemPrompt(sources []Source) string {
	var b strings.Builder
	b.WriteString(`你是一个音乐下载助手。用户会用自由格式描述想要的歌（如「晴天 - 周杰伦」「周杰伦 晴天」「晴天」「Jay Chou 的晴天」），你需要理解它，从音源里搜索、挑出正确的曲目并下载。

工具使用规则：
1. 先用 search_<源名> 搜索，返回的候选每条含 id / artist / title / duration。
2. 如果零结果，按下面该音源的特性说明换关键词重试（拼音、英文译名、只用歌名不带歌手）。整个任务最多搜索 3 次。
3. 从候选里挑最匹配用户意图的那一条。避开 live 版、伴奏/instrumental、翻唱(cover)、remix、加速/慢放版本，除非用户明确要。
4. 选定后必须调用同一个源的 download_<源名>，参数 id 取自候选列表。
5. 下载成功后不要再调用任何工具，直接输出一个 JSON 对象作为最终回答，格式严格如下：
   {"artist": "歌手中文原名", "title": "歌名中文原名", "id": "刚才下载的那个 id"}
   artist 和 title 必须是规范化后的原名：音源里的罗马化写法（如 "Jay Chou" / "Kai Bu Liao Kou"）
   要还原成中文原名（"周杰伦" / "开不了口"）；本来就是英文歌的保持英文即可。
   只输出这个 JSON，不要有任何其它文字、解释或 markdown 代码块。

可用音源：
`)
	for _, s := range sources {
		b.WriteString("- " + s.Name() + "：" + s.Hint() + "\n")
	}
	return b.String()
}

// buildToolDefs 为每个音源生成 search_<名> / download_<名> 两个工具的 schema。
// 这就是"加音源不用改 agent 循环"的兑现处：注册表和工具定义都由 Source 列表推导出来。
func buildToolDefs(sources []Source) []openai.Tool {
	// 参数 schema 用 json.RawMessage 直接写，FunctionDefinition.Parameters 接受 any。
	searchParams := json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": { "type": "string", "description": "搜索关键词，如歌名、歌名+歌手、拼音或英文译名" }
		},
		"required": ["query"]
	}`)
	idParams := json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": { "type": "string", "description": "搜索结果里某条候选的 id，必须原样照抄" }
		},
		"required": ["id"]
	}`)

	defs := make([]openai.Tool, 0, len(sources)*2)
	for _, s := range sources {
		name := s.Name()
		defs = append(defs,
			openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "search_" + name,
				Description: "在 " + name + " 搜索歌曲，返回候选列表。" + s.Hint(),
				Parameters:  searchParams,
			}},
			openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
				Name:        "download_" + name,
				Description: "从 " + name + " 下载指定 id 的歌曲到本地。id 必须来自 search_" + name + " 的返回，不能自己编。",
				Parameters:  idParams,
			}},
		)
	}
	return defs
}

// run 是**一次** Fetch 的局部状态。
//
// 关键设计：候选表、已下载 track 等都挂在这里而不是 Agent 上，
// 所以同一个 Agent 可以被多个并发的 /music 任务共用而互不干扰 ——
// 每次 Fetch 都新建一个 run，随任务结束丢弃。
type run struct {
	agent        *Agent
	dir          string               // 下载落盘目录，由调用方创建与清理
	rep          Reporter             //
	st           *Status              // 与 Runner 共享的同一份进度快照
	candidates   map[string]Candidate // "源名:id" → 候选（直链只在这里，不进模型上下文）
	track        *Track               // 下载成功后才非 nil
	downloadedID string               // 实际下载的候选 id，用于校验模型最终输出
}

// Fetch 跑工具调用循环：理解描述 → 搜索 → 选曲 → 下载 → 拿到规范化命名。
//
// st 由调用方创建并持有（Query 已填好）：agent 直接在这份快照上改
// Searches/Found/Stage/Track，Runner 后续接着改上传状态 ——
// 全程只有一份进度真相，不会出现"agent 记了搜索次数、Runner 那份是 0"的错位。
//
// dir 的创建与清理归调用方（Runner 用 os.MkdirTemp + defer os.RemoveAll）。
func (a *Agent) Fetch(ctx context.Context, st *Status, dir string, rep Reporter) (*Track, error) {
	r := &run{
		agent:      a,
		dir:        dir,
		rep:        rep,
		st:         st,
		candidates: make(map[string]Candidate),
	}
	st.Stage = StageSearching
	rep.Update(ctx, *st)

	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: a.sysPrompt},
		{Role: openai.ChatMessageRoleUser, Content: "请帮我下载这首歌：" + st.Query},
	}

	// 累计本次任务的 token 消耗：一次 Fetch 会多轮调模型，逐轮累加 usage.total。
	totalTokens := 0

	for step := 0; step < a.maxSteps; step++ {
		resp, err := a.llm.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    a.model,
			Messages: messages,
			Tools:    a.toolDefs,
		})
		if err != nil {
			return nil, fmt.Errorf("音乐 agent 调用大模型失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("音乐 agent 大模型返回空 choices")
		}
		totalTokens += resp.Usage.TotalTokens

		msg := resp.Choices[0].Message
		// 把本轮 assistant 消息（可能带 ToolCalls）追加进上下文。
		messages = append(messages, msg)

		// 没有工具调用 = 模型给出了最终回答。
		if len(msg.ToolCalls) == 0 {
			// 返回前把整段对话序列化打印，便于审计。注意 dump 的是 messages 而不是
			// 单次 resp —— resp 只有最后一轮消息，拿不到工具调用链和候选列表。
			if raw, mErr := json.Marshal(messages); mErr != nil {
				slog.ErrorContext(ctx, "音乐 agent 对话审计序列化失败", "err", mErr)
			} else {
				slog.InfoContext(ctx, "音乐 agent 大模型交互对话审计", "tokens", totalTokens, "conversation", string(raw))
			}
			return r.finalize(msg.Content, totalTokens)
		}

		// 逐个执行工具调用，把结果（或失败说明）作为 tool 消息回灌。
		for _, tc := range msg.ToolCalls {
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: tc.ID,
				Content:    r.runTool(ctx, tc),
			})
		}
	}

	return nil, fmt.Errorf("音乐 agent 超过最大步数(%d)仍未给出结果，模型未收敛", a.maxSteps)
}

// finalize 校验并解析模型的最终 JSON 输出，产出 Track。
func (r *run) finalize(content string, tokens int) (*Track, error) {
	// 没下载过就给结论 = 没有文件可传，直接失败。
	if r.track == nil {
		return nil, fmt.Errorf("模型未调用下载工具就给出了结论：%s", tools.TruncateUTF8(content, 200))
	}

	var out struct {
		Artist string `json:"artist"`
		Title  string `json:"title"`
		ID     string `json:"id"`
	}
	// 有些模型爱把 JSON 包在 ```json 代码块里，虽然提示词禁止了，仍顺手剥一层。
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &out); err != nil {
		return nil, fmt.Errorf("模型最终输出不是合法 JSON（%v）：%s", err, tools.TruncateUTF8(content, 200))
	}
	if strings.TrimSpace(out.Artist) == "" || strings.TrimSpace(out.Title) == "" {
		return nil, fmt.Errorf("模型最终输出缺少 artist/title：%s", tools.TruncateUTF8(content, 200))
	}
	// id 对不上说明模型自相矛盾，文件名与文件内容大概率对不上，宁可失败也别传错东西。
	if out.ID != r.downloadedID {
		return nil, fmt.Errorf("模型最终输出的 id %q 与实际下载的 %q 不一致", out.ID, r.downloadedID)
	}

	r.track.Artist = strings.TrimSpace(out.Artist)
	r.track.Title = strings.TrimSpace(out.Title)
	r.track.Tokens = tokens
	return r.track, nil
}

// stripCodeFence 剥掉可能存在的 ```json ... ``` 包裹。
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// 去掉首行（``` 或 ```json）和末尾的 ```
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// runTool 把工具调用派发到搜索或下载。
// 工具名的形状是 search_<源名> / download_<源名>，所以按前缀切一刀就知道去哪。
func (r *run) runTool(ctx context.Context, tc openai.ToolCall) string {
	name := tc.Function.Name
	switch {
	case strings.HasPrefix(name, "search_"):
		return r.doSearch(ctx, strings.TrimPrefix(name, "search_"), tc.Function.Arguments)
	case strings.HasPrefix(name, "download_"):
		return r.doDownload(ctx, strings.TrimPrefix(name, "download_"), tc.Function.Arguments)
	}
	return fmt.Sprintf("未知工具 %q。请使用 search_<源名> 或 download_<源名>。", name)
}

// doSearch 执行一次搜索，把轻量候选列表序列化后回灌给模型。
//
// 注意所有失败路径都是**回灌文本**而不是中断：模型据此可以换关键词或换源，
// 沿用 internal/agent 里同样的"失败回灌不中断"设计。
func (r *run) doSearch(ctx context.Context, srcName, rawArgs string) string {
	src, ok := r.agent.sources[srcName]
	if !ok {
		return fmt.Sprintf("未知音源 %q。", srcName)
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return fmt.Sprintf("工具参数解析失败: %v；参数应为 {\"query\": \"...\"}。", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return "工具参数 query 为空，请给出搜索关键词。"
	}

	// 进度：搜索次数在真正发起搜索前就 +1，这样"搜索中"能立刻显示出来。
	r.st.Searches++
	r.rep.Update(ctx, *r.st)

	cands, err := src.Search(ctx, args.Query)
	if err != nil {
		slog.ErrorContext(ctx, "音乐搜索失败", "source", srcName, "query", args.Query, "err", err)
		return fmt.Sprintf("搜索失败: %v。可以换个关键词再试一次。", err)
	}
	if len(cands) == 0 {
		// 零结果是正常路径，把音源特性再提醒一遍，引导模型换词。
		return "零结果。" + src.Hint() + " 请换关键词重试。"
	}
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}

	// lite 是回给模型的精简形态：**不含直链**。
	type lite struct {
		ID       string `json:"id"`
		Artist   string `json:"artist"`
		Title    string `json:"title"`
		Duration string `json:"duration"`
	}
	out := make([]lite, 0, len(cands))
	for _, c := range cands {
		// 候选存进本次任务的局部表，下载时凭 id 查回真直链。
		r.candidates[srcName+":"+c.ID] = c
		out = append(out, lite{ID: c.ID, Artist: c.Artist, Title: c.Title, Duration: c.Duration})
	}

	r.st.Found = len(out)
	r.rep.Update(ctx, *r.st)

	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Sprintf("序列化候选列表失败: %v", err)
	}
	slog.InfoContext(ctx, "音乐搜索完成", "source", srcName, "query", args.Query, "count", len(out))
	return string(raw)
}

// doDownload 下载指定候选到临时目录。
func (r *run) doDownload(ctx context.Context, srcName, rawArgs string) string {
	src, ok := r.agent.sources[srcName]
	if !ok {
		return fmt.Sprintf("未知音源 %q。", srcName)
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return fmt.Sprintf("工具参数解析失败: %v；参数应为 {\"id\": \"...\"}。", err)
	}
	c, ok := r.candidates[srcName+":"+args.ID]
	if !ok {
		// 模型编了个不存在的 id：回灌纠正，别打到音源上去。
		return fmt.Sprintf("id %q 不在 %s 的搜索结果里。请先搜索，并原样使用候选列表里的 id。", args.ID, srcName)
	}

	// 进度：先把"选定了哪首"显示出来（此刻 Bytes 还是 0，渲染成进行中）。
	r.st.Stage = StageDownloading
	r.st.Track = &Track{Artist: c.Artist, Title: c.Title, Duration: c.Duration, Source: srcName}
	r.rep.Update(ctx, *r.st)

	path := filepath.Join(r.dir, downloadFileName)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Sprintf("创建本地文件失败: %v", err)
	}

	// limitWriter 在这里统一强制大小上限，不下放给各音源实现。
	lw := &limitWriter{w: f, limit: r.agent.maxBytes}
	n, dErr := src.Download(ctx, c, lw)
	closeErr := f.Close()

	if dErr != nil || closeErr != nil {
		// 删掉半成品：残缺的 mp3 传上网盘比没有更糟。
		if rmErr := os.Remove(path); rmErr != nil {
			slog.WarnContext(ctx, "删除下载半成品失败", "path", path, "err", rmErr)
		}
		r.st.Track = nil
		r.rep.Update(ctx, *r.st)

		// errors.Is 沿着 %w 包装链找哨兵错误，所以音源里包了几层都能认出来。
		if errors.Is(dErr, errTooLarge) {
			return fmt.Sprintf("文件过大（超过 %d MB），已中断下载。请改选其它候选。", r.agent.maxBytes>>20)
		}
		if dErr == nil {
			dErr = closeErr
		}
		slog.ErrorContext(ctx, "音乐下载失败", "source", srcName, "id", c.ID, "err", dErr)
		return fmt.Sprintf("下载失败: %v。可以改选其它候选。", dErr)
	}

	r.track = &Track{
		LocalPath: path,
		Artist:    c.Artist, // 先填站点原始名，finalize 里会被模型规范化后的名字覆盖
		Title:     c.Title,
		Duration:  c.Duration,
		Bytes:     n,
		Source:    srcName,
	}
	r.downloadedID = c.ID
	r.st.Track = r.track
	r.rep.Update(ctx, *r.st)

	slog.InfoContext(ctx, "音乐下载完成", "source", srcName, "id", c.ID, "bytes", n)
	return fmt.Sprintf("下载完成，%d 字节。现在请按要求输出最终 JSON。", n)
}

// limitWriter 是计数 writer：写入总量超过 limit 立即返回 errTooLarge，
// 让上游的 io.Copy 中断。
//
// 为什么上限在这里而不是各个 Source 里？
// 否则每加一个音源都要重复实现一遍同样的防御，而且很容易漏。
type limitWriter struct {
	w     io.Writer
	limit int64
	n     int64 // 已写入字节数
}

// Write 满足 io.Writer。超限时返回 (0, errTooLarge)，io.Copy 会立刻停下来。
func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > l.limit {
		return 0, errTooLarge
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}
