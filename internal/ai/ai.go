// Package ai 定义 AI 摘要的统一接口，子文件实现具体厂商（目前只有 deepseek.go）。
//
// 抽接口的目的同 notify：业务代码（monitor）只依赖接口；
// 将来想换 OpenAI / Claude / Ollama，只需新加实现，不用改调用方。
package ai

import "context"

// Helper 是所有 AI 摘要服务要实现的接口。
//   - 入参 ctx       便于 cancel/超时
//   - 入参 content   原始正文（已经从网页抽出来）
//   - 返回 string    摘要文本；失败时实现可以返回兜底字符串而不是空
//   - 返回 error     失败原因
//
// 任何类型只要有这个方法，就自动是 Helper —— Go 接口是隐式的。
type Helper interface {
	Summarize(ctx context.Context, content string) (string, error)
}
