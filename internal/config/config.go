// Package config 负责命令行参数解析和 TOML 配置加载。
package config

import (
	"flag"     // Go 标准库的命令行参数解析（轻量，不像 Cobra 那么重）
	"fmt"      // 新增：错误包装
	"strconv"  // 新增：字符串转 int64
	"strings"  // 新增：按冒号分割 / TrimSpace

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
	// —— 每日天气推送 ——
	WeatherEnabled  bool     `toml:"weather_enabled"`   // 总开关；false 时 weather monitor 直接退出
	WeatherPushTime string   `toml:"weather_push_time"` // "HH:MM"，空/非法回落 "07:00"
	WeatherCities   []string `toml:"weather_cities"`    // 城市编码列表，如 "101010100"=北京
	WeatherMention  []string `toml:"weather_mention"`   // @ 提及列表，"id" 或 "id:显示名"；与 admin_ids 无关
}

// Telegram 段：bot token + 目标 chat + 指令白名单。
type Telegram struct {
	APIToken string `toml:"api_token"`
	ChatID   string `toml:"chat_id"` // 用字符串存，避免 TOML int 溢出/格式问题；main 里再 ParseInt
	// AdminIDs：允许给 bot 发 /summary 指令的 user id 列表。
	// 同 chat_id 用字符串存（防御 TOML int 问题），main 里逐个 ParseInt。
	AdminIDs []string `toml:"admin_ids"`
}

// DeepSeek 段：AI 摘要服务的 key 和模型名。
// 两个模型分开配是有意的：agent 走 function calling，必须用支持 tools 的模型；
// Summarize/Advise 只是纯文本生成，用便宜的轻量模型就够。
// 合成一个字段的话，换轻量模型时会顺手把 agent 的工具调用打挂。
type DeepSeek struct {
	APIToken   string `toml:"api_token"`
	Model      string `toml:"model"`       // Summarize / Advise 用
	AgentModel string `toml:"agent_model"` // agent 用，必须支持 function calling
}

// Jina 段：jina reader(r.jina.ai) 的 key，agent 回退提取渠道用。
// 留空则走免认证公开端点（速率限制更严）。
type Jina struct {
	APIToken string `toml:"api_token"`
}

// Storage 段：推送记录数据库文件路径。
// 和模型名一样不留 Go 侧默认值 —— "库放哪"只有配置文件一个答案，缺了就启动失败。
type Storage struct {
	DBPath string `toml:"db_path"`
}

// Music 段：/music 指令的 WebDAV 上传目标与凭据。
//
// 为什么是六个平铺字段而不是一个数组？
// 需求是"固定两个目标、每次都传"，不需要动态列表；平铺字段最直白，
// 也不用为 TOML 数组解析写额外代码。Go 侧组装成 []music.Target 之后，
// Uploader 内部仍然是按切片循环的，将来加第三个网盘只需在这里加两个字段。
//
// 凭据只写在 myconfig.toml（已 gitignore）里 —— config.toml 是进 git 的模板，
// 仓库又是公开的，密码写进去等于直接泄漏。
type Music struct {
	WebdavUser  string `toml:"webdav_user"`
	WebdavPass  string `toml:"webdav_pass"`
	WebdavName1 string `toml:"webdav_name_1"`
	WebdavURL1  string `toml:"webdav_url_1"`
	WebdavName2 string `toml:"webdav_name_2"`
	WebdavURL2  string `toml:"webdav_url_2"`
}

// fields 把六个字段收成一张「配置项名 → 值」表，供 Configured/validate 共用，
// 避免两处各写一遍字段清单（加字段时只改这里一处）。
// 返回切片而不是 map：要保证报错时字段顺序稳定，map 遍历顺序是随机的。
func (m Music) fields() []struct {
	key string
	val string
} {
	return []struct {
		key string
		val string
	}{
		{"webdav_user", m.WebdavUser},
		{"webdav_pass", m.WebdavPass},
		{"webdav_name_1", m.WebdavName1},
		{"webdav_url_1", m.WebdavURL1},
		{"webdav_name_2", m.WebdavName2},
		{"webdav_url_2", m.WebdavURL2},
	}
}

// Configured 报告 [music] 是否配置完整。
// 全空 = 功能关闭（不是错误）；FromFile 已经保证不会出现"填一半"的状态，
// 所以这里只需判断第一个字段非空即可 —— 但为了不依赖那个隐含前提，仍逐项检查。
func (m Music) Configured() bool {
	for _, f := range m.fields() {
		if strings.TrimSpace(f.val) == "" {
			return false
		}
	}
	return true
}

// validate 执行「要么全空，要么全填」的校验。
//   - 全空   → 功能关闭，返回 nil（现有部署没有 [music] 段，不能因升级就起不来）
//   - 全填   → 返回 nil
//   - 填一半 → 返回 error，列出缺了哪些项，启动即失败
//
// 这里判断"是否为空"用的是原始值（f.val == ""），不像 Configured() 那样 TrimSpace：
// 只要用户敲了字符（哪怕只是空格），就说明这一项"动过"，视为"填了"；
// 否则漏填一项、恰好又打成纯空格，会被误判成"整段没配"而放过校验。
func (m Music) validate() error {
	var missing []string
	filled := 0
	for _, f := range m.fields() {
		if f.val == "" {
			missing = append(missing, f.key)
		} else {
			filled++
		}
	}
	// filled == 0 是「整段没配」，功能关闭；len(missing) == 0 是「配全了」。
	if filled == 0 || len(missing) == 0 {
		return nil
	}
	// strings.Join 把切片按分隔符拼成一句话，比循环拼字符串直观。
	return fmt.Errorf("[music] 段配置不完整，缺少：%s（该段要么整段不配、要么六项全配）", strings.Join(missing, ", "))
}

// Config 是顶层配置结构，对应整个 config.toml。
// 字段名前的 toml tag 把 Go 字段映射到 TOML 的 [table] 名。
type Config struct {
	Telegram Telegram `toml:"telegram"`
	Features Features `toml:"features"`
	DeepSeek DeepSeek `toml:"deepseek"`
	Jina     Jina     `toml:"jina"`
	Storage  Storage  `toml:"storage"` // 新增
	Music    Music    `toml:"music"`   // 新增；可选功能，全空即关闭
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
	// 模型名故意不在 Go 代码里留兜底默认值：缺配置就启动失败，
	// 这样「模型名写在哪」只有一个答案——配置文件。
	// TrimSpace 是防御误填空格（TOML 里 model = "  " 也算填了）。
	if strings.TrimSpace(cfg.DeepSeek.Model) == "" {
		return nil, fmt.Errorf("[deepseek] model 未配置")
	}
	if strings.TrimSpace(cfg.DeepSeek.AgentModel) == "" {
		return nil, fmt.Errorf("[deepseek] agent_model 未配置")
	}
	if strings.TrimSpace(cfg.Storage.DBPath) == "" {
		return nil, fmt.Errorf("[storage] db_path 未配置（本次升级新增的必填项，参考 config.toml 模板）")
	}
	// [music] 是可选功能：全空则 /music 不可用，但不阻止启动；
	// 填一半则报错 —— 半配置状态一定是打字漏了。
	if err := cfg.Music.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Mention 是「每日天气要 @ 的人」的解析结果。
// 它是 weather_mention 配置（[]string）的派生形态：raw 是 "id" 或 "id:显示名"，
// 解析成结构化的 ID + 显示名。放 config 包是因为它属于配置派生数据，
// 且 monitor 已经 import config，直接用不产生导入环。
type Mention struct {
	ID   int64  // Telegram 用户数字 id
	Name string // @ 时显示的文字；缺省回落 "管理员"
}

// ParseMention 把一条 weather_mention 配置解析成 Mention。
//   - "123456"       → {123456, "管理员"}
//   - "234567:老王"  → {234567, "老王"}
// 规则：按第一个 ':' 分割；冒号后为空 / 无冒号 → 显示名回落 "管理员"；id 非法返回 error。
func ParseMention(s string) (Mention, error) {
	idPart := s     // 冒号左边（或整串）当 id
	name := "管理员" // 默认显示名
	// strings.Index 找第一个 ':' 的下标，找不到返回 -1。
	if i := strings.Index(s, ":"); i >= 0 {
		idPart = s[:i]
		// TrimSpace 去掉两端空白；非空才覆盖默认显示名。
		if n := strings.TrimSpace(s[i+1:]); n != "" {
			name = n
		}
	}
	// ParseInt(串, 进制, 位宽)：解析失败返回 error，交给调用方 log 跳过。
	id, err := strconv.ParseInt(strings.TrimSpace(idPart), 10, 64)
	if err != nil {
		return Mention{}, fmt.Errorf("非法 mention %q: %w", s, err)
	}
	return Mention{ID: id, Name: name}, nil
}
