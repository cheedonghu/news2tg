// Package config 负责命令行参数解析和 TOML 配置加载。
package config

import (
	"flag" // Go 标准库的命令行参数解析（轻量，不像 Cobra 那么重）

	// BurntSushi/toml 是社区主流的 TOML 解析库。
	// 通过 struct tag 把 TOML 字段映射到 Go 字段（类似 JSON 的 `json:"xxx"`）。
	"github.com/BurntSushi/toml"
)

// Cli 保存命令行参数解析结果。
// 字段首字母大写 = 包外可见（main.go 要读 cli.Config）。
type Cli struct {
	Config string // -c / -config，配置文件路径
	Output string // -o / -output，日志输出路径（目前没用到，预留）
}

// ParseCli 解析命令行参数，返回填好的 *Cli。
// 注意：flag.Parse() 全局只能调一次；写在这个函数里是约定，调用方在 main 里调一次就行。
func ParseCli() *Cli {
	c := &Cli{}

	// flag.StringVar 把命令行字符串参数绑定到一个变量地址：
	//   参数 1：变量地址（必须是指针，所以传 &c.Config）
	//   参数 2：参数名（命令行写 -c 或 --c 都行）
	//   参数 3：默认值
	//   参数 4：帮助文案（-h/-help 显示）
	//
	// 这里给同一个字段绑了短名和长名两次（-c 和 --config 都能用），
	// 是手动模拟"alias"的常见做法（flag 包不直接支持 alias）。
	flag.StringVar(&c.Config, "c", "config.toml", "Path to the configuration file")
	flag.StringVar(&c.Config, "config", "config.toml", "Path to the configuration file")
	flag.StringVar(&c.Output, "o", "output.log", "Path to output log file")
	flag.StringVar(&c.Output, "output", "output.log", "Path to output log file")

	// 真正解析 os.Args，把值写进上面绑定的地址。
	flag.Parse()
	return c
}

// Features 对应 config.toml 中的 [features] 段。
//
// 反引号里的内容叫"struct tag"，是给反射看的元信息。
// `toml:"v2ex_fetch_latest"` 告诉 TOML 库："这个字段对应 TOML 文件里的 v2ex_fetch_latest"。
// 没有 tag 的话，库会按字段名（大小写规则因库而异）去匹配，不可控。
type Features struct {
	V2exFetchLatest         bool     `toml:"v2ex_fetch_latest"`
	V2exFetchLatestKeyword  []string `toml:"v2ex_fetch_latest_keyword"`
	V2exFetchLatestNodeName []string `toml:"v2ex_fetch_latest_node_name"`
	V2exFetchHot            bool     `toml:"v2ex_fetch_hot"`
	HnFetchTop              bool     `toml:"hn_fetch_top"`
	HnFetchLatest           bool     `toml:"hn_fetch_latest"`
	HnFetchNum              int      `toml:"hn_fetch_num"`
	HnFetchTimeGap          int      `toml:"hn_fetch_time_gap"`
}

// Telegram 段：bot token + 目标 chat + 指令白名单。
type Telegram struct {
	APIToken string `toml:"api_token"`
	ChatID   string `toml:"chat_id"` // 用字符串存，避免 TOML int 溢出/格式问题；main 里再 ParseInt
	// AdminIDs：允许给 bot 发 /summary 指令的 user id 列表。
	// 同 chat_id 用字符串存（防御 TOML int 问题），main 里逐个 ParseInt。
	AdminIDs []string `toml:"admin_ids"`
}

// DeepSeek 段：AI 摘要服务的 key。
type DeepSeek struct {
	APIToken string `toml:"api_token"`
}

// Jina 段：jina reader(r.jina.ai) 的 key，agent 回退提取渠道用。
// 留空则走免认证公开端点（速率限制更严）。
type Jina struct {
	APIToken string `toml:"api_token"`
}

// Config 是顶层配置结构，对应整个 config.toml。
// 字段名前的 toml tag 把 Go 字段映射到 TOML 的 [table] 名。
type Config struct {
	Telegram Telegram `toml:"telegram"`
	Features Features `toml:"features"`
	DeepSeek DeepSeek `toml:"deepseek"`
	Jina     Jina     `toml:"jina"`
}

// FromFile 读取并解析配置文件。
// 返回 (*Config, error)：成功返回填好的指针 + nil；失败返回 nil + error。
func FromFile(path string) (*Config, error) {
	var cfg Config // 零值结构体；所有字段默认零值
	// toml.DecodeFile：第二个参数必须是指针（&cfg），库才能写入。
	// 第一个返回值是 MetaData（哪些 key 被识别等），这里用 _ 丢弃。
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
