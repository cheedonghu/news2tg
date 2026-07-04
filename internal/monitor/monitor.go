// Package monitor 定义抓取任务的统一抽象。
// ↑ Go 规范：包注释写在 package 行的正上方，第一句必须以 "Package <名字>" 开头，
//
//	`go doc` 会把这段当作包的官方说明展示。
package monitor

// import 块：括号包起来可以一次导入多个包。
// 这是分组写法，比写多行 `import "xxx"` 更常见。
import (
	"context" // 标准库：上下文，用来传 cancel 信号、超时、请求级数据

	// 第三方包：导入路径就是 go.mod 里 module 名 + 子目录路径。
	// 空行用来把"标准库"和"本项目/第三方"分组，gofmt 不会动这种分组。
	"github.com/cheedonghu/news-notify/internal/config"
)

// Monitor 是一个"接口"（interface）。Go 的接口是隐式实现的：
// 任何类型只要有名为 Run、签名一致的方法，就自动"实现"了 Monitor，不需要写 implements。
//
// 这里只声明一个方法 Run：
//   - 入参 ctx context.Context  约定俗成的第一个参数名 ctx；
//   - 入参 cfg *config.Config   指针类型（前面有 *），意味着传"地址"而不是"拷贝整个结构体"；
//   - 返回 error                Go 的错误是普通值，nil 表示成功。
type Monitor interface {
	Run(ctx context.Context, cfg *config.Config) error
}

// 实现者约定（写在注释里，让别的开发者知道怎么实现）：
//   - Run 是阻塞的：调用后会一直执行，直到 ctx 被取消或不可恢复错误才返回；
//   - 返回 ctx.Err() 表示"正常退出"（被外部 cancel）；
//   - 其它错误视作异常；
//   - 实现需要自己处理"临时"错误（log 后继续），不要把可重试错误返回出来。
