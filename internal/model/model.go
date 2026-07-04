// Package model 放共享的数据结构（DTO/POJO 的角色）。
// 单独抽包是为了避免循环依赖：notify、monitor、ai 都可能用到这些类型。
package model

// NotifyBase 是"准备发送的一条通知"的统一表示。
// 不同来源（v2ex 热帖/新帖、hn 帖子）都最终落到这个结构再交给 notify 推送。
//
// 字段全部首字母大写 = 包外可见（其它包要读写）。
// 没有 toml/json tag：因为它不是从配置或网络反序列化来的，是程序内部用。
type NotifyBase struct {
	URL                       string // 帖子主页 URL
	OriginURL                 string // 原文链接（HN 才有，v2ex 是空）
	Title                     string // 标题（同时也用作消息头里的一段）
	Content                   string // 最终要发送的消息正文（已经拼好模板）
	ContentTransferedByAIFlag bool   // 标记：true=已交给 AI 处理过 / false=未处理（兜底文案）
}

// 下面三个结构对应 v2ex API 返回的 JSON。
// `json:"name"` tag 告诉 encoding/json 库 Go 字段对应的 JSON key。
// 没 tag 的话，库默认按"字段名小写"匹配，不可靠。

// Node 是 v2ex 的"节点"（板块）信息。
type Node struct {
	Name  string `json:"name"`  // 英文短名，比如 "share"，过滤就靠这个
	Title string `json:"title"` // 中文显示名
	URL   string `json:"url"`
	ID    uint64 `json:"id"`
}

// Member 是 v2ex 的发帖人信息。
// 当前代码没用到所有字段，但留着方便以后扩展。
type Member struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	URL      string `json:"url"`
}

// Topic 是 v2ex 的一条帖子。
type Topic struct {
	ID      uint64  `json:"id"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content *string `json:"content"` // 用 *string 而不是 string：因为 JSON 里这个字段可能是 null
	Replies uint64  `json:"replies"`
	Created uint64  `json:"created"` // Unix 秒时间戳
	Node    Node    `json:"node"`    // 嵌套结构体：JSON 里是个对象
	Member  Member  `json:"member"`
}

// 关于 *string 的小知识：
//   - string 的零值是 ""，没法区分"没传"和"传了空串"。
//   - *string 的零值是 nil，可以用 `if topic.Content == nil` 判断"没传"。
// 仅当真的需要区分这两种情况才用指针，否则用 string 更简单。
