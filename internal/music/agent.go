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
	// 一轮 = 一次模型调用。比 internal/agent 的 3 大得多，因为这里要容纳
	// 多音源的最坏路径：musicso 搜 → 换词 → mp3pm 搜 → 换词 → 下载 →
	// 下载失败改选 → 再下载 → 输出最终 JSON，共 8 轮，留两轮余量取 10。
	// 音源变多时这个值要跟着涨，否则会稳定撞上"未收敛"。
	// ⚠️ 改这个值时必须同步检查 runner.go 的 agentTimeout ——
	// 它是按这里的最坏路径轮数估出来的超时预算，两个旋钮是耦合的，
	// 只调一个就会出现"轮数够了但先被超时打断"的问题。
	defaultMaxSteps = 10
	// defaultMaxBytes 是单曲下载大小上限，防御模型选中一个错误条目把磁盘写爆。
	// 50 << 20 = 50 MiB；正常 mp3 在 3-15 MB 之间。
	defaultMaxBytes = 50 << 20
	// maxCandidates 是一次搜索回给模型的候选数上限。
	// 搜出上百条时全塞进上下文纯属烧 token，前 20 条足够模型挑。
	maxCandidates = 20
)

// tempFileName 为**一次下载尝试**拼出临时目录里的文件名。
//
// 为什么必须按候选区分，而不是所有尝试共用一个固定的 download.mp3：
// 下载失败的分支要删掉半成品，而"下载 1 成功 → 模型改主意去下 2 → 2 中途失败"
// 是设计里明确支持的路径（spec：下载失败 → 回灌，模型可改选另一个候选）。
// 共用一个文件名时，删掉 2 的半成品等于把 1 那个**已经下好的**文件一并删了，
// 可 r.track / r.downloadedID 还留着；模型退回去输出 {"id":"1"} 时 finalize
// 的每一道校验都能过，最后返回一个 LocalPath 已不存在的 Track，两个 WebDAV
// 目标各自 os.Open 失败，用户看到的是"全部 2 个 WebDAV 目标上传失败"——
// 一个把 agent 层的 bug 甩锅给网络的、彻底误导的诊断。
// 一次尝试一个文件名，这类冲突从根上就不存在了。
//
// 不用歌名：此刻还没拿到模型规范化后的名字，最终文件名在上传时由
// BuildFilename 决定，这里只求唯一。
//
// id 虽然只可能来自 r.candidates（模型编的 id 早在 doDownload 里就被拦下了），
// 仍然过一遍 filenameReplacer：它归根到底是从站点 HTML 里解析出来的，
// 万一含 '/' 就会把文件写到临时目录之外去，不值得为此赌解析器永远干净。
func tempFileName(srcName, id string) string {
	return filenameReplacer.Replace(srcName+"-"+id) + ".mp3"
}

// errTooLarge 是下载超限的哨兵错误，供 errors.Is 判别。
var errTooLarge = errors.New("下载内容超过大小上限")

// errSourceUnavailable 是"站点级不可达"的哨兵错误，供 errors.Is 判别。
//
// 为什么要单独区分它，而不是让 doSearch 把所有 Search 错误一律回灌成
// "换个关键词再试"：像 musicso 的 Cloudflare 质询这种错误是站点整体
// 拒绝服务，跟搜索关键词毫无关系——换多少词结果都一样，模型只会在这条
// 死路上白烧 1-2 轮。质询错误的措辞本身已经说得很清楚，但一旦被
// doSearch 拼上"可以换个关键词再试一次"的通用尾巴，模型收到的却是
// 相反的指令。各 Source 在这类站点级错误上用 %w 包一层这个哨兵
// （参见 musicso.go 的 musicSoChallenge），doSearch 就能用 errors.Is
// 认出来，回灌"换词无用，请改用其它音源"而不是通用文案。
// 用法与 errTooLarge 保持一致的风格。
var errSourceUnavailable = errors.New("音源站点级不可达")

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
1. 先用 search_<源名> 搜索，返回的候选每条含 id / artist / title / duration（有的音源不提供 duration，会是空串，属正常）。
2. 下面「可用音源」是**按优先级排好序**的：请**按这个顺序**依次尝试——先试第一个，它零结果或没有匹配项时再换下一个，依此类推。
3. 同一个音源零结果时，可按该音源的特性说明换关键词重试（拼音、英文译名、只用歌名不带歌手）。整个任务最多搜索 4 次。
4. 从候选里挑最匹配用户意图的那一条。避开 live 版、伴奏/instrumental、翻唱(cover)、remix、加速/慢放版本，除非用户明确要。
5. 选定后必须调用同一个源的 download_<源名>，参数 id 取自候选列表，必须原样照抄。
6. 下载成功后不要再调用任何工具，直接输出一个 JSON 对象作为最终回答，格式严格如下：
   {"artist": "歌手原名", "title": "歌名原名", "id": "刚才下载的那个 id"}
   artist 和 title 要规范化：若音源给的是罗马化写法（如 "Jay Chou" / "Kai Bu Liao Kou"），
   还原成中文原名（"周杰伦" / "开不了口"）；**已经是中文原名的原样保留，不要改动**；
   本来就是英文歌的保持英文即可。
   只输出这个 JSON，不要有任何其它文字、解释或 markdown 代码块。

可用音源（按优先级从高到低）：
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
	rep          Reporter             // 进度出口
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
	// 这里**不发首帧**。Runner 在调 Fetch 之前已经用同一份 st 发过一次了
	// （那次的 Stage 就是 StageSearching，本函数原先那两行是逐字重复的）。
	// 再发一次的后果很具体：第二帧内容与首帧一字不差，必然落进节流窗口被
	// 存成 pending；只要第一轮模型调用超过 3s（很常见），补发就会送出一次
	// 内容完全没变的 Edit —— Telegram 直接回 "message is not modified"，
	// 记一条错误日志，还白占一次 1.5s 的全局发送锁，每次 /music 都要来一遍。
	// 首帧归 Runner 管（它同时也负责 SendEditable 拿 msgID），这里只管往下报。

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

	// 文件真的还在吗？
	//
	// r.track 记的是"某一次成功下载"，但那之后可能还有别的下载尝试。临时文件
	// 现在已经按候选分名（见 tempFileName），别的尝试删不掉它了；这道 Stat
	// 仍然保留，是为了把"文件没了"这件事**挡在上传之前**，让错误信息说出真正
	// 的原因。否则一旦文件不在，就变成两个 WebDAV 目标各自 os.Open 失败，
	// 最终报成"全部 N 个 WebDAV 目标上传失败"——一条把人往网络问题上带的假线索。
	if _, sErr := os.Stat(r.track.LocalPath); sErr != nil {
		return nil, fmt.Errorf("已下载的音频文件不可用（%s）: %w", r.track.LocalPath, sErr)
	}

	// 注意：这里**造一个新的 Track**，而不是原地改 r.track。
	//
	// r.track 这个指针早就通过 r.st.Track 进过 Reporter 的快照，此刻很可能正
	// 躺在节流的 pending 里等 time.AfterFunc 的 goroutine 读。在这里改它的
	// Artist/Title/Tokens 就是跨 goroutine 的无同步写，-race 能实锤；而且
	// 字符串是 ptr+len 两个字，撕裂读能让 renderStatus 拿到越界的长度直接
	// panic —— /music 那个 goroutine 是 detached 的、没有 recover，等于把进程干掉。
	// 值拷贝出来的这份谁都没见过，改它是安全的；已经发布出去的那个
	// r.track 从此保持不变，快照的"自洽"承诺才真正成立（见 report.go 的 Status 注释）。
	nt := *r.track
	nt.Artist = strings.TrimSpace(out.Artist)
	nt.Title = strings.TrimSpace(out.Title)
	nt.Tokens = tokens
	return &nt, nil
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
		// Found 必须归零：它的语义是"**最近一次**搜索的候选数"。不重置的话，
		// "搜到 12 条 → 换词重搜 → 这次失败/零结果"之后，消息会一直挂着
		// "搜索 2 次 · 12 个候选"，说的是一件根本没发生过的事。
		// 原先的 r.st.Found = len(out) 坐落在下面零结果早退之后，所以这两条
		// 分支都漏掉了它。
		r.st.Found = 0
		r.rep.Update(ctx, *r.st)
		slog.ErrorContext(ctx, "音乐搜索失败", "source", srcName, "query", args.Query, "err", err)
		// 站点级不可达（如 Cloudflare 质询）跟关键词无关，换词只会白烧步数；
		// 用 errors.Is 认出这类错误后给出相反的指引，让模型转去试其它音源。
		if errors.Is(err, errSourceUnavailable) {
			return fmt.Sprintf("搜索失败: %v。这是该音源站点级不可达，换关键词无用，请改用其它音源。", err)
		}
		return fmt.Sprintf("搜索失败: %v。可以换个关键词再试一次。", err)
	}
	if len(cands) == 0 {
		// 零结果是正常路径，把音源特性再提醒一遍，引导模型换词。
		// Found 归零的理由同上。
		r.st.Found = 0
		r.rep.Update(ctx, *r.st)
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

	// 一次尝试一个文件名，绝不与别的候选撞车（理由见 tempFileName 的注释）。
	path := filepath.Join(r.dir, tempFileName(srcName, c.ID))
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
		// 删的只是**本次尝试自己的**那个文件 —— 文件名按候选区分，所以这里
		// 不可能再误删上一次成功下载的成果（见 tempFileName 的注释）。
		if rmErr := os.Remove(path); rmErr != nil {
			slog.WarnContext(ctx, "删除下载半成品失败", "path", path, "err", rmErr)
		}
		// 退回"上一次成功下载的那首"（一次都没成过时它本来就是 nil），
		// 而不是无脑置 nil：本次尝试失败并不让之前那次的成功作废，
		// 进度消息不该把已经下好的歌从画面上抹掉。
		r.st.Track = r.track
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
