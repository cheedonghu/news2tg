# WebDAV 单目标 + musicso 音源 + 全局代理 · 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `[music]` 支持只配一个 WebDAV 目标，新增 musicso.cc 音源并用有序数组控制音源开关与优先级，同时引入一个全局 HTTP 代理开关。

**Architecture:** 三件事共用一次改动。音源侧照现有 `music.Source` 接口新增一个实现文件（站点特有逻辑全部关在这一个文件里，agent 循环一行不改）；配置侧把 `[music]` 的「六项全配」校验换成「至少一个完整目标」并新增 `sources` 有序数组；代理侧不动任何构造函数签名——仓库里五个自建 HTTP 客户端的 `Transport` 都是 nil，覆盖 `http.DefaultTransport` 即可全部生效，剩下那个共享 client 单独设一次。

**Tech Stack:** Go 1.25、`github.com/PuerkitoBio/goquery`（HTML 解析，仓库已在用）、`github.com/BurntSushi/toml`、标准库 `net/http` + `net/http/httptest`。

**Spec:** `docs/superpowers/specs/2026-08-22-webdav-single-target-and-musicso-source-design.md`

## Global Constraints

- **双语教学注释是本仓库的房规，不是噪音。** 几乎每一行都带中文注释解释 Go 语义。新增/修改代码时**匹配这个密度和风格**，不要精简掉注释。
- **Go ≥ 1.25**（`go.mod` 锁 `go 1.25.0`）。
- **`gofmt -l .` 天然会列出约 26 个既有文件（CRLF 行尾），不能拿它当验收门。** 只检查自己改过的文件。
- **跑测试一律带 `-short`**：`internal/agent/agent_test.go` 有个 e2e 测试会真实调用 DeepSeek API 并产生费用。
- **AI/digest 失败不阻断推送**是既有约定，本次不引入新哲学。
- **音源的站点特有逻辑只允许出现在该音源自己的文件里**（`mp3pm.go` / 新增的 `musicso.go`），不得泄漏进 `agent.go`、`runner.go`。
- **所有 musicso 请求必须带浏览器 UA**，复用 `internal/music/mp3pm.go` 里已有的包级常量 `browserUA`，否则一律被 Cloudflare 质询拦下。
- **提交信息前缀沿用仓库习惯**：`#feat` / `#fix` / `#docs`，正文中文。

---

### Task 1: 全局代理开关 `[network] proxy`

**Files:**
- Modify: `internal/config/config.go`（新增 `Network` 结构体、`Config` 加字段、`FromFile` 加校验）
- Modify: `cmd/news2tg/main.go:105-120`（共享 client 的 Transport）与其前方（`DefaultTransport` 覆盖）
- Modify: `config.toml`（新增 `[network]` 段）
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: 无（首个任务）
- Produces: `config.Network{ Proxy string }`；`Config.Network` 字段；`FromFile` 在 `proxy` 非法时返回 error。后续任务不依赖本任务。

- [ ] **Step 1: 写失败测试**

追加到 `internal/config/config_test.go` 末尾：

```go
// TestFromFileNetworkProxy 验证 [network] proxy 能被解析出来。
func TestFromFileNetworkProxy(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[network]
proxy = "http://127.0.0.1:7890"
`)))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if cfg.Network.Proxy != "http://127.0.0.1:7890" {
		t.Errorf("Network.Proxy = %q, want %q", cfg.Network.Proxy, "http://127.0.0.1:7890")
	}
}

// TestFromFileNetworkProxyAbsent 验证：不配 [network] 时不报错，Proxy 为空串（= 全部直连）。
// 现有部署没有这一段，不能因为升级就起不来。
func TestFromFileNetworkProxyAbsent(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML("")))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if cfg.Network.Proxy != "" {
		t.Errorf("未配置 [network] 时 Proxy 应为空串，实际 %q", cfg.Network.Proxy)
	}
}

// TestFromFileNetworkProxyInvalid 验证：非法代理地址在启动时就报错。
// 别等到运行时第一个 HTTP 请求才炸 —— 那时错误信息离病根已经很远了。
func TestFromFileNetworkProxyInvalid(t *testing.T) {
	cases := []struct {
		name  string
		proxy string
	}{
		// 前两个走 url.Parse 报错这条分支，第三个走"解析成功但 Host 为空"那条 ——
		// 后者正是必须额外检查 Scheme/Host 的理由：url.Parse 对它是不报错的。
		{"漏写 scheme（最常见的手误）", "127.0.0.1:7890"}, // err: first path segment cannot contain colon
		{"只有分隔符没有 scheme", "://nohost"},        // err: missing protocol scheme
		{"有 scheme 但没有主机", "http://"},           // 无 err，Scheme="http" 而 Host=""
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := FromFile(writeTempTOML(t, musicTOML(`
[network]
proxy = "`+c.proxy+`"
`)))
			if err == nil {
				t.Fatalf("proxy = %q 应该导致启动失败", c.proxy)
			}
			if !strings.Contains(err.Error(), "proxy") {
				t.Errorf("错误信息应提到 proxy，实际: %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `go test ./internal/config -run TestFromFileNetworkProxy -v`
Expected: 编译失败，`cfg.Network undefined (type Config has no field or method Network)`

- [ ] **Step 3: 实现配置结构与校验**

在 `internal/config/config.go` 的 `Storage` 结构体之后（约第 95 行）插入：

```go
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
```

在 `Config` 结构体里加字段（紧跟 `Music` 之后）：

```go
	Network  Network  `toml:"network"`  // 新增；可选，空即全部直连
```

在 `FromFile` 里、`cfg.Music.validate()` 那段**之前**插入：

```go
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
```

在 import 块里加 `"net/url"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config -run TestFromFileNetworkProxy -v`
Expected: 三个测试全 PASS

- [ ] **Step 5: 在 main 里落地代理**

在 `cmd/news2tg/main.go` 里，把第 105 行的 `httpClient := &http.Client{` 那一整块**替换**为：

```go
	// 8) 全局代理：非空时对**所有** HTTP 出站生效。
	//
	// 为什么只需要动这两处、不必改任何构造函数签名：
	// 仓库里一共六个 HTTP 客户端构造点，其中五个（notify.Telegram 的
	// &http.Client{Timeout: 30s}、tgbotapi.NewBotAPI 内部的 &http.Client{}、
	// go-openai DefaultConfig 的 &http.Client{} ×3）Transport 字段都是 nil，
	// net/http 会自动回落到 http.DefaultTransport —— 覆盖它就等于同时改到那五个。
	// 剩下的第六个就是下面这个共享 client，它自带显式 Transport，
	// 不吃 DefaultTransport，所以要单独把 Proxy 设一遍。
	//
	// 注意这段必须跑在任何客户端被构造**之前**。tgClient 在第 4 步就建好了，
	// 但 tgbotapi 只是把 *http.Client 存下来、每次请求才现取 Transport，
	// 所以在这里改仍然对它生效。
	var proxyFunc func(*http.Request) (*url.URL, error) // nil = 不走代理
	if p := strings.TrimSpace(cfg.Network.Proxy); p != "" {
		// 这里可以忽略 error：FromFile 已经校验过一遍，走到这儿必定合法。
		u, _ := url.Parse(p)
		proxyFunc = http.ProxyURL(u)
		// 类型断言：DefaultTransport 的静态类型是 http.RoundTripper 接口，
		// 要拿到 Proxy 字段得先断言回具体的 *http.Transport。
		http.DefaultTransport.(*http.Transport).Proxy = proxyFunc
		slog.Info("全局 HTTP 代理已启用", "proxy", p)
	}

	// 8.1) 共享 HTTP 客户端：连接池、超时配置全集中在这里。
	// &http.Client{...} 取地址：拿到 *http.Client 指针，方便共享同一个连接池。
	httpClient := &http.Client{
		//Timeout: 5 * time.Minute, // 整个请求总超时
		Transport: &http.Transport{
			Proxy: proxyFunc, // nil 时等价于不走代理

			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 5, // 仍然要配，复用连接省握手
			IdleConnTimeout:     150 * time.Second,

			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second, // 建连仍然短！故障快速失败
				KeepAlive: 30 * time.Second,
			}).DialContext,

			ResponseHeaderTimeout: 180 * time.Second, // 看是否流式调整：流式可短，非流式要长
			ForceAttemptHTTP2:     false,
		},
	}
```

在 `main.go` 的 import 块里补 `"net/url"` 和 `"strings"`（`strings` 若已存在则跳过）。

- [ ] **Step 6: 编译并跑全量测试**

Run: `go build ./... && go vet ./... && go test -short ./...`
Expected: 编译通过，vet 无输出，测试全 PASS

- [ ] **Step 7: 更新配置模板**

在 `config.toml` 末尾追加：

```toml
# 全局 HTTP 代理。留空 = 全部直连（默认，与不写这一段等价）。
# 非空时对所有出站请求生效：Telegram、DeepSeek、V2EX、HN、音源、WebDAV 一视同仁。
# 按主机分流请在代理程序（clash / v2ray 等）的规则里做，这里不提供 no_proxy。
# 注意：HN 摘要走的是 http://127.0.0.1:50051 的 Python sidecar，
# 若你的代理程序会把 127.0.0.1 也代理出去，请在它的规则里给本机地址留直连。
[network]
proxy = ""
```

- [ ] **Step 8: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/news2tg/main.go config.toml
git commit -m "#feat 新增全局 HTTP 代理开关 [network] proxy

五个自建 client 的 Transport 都是 nil、会回落到 http.DefaultTransport，
覆盖它即可全部生效；共享 client 自带显式 Transport 另设一次。因此不必
改动 ai/agent/music/notify/command 任何一个构造函数签名。

非法代理地址在 FromFile 就报错，不留到运行时第一个请求才炸。"
```

---

### Task 2: `[music]` WebDAV 校验改成「至少一个完整目标」

**Files:**
- Modify: `internal/config/config.go`（`Music.fields` / `Configured` / `validate`）
- Modify: `cmd/news2tg/main.go:174-178`（targets 只收完整的）
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: 无（与 Task 1 独立）
- Produces: `Music.Configured() bool` 语义变为「user/pass 有值 **且** 至少一个完整目标」；包内新增 `type targetState int`、常量 `targetEmpty` / `targetComplete` / `targetHalfBaked`、函数 `classifyTarget(name, url string) targetState`。
  这些**都未导出**，`package main` 用不了，所以 main 侧判断目标是否要收时直接判字段非空白（见 Step 5）。

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 里，**保留** `TestFromFileMusicAbsent` 与 `TestFromFileMusicComplete` 不动，把 `TestFromFileMusicPartial` 整个替换成下面这一组：

```go
// TestFromFileMusicSingleTarget 验证：只配第一个目标是合法的。
// 这是本次改动的核心 —— 原先「六项全配」会让它启动失败。
func TestFromFileMusicSingleTarget(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
`)))
	if err != nil {
		t.Fatalf("只配一个目标时 FromFile 不该报错: %v", err)
	}
	if !cfg.Music.Configured() {
		t.Fatal("只配一个完整目标时 Configured() 应为 true")
	}
	if cfg.Music.WebdavName2 != "" || cfg.Music.WebdavURL2 != "" {
		t.Errorf("第二个目标应为空，实际 name2=%q url2=%q", cfg.Music.WebdavName2, cfg.Music.WebdavURL2)
	}
}

// TestFromFileMusicSecondTargetOnly 验证：只配第二个目标同样合法。
// 校验规则说的是"至少一个完整目标"，不是"第一个必须配"。
func TestFromFileMusicSecondTargetOnly(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://alist:5244/dav/onedrive/Music"
`)))
	if err != nil {
		t.Fatalf("只配第二个目标时 FromFile 不该报错: %v", err)
	}
	if !cfg.Music.Configured() {
		t.Fatal("只配第二个完整目标时 Configured() 应为 true")
	}
}

// TestFromFileMusicInvalid 锁死所有应当启动失败的配置形态。
// 半个目标一定是打字漏了；静默降级只会让人对着「上传失败」抓瞎。
func TestFromFileMusicInvalid(t *testing.T) {
	cases := []struct {
		name    string
		section string
	}{
		{
			name: "只填了凭据，一个目标都没有",
			section: `
[music]
webdav_user = "alist"
webdav_pass = "pw"
`,
		},
		{
			name: "有目标但缺 user",
			section: `
[music]
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
`,
		},
		{
			name: "有目标但缺 pass",
			section: `
[music]
webdav_user   = "alist"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
`,
		},
		{
			name: "第一个目标只有 name 没有 url（半个目标）",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
`,
		},
		{
			name: "目标 1 完整，目标 2 是半个（仍要报错）",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
`,
		},
		{
			name: "只有空白字符也算填了，不能被当成没配",
			section: `
[music]
webdav_user = "   "
`,
		},
		{
			// 五项真值 + 一项纯空格：那个空格项不能被混进"没填"里，
			// 否则整段会被误判成"没配"而放行，管理员拿到一个看似正常启动、
			// 实则 /music 静默不可用的进程。
			name: "其余齐全但 url_1 是纯空格",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "   "
`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := FromFile(writeTempTOML(t, musicTOML(c.section))); err == nil {
				t.Fatal("该配置应当导致启动失败，却成功了")
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `go test ./internal/config -run 'TestFromFileMusic' -v`
Expected: `TestFromFileMusicSingleTarget` 与 `TestFromFileMusicSecondTargetOnly` FAIL（现行校验要求六项全配）

- [ ] **Step 3: 重写校验逻辑**

在 `internal/config/config.go` 里，把 `Music` 结构体上方的注释块、以及 `fields()` / `Configured()` / `validate()` 三个方法整体替换为：

```go
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

	if len(problems) == 0 {
		return nil
	}
	// strings.Join 把切片按分隔符拼成一句话，比循环拼字符串直观。
	return fmt.Errorf("[music] 段配置有误：%s", strings.Join(problems, "；"))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config -v`
Expected: 全 PASS（含原有的 `TestFromFileMusicAbsent` / `TestFromFileMusicComplete`）

- [ ] **Step 5: main 侧只收完整目标**

在 `cmd/news2tg/main.go` 里，把第 173-178 行（`// 两个固定目标；…` 到 `uploader := …`）替换为：

```go
		// 上传目标：只收**完整**的那些。
		// 配置校验已经保证"至少有一个完整目标、且不存在半个目标"，
		// 所以这里只需判空，不必再处理半配置的情况。
		// Uploader 内部按切片循环，1 个和 2 个走的是同一条代码路径。
		targets := make([]music.Target, 0, 2)
		if strings.TrimSpace(cfg.Music.WebdavURL1) != "" {
			targets = append(targets, music.Target{Name: cfg.Music.WebdavName1, URL: cfg.Music.WebdavURL1})
		}
		if strings.TrimSpace(cfg.Music.WebdavURL2) != "" {
			targets = append(targets, music.Target{Name: cfg.Music.WebdavName2, URL: cfg.Music.WebdavURL2})
		}
		uploader := music.NewUploader(httpClient, targets, cfg.Music.WebdavUser, cfg.Music.WebdavPass)
```

- [ ] **Step 6: 补 Uploader 的单目标测试**

追加到 `internal/music/upload_test.go` 末尾：

这两个测试沿用 `upload_test.go` 里已有的 `davRecorder`（假 WebDAV 服务端，
`.handler()` 返回 `http.HandlerFunc`，`status` 字段控制返回码，`0` 视作 201）
与 `writeTempMP3(t, content)` helper，**不要新建 helper**：

```go
// TestUploadSingleTargetSucceeds 验证只有一个目标时上传正常。
// 这条路径在校验改成"至少一个完整目标"之前，配置层根本到不了。
func TestUploadSingleTargetSucceeds(t *testing.T) {
	rec := &davRecorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	u := NewUploader(srv.Client(), []Target{
		{Name: "阿里云盘", URL: srv.URL + "/dav/aliyun/Music"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "ID3fake"), "a.mp3", nil)
	if err != nil {
		t.Fatalf("单目标上传意外失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 个目标状态, want 1", len(got))
	}
	if got[0].State != TargetOK {
		t.Errorf("状态 = %d, want TargetOK；err=%s", got[0].State, got[0].Err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) != 1 {
		t.Errorf("服务端收到 %d 个 PUT, want 1", len(rec.paths))
	}
}

// TestUploadSingleTargetFails 验证只有一个目标且它失败时，整体判定为失败。
// "至少一个成功即算成功"在只有一个目标时退化成"它必须成功"。
func TestUploadSingleTargetFails(t *testing.T) {
	badSrv := httptest.NewServer((&davRecorder{status: http.StatusInsufficientStorage}).handler())
	t.Cleanup(badSrv.Close)

	u := NewUploader(badSrv.Client(), []Target{
		{Name: "阿里云盘", URL: badSrv.URL + "/dav"},
	}, "alist", "pw")

	got, err := u.Upload(context.Background(), writeTempMP3(t, "x"), "a.mp3", nil)
	if err == nil {
		t.Fatal("唯一的目标失败时应当返回 error")
	}
	if len(got) != 1 || got[0].State != TargetFailed {
		t.Fatalf("got = %+v, want 单个 TargetFailed", got)
	}
}
```

- [ ] **Step 7: 编译并跑全量测试**

Run: `go build ./... && go vet ./... && go test -short ./...`
Expected: 全 PASS

- [ ] **Step 8: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/news2tg/main.go internal/music/upload_test.go
git commit -m "#feat [music] 支持只配一个 WebDAV 目标

校验从「六项全配」改成「凭据齐全 + 至少一个完整目标」；半个目标（有 name
没 url）仍然启动失败——那一定是打字漏了。Uploader 一行未改，它本来就是按
[]Target 循环的。

错误信息改成一次性列出所有毛病，省得改配置的人来回试。"
```

---

### Task 3: `renderStatus` 在时长为空时省略那一段

**Files:**
- Modify: `internal/music/report.go:376`
- Test: `internal/music/report_test.go`

**Interfaces:**
- Consumes: 无
- Produces: 无新符号；`renderStatus` 行为变更（`Track.Duration` 为空时曲目行不再有尾随的 ` · `）。Task 4 的 musicso 依赖此行为（它的 `Duration` 恒空）。

- [ ] **Step 1: 写失败测试**

追加到 `internal/music/report_test.go` 末尾：

```go
// TestRenderStatusEmptyDuration 验证：音源不提供时长时，曲目行不留下孤零零的分隔点。
//
// musicso.cc 不返回时长，Duration 恒为空串。原先的写法是
// fmt.Sprintf("%s - %s · %s", Artist, Title, Duration)，会渲染成
// "周杰伦 - 晴天 · " —— 行尾挂一个没有下文的分隔符，看起来像渲染坏了。
// Bytes 和 Source 两段本来就是"有才拼"的写法，这里只是把漏掉的一处补齐。
func TestRenderStatusEmptyDuration(t *testing.T) {
	md := renderStatus(Status{
		Query: "晴天",
		Stage: StageDownloading,
		Track: &Track{Artist: "周杰伦", Title: "晴天", Duration: "", Source: "musicso"},
	})

	// 转义后的分隔点后面必须还有内容（这里是 source），不能是行尾。
	for _, bad := range []string{"· \n", "·\n", "· $"} {
		if strings.Contains(md, bad) {
			t.Errorf("时长为空时渲染出了悬空的分隔点 %q:\n%s", bad, md)
		}
	}
	// 正常内容仍要在。
	for _, want := range []string{"周杰伦", "晴天", "musicso"} {
		if !strings.Contains(md, want) {
			t.Errorf("渲染结果里缺少 %q\n实际:\n%s", want, md)
		}
	}
}

// TestRenderStatusKeepsDurationWhenPresent 守住回归：有时长时照旧显示。
// 光测"空的时候不显示"是不够的 —— 一个永远不拼时长的实现也能让上面那条通过。
func TestRenderStatusKeepsDurationWhenPresent(t *testing.T) {
	md := renderStatus(Status{
		Query: "晴天",
		Stage: StageDownloading,
		Track: &Track{Artist: "周杰伦", Title: "晴天", Duration: "04:29", Source: "mp3pm"},
	})
	if !strings.Contains(md, "04:29") {
		t.Errorf("有时长时应当显示出来:\n%s", md)
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `go test ./internal/music -run TestRenderStatusEmptyDuration -v`
Expected: FAIL，报告渲染出了悬空的分隔点

- [ ] **Step 3: 改实现**

把 `internal/music/report.go:376` 那一行：

```go
		line := fmt.Sprintf("%s - %s · %s", s.Track.Artist, s.Track.Title, s.Track.Duration)
```

替换为：

```go
		line := fmt.Sprintf("%s - %s", s.Track.Artist, s.Track.Title)
		// 时长是"有才拼"：musicso 这类音源不提供时长，硬拼会在行尾留下一个
		// 没有下文的 " · "，看起来像渲染坏了。下面 Bytes / Source 两段
		// 本来就是这个写法，这里只是把漏掉的一处补齐。
		if s.Track.Duration != "" {
			line += fmt.Sprintf(" · %s", s.Track.Duration)
		}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/music -run TestRenderStatus -v`
Expected: 全 PASS（含原有的 `TestRenderStatus`，它用的 Track 带 `03:58`，不受影响）

- [ ] **Step 5: 提交**

```bash
git add internal/music/report.go internal/music/report_test.go
git commit -m "#fix 进度消息在音源不提供时长时不再留下悬空的分隔点

musicso.cc 不返回时长，Duration 恒空，原先硬拼会渲染成「周杰伦 - 晴天 · 」。
Bytes / Source 两段本来就是「有才拼」，这里把漏掉的时长补齐。"
```

---

### Task 4: musicso.cc 音源

**Files:**
- Create: `internal/music/musicso.go`
- Create: `internal/music/testdata/musicso_search.html`
- Modify: `internal/music/source.go`（`Candidate` 新增私有字段 `dlCookie`）
- Test: `internal/music/musicso_test.go`

**Interfaces:**
- Consumes: `music.Source` 接口、`Candidate`、包级常量 `browserUA`（定义在 `mp3pm.go`）、`tools.TruncateUTF8`
- Produces:
  - `func NewMusicSo(httpClient *http.Client) *MusicSo`
  - `type MusicSo struct{ httpClient *http.Client; baseURL string }`（`baseURL` 未导出但**同包测试可直接赋值**，用于指向 httptest，与 `Mp3PM.searchAPI` 同一手法）
  - `(*MusicSo) Name() string` → `"musicso"`
  - `(*MusicSo) Hint() string`
  - `(*MusicSo) Search(ctx context.Context, query string) ([]Candidate, error)`
  - `(*MusicSo) Download(ctx context.Context, c Candidate, w io.Writer) (int64, error)`
  - `func parseMusicSoResults(ctx context.Context, htmlBody, cookie string) []Candidate`（同包可测）
  - `Candidate.dlCookie string`（包内私有）

**站点接口事实（已实地验证，直接照写，不要重新摸索）：**

```
① GET <base>/s/<PathEscape(关键词)>       必须带 browserUA
   → 200 text/html
     响应头 Set-Cookie: PHPSESSID=<会话>
     响应体 每首歌一个 <div class="song-item">：
         <div class="song-name">晴天</div>
         <div class="song-artist"><a class="song-artist-link" …>周杰伦</a> · <a class="song-album-link" …>叶惠美</a></div>
         <span class="song-source src-qq">QQ源</span>
         <a class="song-play" href="/music/0039MnYb0qxYhV-q-晴天-周杰伦.html">
     实测固定 15 条，不提供时长。

② GET <base>/api/play.php?id=<id>&type=<t>    必须带 Cookie: PHPSESSID=…
   → {"code":1,"msg":"成功","url":"https://…mp3?vkey=…","lrc":"…","pic":"…"}
   缺会话返回纯文本 "forbidden"；Referer / X-Requested-With 都无效。

③ GET <上一步的 url>                          不需要任何 cookie
   → 200 audio/mpeg

Cloudflare：非浏览器 UA 一律 403 + cf-mitigated: challenge + 体内含 "Just a moment"。
```

- [ ] **Step 1: 创建测试 fixture**

创建 `internal/music/testdata/musicso_search.html`，内容如下（这是从真实搜索页裁剪来的，
保留了两条正常条目、一条歌名含连字符的条目，外加两条畸形条目用于验证跳过逻辑）：

```html
<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>晴天 周杰伦 - musicSo.cc</title></head><body>
<div class="results-section">
<div id="song-list">
    <div class="song-item">
        <div class="song-index">1</div>
        <div class="song-info">
            <div class="song-name">晴天</div>
            <div class="song-artist"><a class="song-artist-link" href="/s/%E5%91%A8%E6%9D%B0%E4%BC%A6">周杰伦</a> · <a class="song-album-link" href="/s/%E5%8F%B6%E6%83%A0%E7%BE%8E">叶惠美</a></div>
        </div>
        <span class="song-source src-qq">QQ源</span>
        <a class="song-play" href="/music/0039MnYb0qxYhV-q-晴天-周杰伦.html" target="_blank">播放&下载</a>
    </div>
    <div class="song-item">
        <div class="song-index">2</div>
        <div class="song-info">
            <div class="song-name">晴天</div>
            <div class="song-artist"><a class="song-artist-link" href="/s/A-LNK">A-LNK</a> · <a class="song-album-link" href="/s/%E4%B8%8D%E6%95%A3">不散</a></div>
        </div>
        <span class="song-source src-netease">网易源</span>
        <a class="song-play" href="/music/3339230677-n-晴天-A-LNK.html" target="_blank">播放&下载</a>
    </div>
    <div class="song-item">
        <div class="song-index">3</div>
        <div class="song-info">
            <div class="song-name">晴天 - Live</div>
            <div class="song-artist"><a class="song-artist-link" href="/s/%E5%91%A8%E6%9D%B0%E4%BC%A6">周杰伦</a> · <a class="song-album-link" href="/s/x">地表最强巡回演唱会</a></div>
        </div>
        <span class="song-source src-qq">QQ源</span>
        <a class="song-play" href="/music/004Fs2FP1EvZYc-q-晴天 - Live-周杰伦.html" target="_blank">播放&下载</a>
    </div>
    <div class="song-item">
        <div class="song-index">4</div>
        <div class="song-info">
            <div class="song-name">畸形条目：href 不符合 /music/<id>-<t>- 形状</div>
            <div class="song-artist"><a class="song-artist-link" href="/s/x">某人</a></div>
        </div>
        <span class="song-source src-qq">QQ源</span>
        <a class="song-play" href="/music/broken.html" target="_blank">播放&下载</a>
    </div>
    <div class="song-item">
        <div class="song-index">5</div>
        <div class="song-info">
            <div class="song-name">畸形条目：整个 song-play 链接都不存在</div>
            <div class="song-artist"><a class="song-artist-link" href="/s/y">另一个人</a></div>
        </div>
        <span class="song-source src-netease">网易源</span>
    </div>
</div>
</div>
</body></html>
```

- [ ] **Step 2: 写失败测试**

创建 `internal/music/musicso_test.go`：

```go
package music

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestParseMusicSoResults 锁死 HTML 解析行为，尤其是两条畸形条目必须被跳过。
func TestParseMusicSoResults(t *testing.T) {
	got := parseMusicSoResults(context.Background(), loadFixture(t, "musicso_search.html"), "sess123")

	// 5 条 song-item 里，第 4 条 href 形状不对、第 5 条压根没有 song-play，
	// 两条都拿不到 id，模型选了也下不下来，必须在这里就被丢掉。
	if len(got) != 3 {
		t.Fatalf("解析出 %d 条候选，want 3（两条畸形条目应被跳过）\n%+v", len(got), got)
	}

	if got[0].ID != "q-0039MnYb0qxYhV" {
		t.Errorf("[0].ID = %q, want %q", got[0].ID, "q-0039MnYb0qxYhV")
	}
	if got[0].Artist != "周杰伦" || got[0].Title != "晴天" {
		t.Errorf("[0] = {%q, %q}, want {周杰伦, 晴天}", got[0].Artist, got[0].Title)
	}
	// 站点不提供时长，必须是空串 —— renderStatus 依赖它来省略那一段。
	if got[0].Duration != "" {
		t.Errorf("[0].Duration = %q, want 空串（站点不提供时长）", got[0].Duration)
	}
	// 会话必须跟着候选走，下载时才拿得到。
	if got[0].dlCookie != "sess123" {
		t.Errorf("[0].dlCookie = %q, want %q", got[0].dlCookie, "sess123")
	}

	// 网易源的 id 是纯数字，前缀是 n。
	if got[1].ID != "n-3339230677" {
		t.Errorf("[1].ID = %q, want %q", got[1].ID, "n-3339230677")
	}

	// 歌名里含连字符的条目：正则只能吃掉最左边的 id 和 type，
	// 不能被歌名里的 '-' 带偏。
	if got[2].ID != "q-004Fs2FP1EvZYc" {
		t.Errorf("[2].ID = %q, want %q", got[2].ID, "q-004Fs2FP1EvZYc")
	}
	if got[2].Title != "晴天 - Live" {
		t.Errorf("[2].Title = %q, want %q", got[2].Title, "晴天 - Live")
	}
}

// TestParseMusicSoResultsEmpty 验证：没有任何 song-item 时返回空切片而不是报错。
// "没搜到"是正常路径，交给模型换关键词，不该当成错误中断。
func TestParseMusicSoResultsEmpty(t *testing.T) {
	got := parseMusicSoResults(context.Background(), "<html><body>没有找到</body></html>", "s")
	if len(got) != 0 {
		t.Fatalf("空页面应解析出 0 条，实际 %d 条", len(got))
	}
}

// newMusicSoServer 起一个假的 musicso 站点。
//
// 它同时充当断言器：把每一跳收到的 Cookie 记下来，测试据此验证
// "play.php 确实带上了搜索那一跳下发的会话"和"CDN 那一跳没带 cookie"。
func newMusicSoServer(t *testing.T, fixture string) (*httptest.Server, *musicSoCalls) {
	t.Helper()
	calls := &musicSoCalls{}
	mux := http.NewServeMux()

	// ① 搜索页：下发会话 + 返回结果页 HTML
	mux.HandleFunc("/s/", func(w http.ResponseWriter, r *http.Request) {
		calls.searchUA = r.Header.Get("User-Agent")
		calls.searchPath = r.URL.Path
		http.SetCookie(w, &http.Cookie{Name: "PHPSESSID", Value: "the-session", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, fixture)
	})

	// ② 解析直链：必须带会话
	mux.HandleFunc("/api/play.php", func(w http.ResponseWriter, r *http.Request) {
		calls.playCookie = r.Header.Get("Cookie")
		calls.playQuery = r.URL.RawQuery
		if !strings.Contains(calls.playCookie, "PHPSESSID=the-session") {
			_, _ = io.WriteString(w, "forbidden")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// 直链指回本服务器的 /cdn，方便一路测到底。
		fmt.Fprintf(w, `{"code":1,"msg":"成功","url":%q,"lrc":"","pic":""}`, calls.base+"/cdn/song.mp3")
	})

	// ③ CDN：不该带 cookie
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, r *http.Request) {
		calls.cdnCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = io.WriteString(w, "ID3fake-mp3-bytes")
	})

	srv := httptest.NewServer(mux)
	calls.base = srv.URL
	t.Cleanup(srv.Close)
	return srv, calls
}

// musicSoCalls 记录假服务端观察到的请求细节，供断言使用。
//
// 这里不像 upload_test.go 的 davRecorder 那样加锁：那边的多个 PUT 是**并发**
// 发出的，这边 Search / Download 里的每一跳都是顺序发的，而且所有断言都发生在
// 对应方法返回之后 —— HTTP 往返完成本身就建立了 happens-before 关系。
type musicSoCalls struct {
	base       string
	searchUA   string
	searchPath string
	playCookie string
	playQuery  string
	cdnCookie  string
}

// TestMusicSoSearch 覆盖搜索这一跳：URL 形状、浏览器 UA、候选内容。
func TestMusicSoSearch(t *testing.T) {
	srv, calls := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	got, err := m.Search(context.Background(), "晴天 周杰伦")
	if err != nil {
		t.Fatalf("Search 意外报错: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search 返回 %d 条, want 3", len(got))
	}
	// 关键词必须走 path 而不是 query（站点的搜索是 /s/<关键词>）。
	if !strings.HasPrefix(calls.searchPath, "/s/") {
		t.Errorf("搜索路径 = %q, want /s/ 前缀", calls.searchPath)
	}
	// 不带浏览器 UA 会被 Cloudflare 拦下，这条是硬要求。
	if calls.searchUA != browserUA {
		t.Errorf("搜索 UA = %q, want browserUA", calls.searchUA)
	}
	// 会话必须被捕获并写进每一条候选。
	if got[0].dlCookie != "the-session" {
		t.Errorf("候选未带上会话，dlCookie = %q", got[0].dlCookie)
	}
}

// TestMusicSoDownload 覆盖下载两跳，并锁死两条关键的 cookie 语义：
// play.php 必须带会话，CDN 必须不带。
func TestMusicSoDownload(t *testing.T) {
	srv, calls := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	n, err := m.Download(context.Background(),
		Candidate{ID: "q-0039MnYb0qxYhV", dlCookie: "the-session"}, &buf)
	if err != nil {
		t.Fatalf("Download 意外报错: %v", err)
	}
	const payload = "ID3fake-mp3-bytes"
	if n != int64(len(payload)) || buf.String() != payload {
		t.Errorf("下载内容 = %q (%d 字节), want %q", buf.String(), n, payload)
	}
	// id 与 type 必须被正确拆开塞进 query。
	if !strings.Contains(calls.playQuery, "id=0039MnYb0qxYhV") || !strings.Contains(calls.playQuery, "type=q") {
		t.Errorf("play.php query = %q, want 含 id=0039MnYb0qxYhV 与 type=q", calls.playQuery)
	}
	// CDN 那一跳带上 cookie 不会立刻出错，但会把会话泄漏给第三方 CDN，
	// 而且真实 CDN 对多余的 Cookie 头有时会直接 400。
	if calls.cdnCookie != "" {
		t.Errorf("CDN 请求不该带 cookie，实际 %q", calls.cdnCookie)
	}
}

// TestMusicSoDownloadWithoutSession 验证：会话缺失时 play.php 返回 forbidden，
// 必须变成 error 回灌给模型，而不是把 "forbidden" 当成 mp3 写进文件。
func TestMusicSoDownloadWithoutSession(t *testing.T) {
	srv, _ := newMusicSoServer(t, loadFixture(t, "musicso_search.html"))

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "q-0039MnYb0qxYhV", dlCookie: ""}, &buf); err == nil {
		t.Fatal("缺会话时应当报错")
	}
	if buf.Len() != 0 {
		t.Errorf("失败时不该往 writer 里写任何东西，实际写了 %q", buf.String())
	}
}

// TestMusicSoDownloadBadCode 验证：play.php 返回 code != 1 时报错，
// 让 agent 能把失败回灌给模型去改选候选。
func TestMusicSoDownloadBadCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"msg":"获取失败","url":""}`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "q-x", dlCookie: "s"}, &buf); err == nil {
		t.Fatal("code != 1 时应当报错")
	}
}

// TestMusicSoDownloadBadID 验证：候选 id 不是 <t>-<id> 形状时直接报错，
// 不要拿着半个 id 去打站点得到更莫名其妙的错误。
func TestMusicSoDownloadBadID(t *testing.T) {
	m := NewMusicSo(http.DefaultClient)
	var buf strings.Builder
	if _, err := m.Download(context.Background(),
		Candidate{ID: "没有连字符", dlCookie: "s"}, &buf); err == nil {
		t.Fatal("畸形 id 应当报错")
	}
}

// TestMusicSoCloudflareChallenge 验证：撞上 Cloudflare 质询时返回**明确的错误**，
// 而不是降级成"零结果"。
//
// 判别力：质询页是一个没有任何 song-item 的 HTML，如果实现只看
// "解析出几条"，它会静默返回空切片 —— 模型于是换几个关键词全部无果，
// 用户最终看到的是"这首歌搜不到"，一条完全指不到病根的失败信息。
func TestMusicSoCloudflareChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.Header().Set("cf-ray", "a2f236c9695cae43-LAX")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body></body></html>`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	_, err := m.Search(context.Background(), "晴天")
	if err == nil {
		t.Fatal("撞上 Cloudflare 质询时必须报错，不能静默当成零结果")
	}
	if !strings.Contains(err.Error(), "Cloudflare") {
		t.Errorf("错误信息应当点名 Cloudflare，实际: %v", err)
	}
}

// TestMusicSoChallengeWithout403 验证：有些质询是 200 + 挑战页，
// 光看状态码会漏掉，必须也认响应体特征。
func TestMusicSoChallengeWithout403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		_, _ = io.WriteString(w, `<html><head><title>Just a moment...</title></head></html>`)
	}))
	t.Cleanup(srv.Close)

	m := NewMusicSo(srv.Client())
	m.baseURL = srv.URL

	if _, err := m.Search(context.Background(), "晴天"); err == nil {
		t.Fatal("cf-mitigated 存在时即便状态码是 200 也应报错")
	}
}
```

- [ ] **Step 3: 跑测试确认它失败**

Run: `go test ./internal/music -run TestMusicSo -v`
Expected: 编译失败，`undefined: NewMusicSo` / `undefined: parseMusicSoResults` / `unknown field dlCookie`

- [ ] **Step 4: 给 `Candidate` 加私有字段**

在 `internal/music/source.go` 的 `Candidate` 结构体里，`dlURL` 之后补一行：

```go
	dlCookie string // 下载所需的会话凭据（如 PHPSESSID 的值）；空表示该音源不需要
```

并在 `dlURL` 上方那段注释之后补一段：

```go
// dlCookie 与 dlURL 是同一套思路：包外不可见，只有产出它的那个音源看得懂，
// 也绝不进模型上下文。musicso.cc 的直链解析接口要求带上搜索那一跳下发的
// PHPSESSID，把它挂在候选上（而不是让音源自己存一份共享状态）有个具体好处：
// 两个并发的 /music 任务不会互相把对方的会话冲掉 —— 否则 A 搜完、B 重新
// 预热、A 再下载时会话已被换掉，站点直接 403，而报出来是"下载失败"这种
// 完全指不到病根的错误。mp3.pm 不需要会话，这个字段对它恒为空。
```

- [ ] **Step 5: 实现 musicso 音源**

创建 `internal/music/musicso.go`：

```go
package music

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	// 第三方包：HTML 解析（类似 jQuery 的 API），mp3pm.go 与 hackernews.go 已在用。
	"github.com/PuerkitoBio/goquery"

	"github.com/cheedonghu/news2tg/internal/tools"
)

const (
	// musicSoName 是源标识，同时决定工具名 search_musicso / download_musicso。
	musicSoName = "musicso"
	// musicSoBaseURL 是站点根地址。搜索走 /s/<关键词>，解析直链走 /api/play.php。
	musicSoBaseURL = "https://www.musicso.cc"
)

// 编译期断言：*MusicSo 必须满足 Source。接口漏实现会在编译阶段就报错。
var _ Source = (*MusicSo)(nil)

// musicSoHrefRe 从搜索结果的详情页链接里抠出 id 与后端标识。
//
// href 形如 /music/0039MnYb0qxYhV-q-晴天-周杰伦.html：
// 第一段是 id，第二段是单字母的后端标识（q = QQ 音乐，n = 网易云），
// 再往后才是歌名和歌手 —— 而它们**可能自带连字符**（"晴天 - Live"）。
// 所以必须从左往右锚定着取前两段，绝不能按 '-' 全切再猜。
// [^-]+ 对 id 是安全的：QQ 的 mid 是 14 位 base62、网易的是纯数字，都不含 '-'。
var musicSoHrefRe = regexp.MustCompile(`^/music/([^-]+)-([a-z])-`)

// MusicSo 是 musicso.cc 的 Source 实现。
//
// 这个站把 QQ 音乐和网易云的搜索结果**合并排序后一次给出**，
// 所以这里是一个 Source 而不是按后端拆成好几个 —— 模型本来也无从判断
// "这首歌在哪家有"，拆开只会让工具数翻倍、白烧 token。
type MusicSo struct {
	httpClient *http.Client
	// baseURL 单独抽成字段（而非直接用常量）是为了测试能指向 httptest，
	// 与 mp3pm.go 里 searchAPI、digest/jina.go 里 baseURL 的做法一致。
	baseURL string
}

// NewMusicSo 构造函数。httpClient 复用 main 里创建的共享连接池。
func NewMusicSo(httpClient *http.Client) *MusicSo {
	return &MusicSo{httpClient: httpClient, baseURL: musicSoBaseURL}
}

// Name 返回源标识。
func (m *MusicSo) Name() string { return musicSoName }

// Hint 告诉模型这个站怎么搜才有效。
// 和 mp3.pm 的规则正好相反：那边是俄语站、中文歌按拼音收录，这边是中文站。
func (m *MusicSo) Hint() string {
	return "中文音乐站，聚合 QQ 音乐与网易云，歌名歌手都是中文原名。" +
		"中文歌请直接用中文关键词搜索（歌名，或「歌名 歌手」），不要转成拼音或英文译名。" +
		"英文歌用原文英文名搜索。该站不提供时长信息。"
}

// Search 只需一次 GET：搜索页在返回结果 HTML 的同时下发本轮会话 cookie。
//
// 会话为什么必须每次现拿：站点的直链解析接口 /api/play.php 认 PHPSESSID，
// 而 PHP 会话默认 24 分钟就过期，本进程却是常驻的、可能几天才来一次 /music。
// 只在启动时拿一次的话，绝大多数真实调用都会撞在过期会话上。
// 好在会话和搜索是同一个请求，"现拿现用"不额外多一跳。
func (m *MusicSo) Search(ctx context.Context, query string) ([]Candidate, error) {
	slog.InfoContext(ctx, "开始在 musicso 搜索", "query", query)

	// 关键词走 path 而不是 query：站点的搜索地址就是 /s/<关键词>。
	// url.PathEscape 负责把空格、中文等转义成合法的路径片段。
	dst := strings.TrimRight(m.baseURL, "/") + "/s/" + url.PathEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dst, nil)
	if err != nil {
		return nil, err
	}
	// 站点在 Cloudflare 后面：不带浏览器 UA 一律被质询拦下。
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("musicso 搜索请求失败: %w", err)
	}
	// defer 在函数返回时执行，保证连接被释放回连接池。
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 musicso 搜索页失败: %w", err)
	}

	// 质询要在状态码判断**之前**认：有些质询是 200 + 挑战页，
	// 光看状态码会把它当成一个正常但没有结果的页面放过去。
	if err := musicSoChallenge(ctx, resp, string(body)); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("musicso 搜索页返回非 2xx 状态码 %d", resp.StatusCode)
	}

	// 捞出本轮会话。拿不到不在这里报错：也许站点改了 cookie 名，
	// 真正的后果会在 Download 那一跳以明确的 403 呈现出来；
	// 但先 WARN 一条，让日志能指向病根。
	session := musicSoSession(resp)
	if session == "" {
		slog.WarnContext(ctx, "musicso 搜索页未下发 PHPSESSID，下载多半会失败")
	}

	return parseMusicSoResults(ctx, string(body), session), nil
}

// musicSoSession 从响应里取出 PHPSESSID 的值。
// resp.Cookies() 解析的是 Set-Cookie 响应头，返回 []*http.Cookie。
func musicSoSession(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == "PHPSESSID" {
			return c.Value
		}
	}
	return ""
}

// musicSoChallenge 识别 Cloudflare 的 bot 质询。
//
// 为什么必须单独识别、而不是让它自然地表现成"零结果"：
// 质询页是一个没有任何 song-item 的 HTML，若只看"解析出几条"，
// 它会静默返回空切片 —— 模型于是换几个关键词全部无果，用户最终看到的是
// "这首歌搜不到"，一条完全指不到病根的失败信息。站点是否可达这件事，
// 必须以它自己的面目报出来。
//
// 两条特征分别覆盖两种形态：cf-mitigated 响应头（CF 的正式标记），
// 以及挑战页标题（响应头被中间层剥掉时的兜底）。
func musicSoChallenge(ctx context.Context, resp *http.Response, body string) error {
	mitigated := resp.Header.Get("cf-mitigated")
	if mitigated == "" && !strings.Contains(body, "Just a moment") {
		return nil
	}
	ray := resp.Header.Get("cf-ray")
	slog.ErrorContext(ctx, "musicso 被 Cloudflare 拦截",
		"status", resp.StatusCode, "cf-mitigated", mitigated, "cf-ray", ray)
	return fmt.Errorf("musicso 被 Cloudflare 拦截（bot 质询，cf-ray=%s）；"+
		"该出口 IP 已被判定为机器人，需要更换出口或配置 [network] proxy", ray)
}

// parseMusicSoResults 从搜索结果页 HTML 里抽出候选列表。
//
// 页面结构（实测）：每首歌一个 <div class="song-item">，
// 歌名/歌手在子元素里，id 与后端标识编码在详情页链接的 href 上。
// 也就是说搜索结果页一次性给全，不需要再进详情页。
//
// 返回值不带 error：解析不出东西时返回空切片，由调用方当作"零结果"处理 ——
// 对模型来说"没搜到"和"页面变了"都该走同一条"换关键词重试"的路。
// 但两者对我们排障的意义不同，所以下面额外打一条 WARN 区分。
func parseMusicSoResults(ctx context.Context, htmlBody, cookie string) []Candidate {
	// goquery 从 io.Reader 解析；strings.NewReader 把字符串包成 Reader。
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		slog.ErrorContext(ctx, "解析 musicso 结果页 HTML 失败", "err", err)
		return nil
	}

	var out []Candidate
	// Each 的回调签名是 func(下标 int, 元素 *goquery.Selection)；_ 表示丢弃下标。
	doc.Find("div.song-item").Each(func(_ int, s *goquery.Selection) {
		// Attr 返回 (值, 是否存在)；没有播放链接的条目拿不到 id，跳过。
		href, ok := s.Find("a.song-play").First().Attr("href")
		if !ok {
			return
		}
		// FindStringSubmatch 返回 [整体匹配, 第1组, 第2组…]；不匹配返回 nil。
		mm := musicSoHrefRe.FindStringSubmatch(href)
		if mm == nil {
			// href 形状不对（站点改版，或这条本来就是个占位），
			// 模型选了也下不下来，直接丢掉。
			return
		}
		id, backend := mm[1], mm[2]

		// First() 防御同一条目里意外出现多个同类元素；
		// Text() 会把 HTML 实体（&#039;）解码成真正的字符。
		title := strings.TrimSpace(s.Find(".song-name").First().Text())
		artist := strings.TrimSpace(s.Find("a.song-artist-link").First().Text())
		if title == "" {
			return // 连歌名都没有的条目对模型毫无意义
		}

		out = append(out, Candidate{
			// id 里带上后端标识，下载时才知道该往 play.php 传哪个 type。
			// 拼在一起而不是给 Candidate 再加一个私有字段：这个形状
			// 照抄站点 href 自己的写法，模型原样照抄即可，Go 侧一分为二。
			ID:       backend + "-" + id,
			Artist:   artist,
			Title:    title,
			Duration: "", // 站点不提供时长；renderStatus 会因此省略那一段
			dlCookie: cookie,
		})
	})

	// 页面有内容却一条都没解析出来 —— 大概率是站点改版了，而不是"真没这首歌"。
	// 单独告警，让日志能区分这两种情况，否则改版会静默表现成"永远搜不到"。
	if len(out) == 0 && strings.TrimSpace(htmlBody) != "" {
		slog.WarnContext(ctx, "musicso 结果页解析出 0 条候选（可能站点改版）", "bodyLen", len(htmlBody))
	}
	return out
}

// musicSoPlayResp 是 /api/play.php 的响应形状。
// 只声明用得上的字段，其余（lrc / pic）让 encoding/json 自动丢弃。
type musicSoPlayResp struct {
	Code int    `json:"code"` // 1 = 成功
	Msg  string `json:"msg"`
	URL  string `json:"url"` // CDN 直链
}

// Download 走两跳：先用会话换到 CDN 直链，再裸 GET 那个直链。
//
// 为什么不能一跳：搜索结果里给的是站点自己的详情页地址，真正的音频直链
// 要现问 /api/play.php 要，而且它认搜索那一跳下发的 PHPSESSID。
// 好在换来的 CDN 直链本身**不需要任何 cookie**（实测），所以第二跳是裸 GET ——
// 也别给它带上 cookie，那等于把会话泄漏给第三方 CDN。
func (m *MusicSo) Download(ctx context.Context, c Candidate, w io.Writer) (int64, error) {
	// 候选 id 的形状是 <后端标识>-<站内 id>，按第一个 '-' 切一刀。
	// SplitN(s, sep, 2) 最多切成 2 段，所以站内 id 里万一有 '-' 也不会被切碎。
	parts := strings.SplitN(c.ID, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("musicso 候选 id %q 形状不对（应为 <q|n>-<id>）", c.ID)
	}
	backend, id := parts[0], parts[1]

	slog.InfoContext(ctx, "开始从 musicso 下载", "id", id, "backend", backend, "title", c.Title)

	// ① 用会话换直链。
	playURL := fmt.Sprintf("%s/api/play.php?id=%s&type=%s",
		strings.TrimRight(m.baseURL, "/"), url.QueryEscape(id), url.QueryEscape(backend))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", browserUA)
	if c.dlCookie != "" {
		// 手动拼 Cookie 头而不是用 cookiejar：会话是跟着候选走的，
		// 这样两个并发的 /music 任务不会互相把对方的会话冲掉。
		req.Header.Set("Cookie", "PHPSESSID="+c.dlCookie)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("musicso 直链解析请求失败: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	// 读完就关：下面还要发第二个请求，早点把连接还回池子。
	resp.Body.Close()
	if readErr != nil {
		return 0, fmt.Errorf("读取 musicso 直链解析响应失败: %w", readErr)
	}
	if err := musicSoChallenge(ctx, resp, string(body)); err != nil {
		return 0, err
	}

	var pr musicSoPlayResp
	if jErr := json.Unmarshal(body, &pr); jErr != nil {
		// 会话缺失时站点回的是纯文本 "forbidden"，正好落在这里。
		// 把原文截断带进错误信息，比一句"JSON 解析失败"有用得多。
		return 0, fmt.Errorf("musicso 直链解析返回的不是 JSON（%v）：%s",
			jErr, tools.TruncateUTF8(strings.TrimSpace(string(body)), 100))
	}
	if pr.Code != 1 || strings.TrimSpace(pr.URL) == "" {
		return 0, fmt.Errorf("musicso 未能给出直链（code=%d msg=%q）", pr.Code, pr.Msg)
	}

	// ② 裸 GET 直链。注意**不带** cookie。
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pr.URL, nil)
	if err != nil {
		return 0, err
	}
	dlReq.Header.Set("User-Agent", browserUA)

	dlResp, err := m.httpClient.Do(dlReq)
	if err != nil {
		return 0, fmt.Errorf("musicso 下载请求失败: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		return 0, fmt.Errorf("musicso 下载返回非 2xx 状态码 %d", dlResp.StatusCode)
	}

	// io.Copy 从 Body 流式拷到 w，不会把整个文件读进内存。
	// w 可能是带大小上限的 limitWriter，超限时它会返回错误让 Copy 提前中断；
	// 这里用 %w 包装以保留 errors.Is 的可判别性（agent 要靠它区分"过大"和别的失败）。
	n, err := io.Copy(w, dlResp.Body)
	if err != nil {
		return n, fmt.Errorf("写入 mp3 数据失败: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/music -run 'TestMusicSo|TestParseMusicSo' -v`
Expected: 全部 PASS

- [ ] **Step 7: 跑全量测试 + 竞态检测**

Run: `go test -short ./... && go test -short -race ./internal/music`
Expected: 全 PASS，无竞态报告

- [ ] **Step 8: 提交**

```bash
git add internal/music/musicso.go internal/music/musicso_test.go internal/music/source.go internal/music/testdata/musicso_search.html
git commit -m "#feat 新增 musicso.cc 音源

站点聚合 QQ 音乐与网易云并已合并排序，歌名歌手是中文原名，正好补上
mp3.pm（俄语站、中文歌按拼音收录）的短板。搜索一次 GET 即同时拿到会话与
结果，下载走 play.php 换直链再裸 GET CDN。

会话跟着候选走（Candidate.dlCookie）而不是音源持有共享状态：否则两个并发的
/music 任务会互相冲掉对方的会话，报出来却是「下载失败」这种指不到病根的错误。

Cloudflare 质询单独识别并明确报错，不降级成「零结果」——否则模型会换几个
关键词全部无果，用户看到的是「这首歌搜不到」。"
```

---

### Task 5: `[music] sources` 有序数组 + main 音源工厂表

**Files:**
- Modify: `internal/config/config.go`（`Music.Sources` 字段 + 校验）
- Modify: `cmd/news2tg/main.go:167-181`（工厂表按配置顺序构造）
- Modify: `config.toml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `music.NewMusicSo`（Task 4）、`music.NewMp3PM`（已有）
- Produces: `Music.Sources []string`；导出常量/变量 `config.DefaultMusicSources []string`（供 main 与测试共用）；`Music.EffectiveSources() []string`（缺省时回落到默认顺序）

- [ ] **Step 1: 写失败测试**

追加到 `internal/config/config_test.go` 末尾：

```go
// TestMusicSourcesDefault 验证：不写 sources 时回落到默认顺序、全部启用。
// 现有部署的 myconfig.toml 没有这个字段，不能因为升级就起不来或悄悄关掉音源。
func TestMusicSourcesDefault(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
`)))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	got := cfg.Music.EffectiveSources()
	if len(got) != len(DefaultMusicSources) {
		t.Fatalf("EffectiveSources() = %v, want %v", got, DefaultMusicSources)
	}
	for i := range got {
		if got[i] != DefaultMusicSources[i] {
			t.Fatalf("EffectiveSources() = %v, want %v", got, DefaultMusicSources)
		}
	}
}

// TestMusicSourcesOrderPreserved 验证：数组顺序被原样保留 —— 顺序就是优先级。
func TestMusicSourcesOrderPreserved(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
sources       = ["mp3pm", "musicso"]
`)))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	got := cfg.Music.EffectiveSources()
	if len(got) != 2 || got[0] != "mp3pm" || got[1] != "musicso" {
		t.Fatalf("EffectiveSources() = %v, want [mp3pm musicso]", got)
	}
}

// TestMusicSourcesSubset 验证：只列一个 = 只启用一个，其余关闭。
func TestMusicSourcesSubset(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
sources       = ["musicso"]
`)))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if got := cfg.Music.EffectiveSources(); len(got) != 1 || got[0] != "musicso" {
		t.Fatalf("EffectiveSources() = %v, want [musicso]", got)
	}
}

// TestMusicSourcesInvalid 锁死所有应当启动失败的 sources 写法。
func TestMusicSourcesInvalid(t *testing.T) {
	const creds = `
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
`
	cases := []struct {
		name    string
		sources string
	}{
		{"未知音源名", `sources = ["musicso", "spotify"]`},
		{"重复项", `sources = ["musicso", "musicso"]`},
		{"显式空数组", `sources = []`},
		{"空串元素", `sources = ["musicso", ""]`},
		{"纯空格元素", `sources = ["musicso", "   "]`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			_, err := FromFile(writeTempTOML(t, musicTOML("\n[music]"+creds+c.sources+"\n")))
			if err == nil {
				t.Fatal("该 sources 写法应当导致启动失败，却成功了")
			}
			if !strings.Contains(err.Error(), "sources") {
				t.Errorf("错误信息应提到 sources，实际: %v", err)
			}
		})
	}
}

// TestMusicSourcesAloneStillValidates 验证：只写了 sources、webdav 全忘时也要报错。
//
// 这条守的是 touched 判定必须覆盖 sources。否则这份配置会被判成
// "整段没配"而静默放行，管理员拿到一个看似正常启动、
// 实则 /music 悄悄不可用的进程。
func TestMusicSourcesAloneStillValidates(t *testing.T) {
	_, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
sources = ["musicso"]
`)))
	if err == nil {
		t.Fatal("只写 sources 而 webdav 全空时应当启动失败")
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `go test ./internal/config -run TestMusicSources -v`
Expected: 编译失败，`undefined: DefaultMusicSources`、`cfg.Music.EffectiveSources undefined`

- [ ] **Step 3: 实现 sources 字段与校验**

在 `internal/config/config.go` 里：

**(a)** 给 `Music` 结构体加字段（放在六个 webdav 字段之后）：

```go
	// Sources 是启用的音源列表，**顺序即优先级**（模型按这个顺序依次尝试）。
	// 不在列表里 = 关闭。缺省（不写这一行）= DefaultMusicSources 全部启用。
	//
	// 为什么用一个有序数组而不是"开关字段 + 优先级字段"：
	// 一个字段同时表达两件事，就不可能出现"开了但没给优先级"
	// 或"两个源抢同一个优先级"这种自相矛盾的配置。
	Sources []string `toml:"sources"`
```

**(b)** 在 `Music` 结构体之后加：

```go
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
	// 所以那个 case 由 rawSourcesPresent 单独兜（见 validate 的调用处）。
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
```

在 import 块里加 `"sort"`。

**(c)** 修改 `validate()`：把 `anyTouched` 那一段与 `problems` 收尾改成：

```go
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
```

并在 `problems` 收集的末尾（`if len(problems) == 0` **之前**）加：

```go
	problems = append(problems, m.validateSources()...)
```

**(d)** 处理 `sources = []` 这个 case。TOML 解析后 `[]` 与"没写"都是长度 0，
无法在 `Music` 上区分，所以在 `FromFile` 里用 `toml.MetaData` 判断键是否出现过。
把 `FromFile` 里的解码调用改成保留 metadata，并在 `cfg.Music.validate()` 之前加：

```go
	// md.IsDefined 能区分"写了 sources = []"和"根本没写 sources" ——
	// 前者是显式关掉所有音源，那样 /music 必然什么都搜不到，
	// 属于关错了地方，应当启动失败而不是留个残废功能给人用。
	if md.IsDefined("music", "sources") && len(cfg.Music.Sources) == 0 {
		return nil, fmt.Errorf("[music] sources 是空数组：这会关掉所有音源、让 /music 必然搜不到东西。" +
			"若要关闭 /music 请整个删掉 [music] 段；若要启用音源请列出：%s",
			strings.Join(sortedKnownSources(), ", "))
	}
```

具体改法：`internal/config/config.go:219` 现在是

```go
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
```

第一个返回值（`toml.MetaData`）被丢弃了。改成把它接出来：

```go
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
```

注意这会让 `err` 从 `if` 语句的作用域提升到函数作用域，后面原本的
`return nil, fmt.Errorf(...)` 那几行不用动，但要确认 `err` 没有跟同函数里
别的变量重名（若重名就改用 `:=` 之外的写法）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config -v`
Expected: 全 PASS

- [ ] **Step 5: main 侧按配置顺序构造音源**

在 `cmd/news2tg/main.go` 里，把第 169-172 行（`// 音源列表：目前只有 mp3.pm…` 到
`musicAgent := …`）替换为：

```go
		// 音源工厂表：名字 → 怎么造。加音源时在这里多一项，
		// 同时在 config 的 knownMusicSources / DefaultMusicSources 里登记。
		// 用工厂函数而不是直接建实例：没被启用的音源不必白白构造。
		musicSourceFactory := map[string]func() music.Source{
			"musicso": func() music.Source { return music.NewMusicSo(httpClient) },
			"mp3pm":   func() music.Source { return music.NewMp3PM(httpClient) },
		}
		// 按配置顺序构造 —— 这个切片的顺序会一路传到系统提示词里，
		// 成为模型"先试哪个源"的依据，所以顺序本身就是配置语义。
		enabled := cfg.Music.EffectiveSources()
		sources := make([]music.Source, 0, len(enabled))
		for _, name := range enabled {
			// 配置校验已经保证每个名字都在工厂表里，这里的 ok 判断
			// 只为防止将来改动时两处失配（漏了在工厂表登记新音源）。
			newSource, ok := musicSourceFactory[name]
			if !ok {
				slog.Error("音源已在配置中启用但没有对应实现，请检查 main 里的工厂表", "source", name)
				os.Exit(1)
			}
			sources = append(sources, newSource())
		}
		// 复用 DeepSeek 的 key 和 agent_model（同样需要 function calling 能力）。
		musicAgent := music.NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, sources)
```

并把第 181 行的启动日志改成同时报出音源：

```go
		slog.Info("音乐功能已启用", "targets", len(targets), "sources", enabled)
```

- [ ] **Step 6: 编译并跑全量测试**

Run: `go build ./... && go vet ./... && go test -short ./...`
Expected: 全 PASS

- [ ] **Step 7: 更新配置模板**

把 `config.toml` 的 `[music]` 段改成：

```toml
[music]
webdav_user   = ""
webdav_pass   = ""
webdav_name_1 = ""
webdav_url_1  = ""
# 第二个目标可留空：至少配一个完整目标（name + url）即可。
# 但不要只填一半 —— 有 name 没 url 会直接启动失败。
webdav_name_2 = ""
webdav_url_2  = ""
# 启用的音源，**顺序即优先级**（模型按这个顺序依次尝试，前一个没结果再换下一个）。
# 不在列表里 = 关闭。整行删掉 = 全部启用，顺序同下。
#   musicso —— 中文站，聚合 QQ 音乐与网易云，歌名歌手是中文原名，中文歌首选
#   mp3pm   —— 俄语站，中文歌按拼音收录，作为兜底
# 不要写成空数组 sources = []：那会关掉所有音源，启动时会直接报错。
sources = ["musicso", "mp3pm"]
```

- [ ] **Step 8: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/news2tg/main.go config.toml
git commit -m "#feat [music] 新增 sources 有序数组控制音源开关与优先级

一个字段同时表达"启不启用"和"谁先试"，不可能出现「开了但没优先级」或
「两个源撞号」这种矛盾配置。缺省 = 默认顺序全部启用，现有部署升级不受影响；
显式写空数组则启动失败——那会关掉所有音源、让 /music 必然搜不到东西。

touched 判定扩到 sources：只写 sources 而 webdav 全忘时同样要报错，
不能被当成「整段没配」静默放行。"
```

---

### Task 6: agent 提示词与步数上限适配多音源

**Files:**
- Modify: `internal/music/agent.go:22`（`defaultMaxSteps`）、`:102-121`（`buildSystemPrompt`）
- Test: `internal/music/agent_test.go`

**Interfaces:**
- Consumes: `buildSystemPrompt(sources []Source) string`（已有）
- Produces: 生产代码无新符号；`defaultMaxSteps` 由 6 变为 10；系统提示词新增"按顺序尝试音源"一句。测试侧给已有的 `fakeSource` 加一个可选 `hint` 字段（不影响现有用例，`Hint()` 在 hint 为空时仍返回 `"测试音源"`）。

- [ ] **Step 1: 写失败测试**

追加到 `internal/music/agent_test.go` 末尾：

`agent_test.go:63` 已有一个 `fakeSource`，但它的 `Hint()` 返回写死的 `"测试音源"`，
无法给两个源不同的提示。**给它加一个可选的 hint 字段**（不新建类型 —— 那会让
同一个包里躺着两个几乎一样的假音源）：

把 `agent_test.go:63-72` 的结构体与 `Hint` 方法改成：

```go
// fakeSource 是可编程的假音源。
type fakeSource struct {
	name      string
	hint      string                 // 空则回落到默认文案；提示词测试要靠它区分不同音源
	results   map[string][]Candidate // 关键词 → 候选；未命中的关键词返回零结果
	searchErr error
	payload   string // Download 写出的内容
	dlErr     error
	dlCalls   []string // 记录被下载的 id
}

func (s *fakeSource) Name() string { return s.name }

// Hint 默认返回一句占位文案，这样现有那十几个用例一个字都不用改；
// 只有关心"各源 Hint 是否分别进了提示词"的用例才需要显式设置 hint。
func (s *fakeSource) Hint() string {
	if s.hint != "" {
		return s.hint
	}
	return "测试音源"
}
```

然后追加这三个测试：

```go
// TestBuildSystemPromptOrdersSources 验证：系统提示词按传入顺序列出音源，
// 并明确要求模型按这个顺序依次尝试。
//
// 优先级就是靠这个落地的 —— Go 里没有任何硬编码的音源排序，
// 顺序完全来自 cfg.Music.sources。所以这条断言守的是整个优先级机制。
func TestBuildSystemPromptOrdersSources(t *testing.T) {
	p := buildSystemPrompt([]Source{
		&fakeSource{name: "musicso", hint: "中文站提示"},
		&fakeSource{name: "mp3pm", hint: "俄语站提示"},
	})

	iMusicSo := strings.Index(p, "musicso")
	iMp3PM := strings.Index(p, "mp3pm")
	if iMusicSo < 0 || iMp3PM < 0 {
		t.Fatalf("提示词里应当列出两个音源:\n%s", p)
	}
	if iMusicSo > iMp3PM {
		t.Errorf("音源顺序被打乱：musicso 应当排在 mp3pm 之前\n%s", p)
	}
	// 光列出来不够，得明确告诉模型这是有先后的。
	if !strings.Contains(p, "顺序") {
		t.Errorf("提示词没有要求模型按顺序尝试音源:\n%s", p)
	}
	// 各源的 Hint 必须都在（"某个站怎么搜才有效"这条知识跟着站点实现走）。
	for _, want := range []string{"中文站提示", "俄语站提示"} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词里缺少 Hint %q:\n%s", want, p)
		}
	}
}

// TestBuildSystemPromptNormalizationIsConditional 验证：规范化那段不再无条件
// 要求"把罗马化还原成中文"。
//
// musicso 给的本来就是中文原名，无条件的措辞会诱导模型去"还原"一个
// 已经是原名的字符串，白白引入出错机会。
func TestBuildSystemPromptNormalizationIsConditional(t *testing.T) {
	p := buildSystemPrompt([]Source{&fakeSource{name: "musicso", hint: "h"}})
	if !strings.Contains(p, "已经是中文") {
		t.Errorf("提示词应当说明「已经是中文的原样保留」:\n%s", p)
	}
}

// TestDefaultMaxStepsFitsMultipleSources 守住步数上限。
//
// 一轮 = 一次模型调用。两个音源的最坏路径是：
// musicso 搜 → 换词 → mp3pm 搜 → 换词 → 下载 → 下载失败改选 → 再下载 → 输出 JSON
// 共 8 轮。原来的 6 在双源场景下会稳定撞上"未收敛"。
func TestDefaultMaxStepsFitsMultipleSources(t *testing.T) {
	const worstCasePath = 8
	if defaultMaxSteps < worstCasePath {
		t.Fatalf("defaultMaxSteps = %d，装不下双音源最坏路径的 %d 轮", defaultMaxSteps, worstCasePath)
	}
}
```

- [ ] **Step 2: 跑测试确认它失败**

Run: `go test ./internal/music -run 'TestBuildSystemPrompt|TestDefaultMaxSteps' -v`
Expected: `TestBuildSystemPromptNormalizationIsConditional` 与
`TestDefaultMaxStepsFitsMultipleSources` FAIL；
`TestBuildSystemPromptOrdersSources` 可能因缺"顺序"二字而 FAIL

- [ ] **Step 3: 改步数上限**

`internal/music/agent.go:20-22` 那段常量注释与值替换为：

```go
	// defaultMaxSteps 限制 agent 循环步数，防止模型反复调工具不收敛。
	// 一轮 = 一次模型调用。比 internal/agent 的 3 大得多，因为这里要容纳
	// 多音源的最坏路径：musicso 搜 → 换词 → mp3pm 搜 → 换词 → 下载 →
	// 下载失败改选 → 再下载 → 输出最终 JSON，共 8 轮，留两轮余量取 10。
	// 音源变多时这个值要跟着涨，否则会稳定撞上"未收敛"。
	defaultMaxSteps = 10
```

- [ ] **Step 4: 改系统提示词**

`internal/music/agent.go` 的 `buildSystemPrompt` 里，把那段 raw string 字面量替换为：

```go
	b.WriteString(`你是一个音乐下载助手。用户会用自由格式描述想要的歌（如「晴天 - 周杰伦」「周杰伦 晴天」「晴天」「Jay Chou 的晴天」），你需要理解它，从音源里搜索、挑出正确的曲目并下载。

工具使用规则：
1. 先用 search_<源名> 搜索，返回的候选每条含 id / artist / title / duration（有的音源不提供 duration，会是空串，属正常）。
2. 下面「可用音源」是**按优先级排好序**的：先试第一个，它零结果或没有匹配项时再换下一个，依此类推。
3. 同一个音源零结果时，可按该音源的特性说明换关键词重试（拼音、英文译名、只用歌名不带歌手）。整个任务最多搜索 4 次。
4. 从候选里挑最匹配用户意图的那一条。避开 live 版、伴奏/instrumental、翻唱(cover)、remix、加速/慢放版本，除非用户明确要。
5. 选定后必须调用同一个源的 download_<源名>，参数 id 取自候选列表，必须原样照抄。
6. 下载成功后不要再调用任何工具，直接输出一个 JSON 对象作为最终回答，格式严格如下：
   {"artist": "歌手原名", "title": "歌名原名", "id": "刚才下载的那个 id"}
   artist 和 title 要规范化：若音源给的是罗马化写法（如 "Jay Chou" / "Kai Bu Liao Kou"），
   还原成中文原名（"周杰伦" / "开不了口"）；**已经是中文原名的原样保留，不要改动**；
   本来就是英文歌的保持英文即可。
   只输出这个 JSON，不要有任何其它文字、解释或 markdown 代码块。

可用音源（按优先级从高到低）：
`)
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/music -v`
Expected: 全 PASS。若 `TestAgentMaxStepsExhausted` 因步数变大而失败，
**不要改回 `defaultMaxSteps`** —— 那个测试应当直接构造一个 `Agent` 并把
`maxSteps` 字段压到 1~2（`Agent.maxSteps` 本来就是为此抽成字段的），
按这个思路修它。

- [ ] **Step 6: 跑全量测试**

Run: `go build ./... && go vet ./... && go test -short ./...`
Expected: 全 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/music/agent.go internal/music/agent_test.go
git commit -m "#feat 音乐 agent 提示词与步数上限适配多音源

maxSteps 6 → 10：双音源最坏路径要 8 轮（两源各搜一次+各换词一次、下载、
下载失败改选、再下载、输出），原值会稳定撞上「未收敛」。

提示词新增「按列出的顺序依次尝试音源」——优先级完全靠这句落地，Go 里没有
任何硬编码排序。规范化那段改成有条件：musicso 给的本来就是中文原名，
无条件要求「把罗马化还原成中文」会诱导模型去改一个已经对的字符串。"
```

---

### Task 7: 更新 CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: 前六个任务的全部产出
- Produces: 无代码符号

- [ ] **Step 1: 更新 music 包描述**

在 `CLAUDE.md` 的 `**music** (internal/music/)` 那一段里：

- 把「实现：`Mp3PM`（`mp3pm.go`，HTML 解析，站点特有逻辑只在这一个文件里）」
  改成「实现：`MusicSo`（`musicso.go`，中文站，聚合 QQ 音乐与网易云）与
  `Mp3PM`（`mp3pm.go`，俄语站兜底）；站点特有逻辑只在各自那一个文件里」。
- 在「上传不是工具」那句之后补一句：
  「上传目标**至少一个、至多两个**；`Uploader` 内部按 `[]Target` 循环，
  只配一个和配两个走同一条代码路径。」
- 补一句 musicso 的会话语义：
  「`musicso` 的下载需要搜索那一跳下发的 PHPSESSID，它跟着 `Candidate.dlCookie`
  走而不是由音源持有共享状态 —— 否则并发的两个 `/music` 会互相冲掉会话。
  该站在 Cloudflare 后面，所有请求必须带 `browserUA`；撞上质询时返回**明确错误**
  而非零结果。」

- [ ] **Step 2: 更新 Config 段描述**

在 `### Config` 那一节里：

- 把 `[music]` 那一整段关于「六个平铺字段」「六项全空 → 不可用但正常启动；
  填一半 → 启动失败」的描述，改成：

  > `[music]`（`/music` 的 WebDAV 凭据、上传目标与音源列表）是**可选段**：
  > **整段不碰 → `/music` 不可用但程序正常启动**（现有部署没有这一段，
  > 不能因升级就起不来）；**碰了就必须配成能用的样子** —— 凭据齐全、
  > 且**至少一个完整的上传目标**（`name` + `url` 同时填）。
  > 「半个目标」（有 `name` 没 `url`）一律启动失败，那必然是打字漏了。
  > `sources` 是有序数组，**顺序即优先级**，不在列表里即关闭；
  > 缺省等价于 `["musicso", "mp3pm"]` 全部启用，显式写 `[]` 则启动失败。
  > 凭据只写 `myconfig.toml`，`config.toml` 留空占位——后者进 git 且仓库公开。

- 在这一节末尾追加：

  > `[network]`（可选）只有一个 `proxy` 字段：留空即全部直连；非空则**所有**
  > 出站 HTTP 走该代理，不提供 `no_proxy`（按主机分流交给代理程序自己的规则）。
  > 落地方式是在 `main` 里覆盖 `http.DefaultTransport.Proxy` 并给共享 client
  > 的 Transport 设一次 —— 仓库里五个自建 client 的 `Transport` 都是 nil、
  > 会回落到 `DefaultTransport`，所以**不需要改任何构造函数签名**。
  > 注意 HN 摘要走的是 `http://127.0.0.1:50051` 的 Python sidecar，
  > 代理程序的规则里应给本机地址留直连。

- [ ] **Step 3: 更新测试清单**

在 Commands 一节里那串测试文件枚举中，`internal/music/*_test.go` 的括号说明里
补上 musicso 的覆盖点：

> `musicso` 的 HTML fixture 解析（含畸形条目跳过、歌名自带连字符）、
> httptest 三跳（搜索页下发会话 → `play.php` 校验会话 → CDN 不带 cookie）、
> Cloudflare 质询识别

- [ ] **Step 4: 校对**

通读改动过的段落，确认没有残留「六项全配」「固定两个目标」「目前只有 mp3.pm」
这类已经过时的说法。

Run: `grep -n "六项\|固定两个\|只有 mp3.pm\|六个平铺" CLAUDE.md`
Expected: 无输出（或仅剩解释历史的语境，需人工确认）

- [ ] **Step 5: 提交**

```bash
git add CLAUDE.md
git commit -m "#docs CLAUDE.md 同步本次改动

[music] 的「六项全配」改成「至少一个完整目标」、新增 sources 有序数组与
musicso 音源、新增 [network] proxy 段及其「不改构造函数签名」的落地方式。"
```

---

## 收尾验收

全部任务完成后跑一遍：

```bash
go build ./...
go vet ./...
go test -short ./...
go test -short -race ./internal/music ./internal/config
gofmt -l internal/music internal/config cmd/news2tg   # 只看本次碰过的目录
```

**注意 `gofmt -l .` 在本仓库天然会列出约 26 个既有文件（CRLF 行尾），不是本次引入的问题，别拿它当验收门。** 只需确认自己新建/改过的文件不在输出里；若在，用 `gofmt -w <文件>` 处理。

手工冒烟（需要真实凭据，在能访问 musicso.cc 的机器上）：

1. `myconfig.toml` 里只填**一个** WebDAV 目标 + `sources = ["musicso", "mp3pm"]`
2. 启动，确认日志出现 `音乐功能已启用 targets=1 sources=[musicso mp3pm]`
3. Telegram 里发 `/music 晴天 周杰伦`
4. 期望：进度消息里曲目行是 `周杰伦 - 晴天 · 10.3 MB · musicso`（**没有**悬空的 ` · `），
   上传区只有一行，终态打勾
