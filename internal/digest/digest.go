// Package digest 定义"网页正文抽取"的统一接口，子文件实现具体渠道。
//
// 抽接口的目的同 ai / notify：业务代码（monitor）只依赖接口；
// 将来想换不同的正文抽取渠道（agent、其它 sidecar 等），只需新加实现，不用改调用方。
package digest

import "context"

// Fetcher 抽象"给定原文链接，取回正文摘要"。
//   - 入参 ctx        便于 cancel/超时
//   - 入参 originURL  原文网页链接
//   - 返回 string     抽取出来的正文/摘要文本
//   - 返回 error      失败原因（调用方据此走兜底，不影响推送）
//
// 任何类型只要有这个方法，就自动是 Fetcher —— Go 接口是隐式的。
// 目前实现：Python sidecar（python.go）；后续可加 agent 等渠道。
type Fetcher interface {
	Fetch(ctx context.Context, originURL string) (string, error)
}
