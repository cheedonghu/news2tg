// Package agent 实现一个"网址 → 网页正文中文总结"的 LLM agent。
//
// 与 ai.Helper（内容 → 总结）不同，本 agent 接收的是网址，并通过 LLM 的
// 工具调用（function calling）自主选择正文提取渠道：
//   - 优先用 extract_with_python（本机 Python sidecar，digest.Python）
//   - 当提取失败或模型判断内容不准确/不完整时，回退 extract_with_jina（digest.Jina）
//
// 拿到可用正文后由模型给出中文总结。两个提取器都只是 digest.Fetcher 实现，
// 因此将来加渠道只需再注册一个 Fetcher，不必改 agent 循环。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	openai "github.com/sashabaranov/go-openai"

	"github.com/cheedonghu/news2tg/internal/digest"
)

// 两个工具的名字，注册表和工具定义共用，避免拼写漂移。
const (
	toolPython = "extract_with_python"
	toolJina   = "extract_with_jina"
)

// defaultMaxSteps 限制 agent 循环步数，防止模型反复调工具不收敛。
const defaultMaxSteps = 3

// systemPrompt 指导模型如何编排两个提取工具并产出总结。
const systemPrompt = `你是一个网页内容总结助手。给你一个网址，你需要先获取网页正文，再用中文总结。

工具使用规则：
1. 先用 extract_with_python 提取网页正文（本机服务，速度快，优先使用）。
2. 如果 extract_with_python 调用失败，或返回的内容明显为空、报错、被截断、与网址主题不符（即"内容不准确/不完整"），再用 extract_with_jina 重新提取。
3. 拿到可用正文后，用中文总结其内容，最多不超过 2000 字。不要在最终回答里调用工具，直接给出总结正文。`

// chatCompleter 抽象 LLM 聊天补全调用：*openai.Client 天然满足，
// 测试时注入 fake，避免真发网络请求。
type chatCompleter interface {
	CreateChatCompletion(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// Agent 持有 LLM 客户端、模型名、工具注册表（工具名 → 提取器）和工具定义。
type Agent struct {
	llm      chatCompleter
	model    string
	tools    map[string]digest.Fetcher // 工具名 → 对应的正文提取器
	toolDefs []openai.Tool             // 传给模型的工具 schema
	maxSteps int
}

// NewAgent 构造函数。
// apiKey 复用 DeepSeek 的 key；model 由 main 从 [deepseek] agent_model 传入，
// 必须是支持 function calling 的模型，否则下面的 toolDefs 形同虚设。
// python / jina 都是 digest.Fetcher（用接口便于换实现/测试）。
func NewAgent(apiKey, model string, python, jina digest.Fetcher) *Agent {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = "https://api.deepseek.com/v1" // 同 ai/deepseek.go：DeepSeek 兼容 OpenAI 协议
	return &Agent{
		llm:   openai.NewClientWithConfig(cfg),
		model: model,
		tools: map[string]digest.Fetcher{
			toolPython: python,
			toolJina:   jina,
		},
		toolDefs: buildToolDefs(),
		maxSteps: defaultMaxSteps,
	}
}

// buildToolDefs 构造两个工具的 JSON schema 定义，参数都是 { "url": string }。
func buildToolDefs() []openai.Tool {
	// 参数 schema 用 json.RawMessage 直接写，FunctionDefinition.Parameters 接受 any。
	urlParams := json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": { "type": "string", "description": "要提取正文的网页完整网址" }
		},
		"required": ["url"]
	}`)
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        toolPython,
				Description: "用本机 Python sidecar 提取网页正文。优先使用。",
				Parameters:  urlParams,
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        toolJina,
				Description: "用 jina(r.jina.ai) 提取网页正文。仅当 extract_with_python 失败或内容不准确/不完整时使用。",
				Parameters:  urlParams,
			},
		},
	}
}

// Summarize 运行工具调用循环：给定网址，返回中文总结。
func (a *Agent) Summarize(ctx context.Context, url string) (string, error) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: "请总结这个网址的网页内容：" + url},
	}

	// 累计本次总结的 token 消耗：一次 Summarize 可能多轮调模型，逐轮累加 usage.total。
	totalTokens := 0

	for step := 0; step < a.maxSteps; step++ {
		resp, err := a.llm.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    a.model,
			Messages: messages,
			Tools:    a.toolDefs,
		})
		if err != nil {
			return "", fmt.Errorf("agent 调用大模型失败: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("agent 大模型返回空 choices")
		}
		totalTokens += resp.Usage.TotalTokens // 来源：模型返回的 usage

		msg := resp.Choices[0].Message
		// 把本轮 assistant 消息（可能带 ToolCalls）追加进上下文。
		messages = append(messages, msg)

		// 没有工具调用 = 模型给出了最终总结。把 token 统计拼到正文末尾再返回，
		// push 到 TG 时自动带上。保持纯文本，由下游统一做 MarkdownV2 转义。
		if len(msg.ToolCalls) == 0 {
			// 返回前把整段对话（messages，含 system/user/各轮 assistant/tool 结果）
			// 序列化成 JSON 打印，便于后续审计。注意 dump 的是 messages 而不是单次 resp——
			// resp 只有最后一轮 assistant 消息，拿不到工具调用链和提取到的正文。
			if raw, err := json.Marshal(messages); err != nil {
				slog.ErrorContext(ctx, "agent 对话审计序列化失败", "err", err)
			} else {
				slog.InfoContext(ctx, "agent 大模型交互对话审计", "tokens", totalTokens, "conversation", string(raw))
			}
			return fmt.Sprintf("%s\n\n本次消耗 tokens: %d", msg.Content, totalTokens), nil
		}

		// 逐个执行工具调用，把结果（或失败说明）作为 tool 消息回灌。
		for _, tc := range msg.ToolCalls {
			result := a.runTool(ctx, tc)
			messages = append(messages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	return "", fmt.Errorf("agent 超过最大步数(%d)仍未给出总结", a.maxSteps)
}

// runTool 执行一次工具调用，返回要回灌给模型的文本。
// 提取失败不直接 return，而是把错误说明回灌，让模型自主决定是否回退到另一个渠道。
func (a *Agent) runTool(ctx context.Context, tc openai.ToolCall) string {
	fetcher, ok := a.tools[tc.Function.Name]
	if !ok {
		return fmt.Sprintf("未知工具 %q，请改用 %s 或 %s。", tc.Function.Name, toolPython, toolJina)
	}

	// 解析模型给的参数 JSON：{ "url": "..." }。
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return fmt.Sprintf("工具参数解析失败: %v；参数应为 {\"url\": \"...\"}。", err)
	}
	if args.URL == "" {
		return "工具参数 url 为空，请提供要提取的网页网址。"
	}

	content, err := fetcher.Fetch(ctx, args.URL)
	if err != nil {
		slog.ErrorContext(ctx, "agent 工具提取失败", "tool", tc.Function.Name, "url", args.URL, "err", err)
		// 关键：失败回灌而非中断，模型据此可改用另一个提取工具。
		return fmt.Sprintf("%s 提取失败: %v。如果还没试过另一个提取工具，可以改用它。", tc.Function.Name, err)
	}
	slog.InfoContext(ctx, "agent 工具提取成功", "tool", tc.Function.Name, "url", args.URL, "len", len(content))
	return content
}
