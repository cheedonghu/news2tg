package ai

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8" // 处理 UTF-8 字符（中文一个字符 = 多个字节，必须用这个包按"字符"算长度）

	// 用别名 openai 显式标识包名（go-openai 的包名就是 openai，这里写成显式更清楚）。
	openai "github.com/sashabaranov/go-openai"
)

// DeepSeek 实现 ai.Helper 接口。
// DeepSeek 的 API 是 OpenAI 协议兼容的，所以直接复用 go-openai 这个 SDK，
// 把 BaseURL 换成 DeepSeek 的地址即可。
type DeepSeek struct {
	client *openai.Client // SDK 客户端，内部维护 HTTP client
	// 上下文窗口大小
	contextLength int
	// AI总结后给用户阅读的最大长度
	readerLength int
}

// NewDeepSeek 构造函数。注意没有返回 error：这里只是配置，没真发请求。
func NewDeepSeek(apiKey string) *DeepSeek {
	cfg := openai.DefaultConfig(apiKey)         // 默认配置（OpenAI 官方地址）
	cfg.BaseURL = "https://api.deepseek.com/v1" // 改成 DeepSeek 的地址
	return &DeepSeek{client: openai.NewClientWithConfig(cfg), contextLength: 60000, readerLength: 2000}
}

// Summarize 实现 Helper 接口；签名一致就自动算"实现了"。
//
// 行为：
//   - 模型上下文基本都有128k了，单字符token大约是1.3，那字符个数就是98,461，因为注意力机制，设置成60000
//   - 拼一个中文 prompt，调一次 chat completion；
//   - 失败时不抛 error 给上层，返回固定兜底文案 + nil（让推送照样发出去）—— 这是有意设计。
func (d *DeepSeek) Summarize(ctx context.Context, content string) (string, error) {
	slog.InfoContext(ctx, "利用大模型将内容转为中文")

	// utf8.RuneCountInString 按"字符"数（rune 数）算长度。
	// 不能用 len(content)：那是字节数，中文一个字符 3 字节，会偏大。
	if utf8.RuneCountInString(content) > d.contextLength {
		// 注意：这里也返回 nil 而不是 error —— 错误"语义"通过返回字符串表达。
		// 严格来说返回 error 更合理；这是项目当前的妥协，便于上层无脑推送。
		return fmt.Sprintf("字符数为%d，超过设定的上下文窗口%d，尝试使用agent功能", utf8.RuneCountInString(content), d.contextLength), nil
	}

	// 拼 prompt：fmt.Sprintf 把模板和内容拼成完整字符串。
	prompt := fmt.Sprintf("帮我用中文总结下面的内容，最大不超过%d字: \n%s", d.readerLength, content)

	// 调 SDK：CreateChatCompletion 是 OpenAI Chat Completions 的标准调用。
	// 入参是结构体字面量，复杂请求一目了然。
	resp, err := d.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: "deepseek-v4-flash",
		Messages: []openai.ChatCompletionMessage{
			// 单条 user 消息；多轮对话会塞多个进去（system / user / assistant 轮流）。
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	})
	if err != nil {
		slog.ErrorContext(ctx, "大模型返回异常", "err", err)
		// 同样：失败兜底而不是抛 error
		return "大模型返回异常", nil
	}
	// Choices 是模型生成的多个候选；通常只有 1 个。空切片就视作异常。
	if len(resp.Choices) == 0 {
		return "大模型返回非String内容", nil
	}
	// 取第一个候选的回复文本。
	return resp.Choices[0].Message.Content, nil
}

// Advise 根据多城市天气文本，用 DeepSeek 生成中文穿衣建议。
//
// 与 Summarize 一样是"失败兜底不抛错"：任何异常都返回固定文案 + nil，
// 让天气推送照常发出去（AI 只是锦上添花，不能阻断主流程）。
// 一次调用处理所有城市：把各城市天气拼进 prompt，让模型按城市各给一句。
func (d *DeepSeek) Advise(ctx context.Context, weatherText string) (string, error) {
	slog.InfoContext(ctx, "利用大模型生成穿衣建议")

	// prompt 约束输出格式：每城市一行「城市：建议」，避免模型发挥太长。
	prompt := fmt.Sprintf(
		"下面是今天几个城市的天气，请用中文为每个城市各写一句简短的穿衣建议，"+
			"每个城市一行，格式「城市：建议」，不要多余解释：\n%s",
		weatherText,
	)

	resp, err := d.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: "deepseek-v4-flash", // 与 Summarize 同款轻量模型
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	})
	if err != nil {
		slog.ErrorContext(ctx, "穿衣建议大模型返回异常", "err", err)
		return "穿衣建议获取失败", nil // 兜底，不抛错
	}
	if len(resp.Choices) == 0 {
		return "穿衣建议获取失败", nil
	}
	return resp.Choices[0].Message.Content, nil
}
