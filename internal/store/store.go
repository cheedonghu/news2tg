// Package store 负责"推送记录"的持久化。
//
// 抽接口的好处和 notify.Notifier / digest.Fetcher 一样：
//  1. 业务代码（monitor 包）只依赖接口，不依赖 SQLite，将来换存储只需新加实现；
//  2. 测试时可以传真实的临时库文件，也可以传 fake。
package store

import (
	"context" // 上下文：传 cancel/超时
	"time"    // 记录推送时刻
)

// 来源标识：写入 Record.Source 和查询 AlreadyPushed 时必须用同一个值，
// 所以提到这里定义一次 —— 两侧各写一个字符串字面量的话，
// 一旦分叉 AlreadyPushed 会永远查不到，导致每轮重推整个列表。
const (
	SourceV2EX       = "v2ex"
	SourceHackerNews = "hackernews"
)

// Record 是一条推送记录。
// 字段首字母大写 = 包外可见（monitor 要构造它）。
type Record struct {
	Source     string    // 来源标识："v2ex" | "hackernews"
	ExternalID string    // 该来源内的唯一标识：v2ex 用帖子 URL，hn 用帖子数字 id
	Title      string    // 帖子标题（不是消息抬头）
	URL        string    // 帖子主页 URL
	ChatID     string    // 推送目标 chat；配置里本就是字符串，直接透传不做转换
	PushedAt   time.Time // 发送成功的时刻
}

// Store 是推送记录存储要实现的接口。
// 任何类型只要有这些方法就自动是一个 Store，不用写 implements。
//
// 注意这里**没有** CleanOld 之类的方法：去重是永久的，记录不再按时间窗口清理。
type Store interface {
	// AlreadyPushed 查这条是否推送过。
	// 返回 (false, nil) 表示确认没推过；error 非 nil 时 bool 无意义。
	AlreadyPushed(ctx context.Context, source, externalID string) (bool, error)

	// MarkPushed 记一条推送记录。重复调用同一条是幂等的，不报错。
	MarkPushed(ctx context.Context, rec Record) error

	// Close 释放底层资源（关闭数据库连接池）。
	Close() error
}
