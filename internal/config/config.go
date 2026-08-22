// Package config 负责命令行参数解析和 TOML 配置加载。
package config

import (
	"flag"     // Go 标准库的命令行参数解析（轻量，不像 Cobra 那么重）
	"fmt"      // 新增：错误包装
	"net/url"  // 新增：校验 [network] proxy 地址
	"sort"     // 新增：sources 校验报错信息里给合法音源名排序
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

// Network 段：进程级的 HTTP 代理开关。
//
// 为什么是"要么全走要么全不走"而不是按主机分流？
// 分流规则属于代理程序（clash / v2ray 之类）的职责，它们做得比我们好得多；
// 在这里再实现一套 no_proxy 只会多一处需要同步维护的规则表。
//
// 这一段是可选的：不配即全部直连，行为与引入本字段之前完全一致。
type Network struct {
	Proxy string `toml:"proxy"` // 形如 http://127.0.0.1:7890 或 socks5://…；空 = 直连
}

// Music 段：/music 指令的 WebDAV 上传目标与凭据。
//
// 为什么是六个平铺字段而不是一个数组？
// 需求是"固定至多两个目标、每次都传"，不需要动态列表；平铺字段最直白，
// 也不用为 TOML 数组解析写额外代码。Go 侧组装成 []music.Target 之后，
// Uploader 内部仍然是按切片循环的，将来加第三个网盘只需在这里加两个字段。
//
// 两个目标**不要求都配**：只配一个是合法的（现实里常有一个网盘临时挂掉）。
// 但"半个目标"（有 name 没 url，或反之）仍然报错 —— 那一定是打字漏了。
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
	// Sources 是启用的音源列表，**顺序即优先级**（模型按这个顺序依次尝试）。
	// 不在列表里 = 关闭。缺省（不写这一行）= DefaultMusicSources 全部启用。
	//
	// 为什么用一个有序数组而不是"开关字段 + 优先级字段"：
	// 一个字段同时表达两件事，就不可能出现"开了但没给优先级"
	// 或"两个源抢同一个优先级"这种自相矛盾的配置。
	Sources []string `toml:"sources"`
}

// DefaultMusicSources 是不写 sources 时的默认启用列表，顺序即优先级。
//
// musicso 排在前面：它是中文站、歌名歌手都是中文原名，覆盖中文歌远好于
// mp3.pm（俄语站，中文歌按拼音收录，得靠模型猜拼音才搜得到）。
// mp3.pm 留作兜底：musicso 在 Cloudflare 后面，出口 IP 被判成机器人时会整体不可用。
var DefaultMusicSources = []string{"musicso", "mp3pm"}

// knownMusicSources 是所有合法的音源名。
// 用 map 而不是切片：校验时要按名字查，O(1) 比线性扫直观。
// 加音源时改这里和 DefaultMusicSources 两处，以及 main 里的工厂表。
var knownMusicSources = map[string]bool{
	"musicso": true,
	"mp3pm":   true,
}

// EffectiveSources 返回实际生效的音源列表。
//
// 缺省（长度为 0）时回落到默认全启用 —— 注意这里不会返回空列表：
// 显式写 sources = [] 已经在 validate() 里变成启动失败了，
// 所以能走到这儿的空值只可能是"根本没写这一行"。
//
// 返回副本而不是内部切片：调用方（main）拿去构造音源时不该有能力
// 改到配置本身。append(nil, s...) 是 Go 里拷贝切片的惯用写法。
func (m Music) EffectiveSources() []string {
	if len(m.Sources) == 0 {
		return append([]string(nil), DefaultMusicSources...)
	}
	return append([]string(nil), m.Sources...)
}

// validateSources 校验 sources 数组。
// 只在 [music] 段确实在用时才被调用（见 validate）。
func (m Music) validateSources() []string {
	// 完全没写这一行 → 走默认，没什么可校验的。
	// 注意 TOML 里 sources = [] 解析出来同样是长度 0 的切片，与"没写"无法区分，
	// 所以那个 case 由 FromFile 里的 md.IsDefined("music", "sources") 单独兜住
	// （见 FromFile 里对应的判断处，而不是这个函数）。
	if len(m.Sources) == 0 {
		return nil
	}
	var problems []string
	seen := make(map[string]bool, len(m.Sources))
	for i, s := range m.Sources {
		name := strings.TrimSpace(s)
		if name == "" {
			problems = append(problems, fmt.Sprintf("sources 第 %d 项是空值", i+1))
			continue
		}
		if !knownMusicSources[name] {
			problems = append(problems, fmt.Sprintf("sources 里的 %q 不是已知音源（可选：%s）",
				name, strings.Join(sortedKnownSources(), ", ")))
			continue
		}
		if seen[name] {
			problems = append(problems, fmt.Sprintf("sources 里的 %q 重复出现", name))
			continue
		}
		seen[name] = true
	}
	return problems
}

// sortedKnownSources 返回排好序的合法音源名，仅用于拼错误信息。
// 必须排序：map 遍历顺序是随机的，不排的话同一个错误每次报出来顺序都不同。
func sortedKnownSources() []string {
	out := make([]string, 0, len(knownMusicSources))
	for k := range knownMusicSources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fields 把六个字段收成一张「配置项名 → 值」表，供 touched 判定共用，
// 避免多处各写一遍字段清单（加字段时只改这里一处）。
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

// targetState 描述一个上传目标的配置完整度。
type targetState int

const (
	targetEmpty      targetState = iota // name 和 url 都是空白 —— 该目标没配，合法
	targetComplete                      // name 和 url 都有可用值 —— 该目标可用
	targetHalfBaked                     // 只有一项有值 —— 一定是打字漏了，必须报错
)

// classifyTarget 判定一个目标的三态。
// 抽成函数是因为两个目标要各判一次，而这条规则一旦两处各写一遍就会走样。
func classifyTarget(name, url string) targetState {
	n, u := usable(name), usable(url)
	switch {
	case n && u:
		return targetComplete
	case !n && !u:
		return targetEmpty
	default:
		return targetHalfBaked
	}
}

// touched 判断某一项"用户是否敲过字符"：只要原始值非空就算，哪怕只是纯空格。
// 用来判断整段 [music] 到底有没有在用。
// 注意不能用 TrimSpace 判断——"六项全打成空格"如果被当成"没碰过"，
// 就会被 validate() 放行成"整段没配"，而这其实和"填一半"一样是打字失误，
// 理应报错，不该被静默放过。
func touched(val string) bool {
	return val != ""
}

// usable 判断某一项的值 TrimSpace 之后是否真的能用——纯空格 trim 完是空串，算不能用。
// Configured() 用它判断功能能不能跑；validate() 用它判断"填了的项里有没有废的"。
func usable(val string) bool {
	return strings.TrimSpace(val) != ""
}

// Configured 报告 /music 功能是否可用：凭据齐全，且至少有一个完整的上传目标。
//
// 只要经过 validate() 校验成功返回的 cfg，Configured() 就只有两种结果：
// 要么可用，要么整段没碰（功能关闭）—— 不会存在"进程正常启动了，
// 但其实只是半配置、Configured() 悄悄是 false"这种状态；
// 那种状态在 validate() 里已经变成 FromFile 报错、根本起不来了。
func (m Music) Configured() bool {
	if !usable(m.WebdavUser) || !usable(m.WebdavPass) {
		return false
	}
	return classifyTarget(m.WebdavName1, m.WebdavURL1) == targetComplete ||
		classifyTarget(m.WebdavName2, m.WebdavURL2) == targetComplete
}

// validate 执行「要么整段不碰，要么配成一个能用的样子」的校验：
//   - 零项 touched → 整段没配，功能关闭，返回 nil
//     （现有部署没有 [music] 段，不能因为这次升级就启动失败）
//   - 碰了，但 user/pass 缺失、或某个目标是半个、或一个完整目标都没有 → error
//   - 否则 → nil
//
// 这里故意拆成两个谓词：先用 touched（原始值非空）判断"这一段是不是在用"，
// 再用 usable（TrimSpace 非空）判断"填的东西能不能用"。只用一个会顾此失彼——
// 全用 TrimSpace 的话，"其余真值 + 一项纯空格"里那个空格项会被当成"没填"，
// 和其余全空的字段混在一起，误判成"整段没配"而放行，管理员会拿着一个
// 看似正常启动、实则 /music 静默不可用的进程去抓瞎；全用原始值的话，
// "全打成空格"又会被当成"都碰过"，可实际一个能用的值都没有，同样不该被放过。
func (m Music) validate() error {
	anyTouched := false
	for _, f := range m.fields() {
		if touched(f.val) {
			anyTouched = true
			break
		}
	}
	// sources 也算"碰过这一段"。不加这条的话，"只写了 sources、webdav 全忘"
	// 会被判成整段没配而静默放行，管理员拿到一个看似正常启动、
	// 实则 /music 悄悄不可用的进程。
	if len(m.Sources) > 0 {
		anyTouched = true
	}
	if !anyTouched {
		return nil // 整段没配，功能关闭
	}

	// problems 收集所有毛病后一次性报出来，而不是遇到第一个就返回 ——
	// 让改配置的人一轮就能全改对，不用来回试。
	var problems []string
	if !usable(m.WebdavUser) {
		problems = append(problems, "webdav_user 未填")
	}
	if !usable(m.WebdavPass) {
		problems = append(problems, "webdav_pass 未填")
	}

	s1 := classifyTarget(m.WebdavName1, m.WebdavURL1)
	s2 := classifyTarget(m.WebdavName2, m.WebdavURL2)
	if s1 == targetHalfBaked {
		problems = append(problems, "目标 1 只配了一半（webdav_name_1 与 webdav_url_1 必须同时填写）")
	}
	if s2 == targetHalfBaked {
		problems = append(problems, "目标 2 只配了一半（webdav_name_2 与 webdav_url_2 必须同时填写）")
	}
	if s1 != targetComplete && s2 != targetComplete {
		problems = append(problems, "至少需要一个完整的上传目标（name + url）")
	}

	problems = append(problems, m.validateSources()...)

	if len(problems) == 0 {
		return nil
	}
	// strings.Join 把切片按分隔符拼成一句话，比循环拼字符串直观。
	return fmt.Errorf("[music] 段配置有误：%s", strings.Join(problems, "；"))
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
	Network  Network  `toml:"network"` // 新增；可选，空即全部直连
}

// FromFile 读取并解析配置文件。
// 返回 (*Config, error)：成功返回填好的指针 + nil；失败返回 nil + error。
func FromFile(path string) (*Config, error) {
	var cfg Config // 零值结构体；所有字段默认零值
	// toml.DecodeFile：第二个参数必须是指针（&cfg），库才能写入。
	// 第一个返回值是 MetaData（哪些 key 被识别等），这里接出来给下面 sources 用：
	// 要区分"写了 sources = []"和"根本没写 sources"，只能靠它。
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
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
	// [network] proxy 在这里就地校验：非法地址应当在启动时暴露，
	// 而不是等到运行时第一个 HTTP 请求失败——那时错误离病根已经很远。
	// 空串是合法的（表示全部直连），所以先判空再解析。
	if p := strings.TrimSpace(cfg.Network.Proxy); p != "" {
		u, perr := url.Parse(p)
		// url.Parse 对很多畸形串是宽容的（不报错但字段为空），所以必须
		// 额外检查 Scheme 和 Host —— "127.0.0.1:7890" 会被解析成
		// Scheme="127.0.0.1"、Opaque="7890"，Host 为空，拿去当代理必挂。
		if perr != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("[network] proxy 不是合法的代理地址（需形如 http://host:port 或 socks5://host:port）: %q", cfg.Network.Proxy)
		}
	}
	// md.IsDefined 能区分"写了 sources = []"和"根本没写 sources" ——
	// 前者是显式关掉所有音源，那样 /music 必然什么都搜不到，
	// 属于关错了地方，应当启动失败而不是留个残废功能给人用。
	if md.IsDefined("music", "sources") && len(cfg.Music.Sources) == 0 {
		return nil, fmt.Errorf("[music] sources 是空数组：这会关掉所有音源、让 /music 必然搜不到东西。"+
			"若要关闭 /music 请整个删掉 [music] 段；若要启用音源请列出：%s",
			strings.Join(sortedKnownSources(), ", "))
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
