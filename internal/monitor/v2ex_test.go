// 测试文件命名规范：必须以 _test.go 结尾，Go 工具链只在 `go test` 时编译它，
// 正常 `go build` 不会包含进二进制。
//
// package 声明必须和被测代码相同（这里都是 monitor），这样可以访问包内私有函数（如 filterNewTopic）。
// 也可以写成 `package monitor_test` 做"黑盒测试"，那时只能访问导出的（首字母大写的）符号。
package monitor

import (
	"testing" // Go 官方测试框架，提供 *testing.T、t.Run、t.Fatalf 等
	"time"

	"github.com/cheedonghu/news-notify/internal/config"
	"github.com/cheedonghu/news-notify/internal/model"
)

// 测试函数命名规范：必须以 Test 开头 + 大写字母，参数必须是 *testing.T。
// 命中规范的函数才会被 `go test` 当成测试自动执行。
//
// 这是一个"表驱动测试"（table-driven test），是 Go 社区最主流的写法：
// 把多个用例写成一张表（切片），用一个循环跑完，每个 case 一个子测试。
func TestFilterNewTopic(t *testing.T) {
	// 在函数内定义局部函数（闭包）做"工厂方法"，避免每个 case 都重复写一长串字面量。
	// mkTopic 返回 *model.Topic 指针，因为 filterNewTopic 接收的是指针。
	mkTopic := func(title, node string) *model.Topic {
		return &model.Topic{
			Title: title,
			Node:  model.Node{Name: node}, // 嵌套结构体字面量
		}
	}
	mkCfg := func(keywords, nodes []string) *config.Config {
		return &config.Config{
			Features: config.Features{
				V2exFetchLatestKeyword:  keywords,
				V2exFetchLatestNodeName: nodes,
			},
		}
	}

	// 用匿名结构体切片定义用例表。
	// `[]struct{...}{ {...}, {...} }` 这种写法很常见：临时只在这里用的类型不必单独命名。
	cases := []struct {
		name     string         // 子测试名（会出现在 -v 输出和 -run 过滤里）
		topic    *model.Topic   // 被测输入
		cfg      *config.Config // 配置
		expected bool           // 期望输出
	}{
		{
			name:     "两个列表都空 → 不限制，直接通过",
			topic:    mkTopic("随便什么标题", "anynode"),
			cfg:      mkCfg(nil, nil), // 传 nil 切片：len=0，符合"列表为空"分支
			expected: true,
		},
		{
			name:     "关键字命中（节点列表空）",
			topic:    mkTopic("聊聊大模型部署", "anynode"),
			cfg:      mkCfg([]string{"大模型"}, nil),
			expected: true,
		},
		{
			name:     "关键字未命中、节点未命中 → 拒绝",
			topic:    mkTopic("聊聊键盘", "movie"),
			cfg:      mkCfg([]string{"大模型"}, []string{"share"}),
			expected: false,
		},
		{
			name:     "关键字未命中、节点命中 → OR 通过",
			topic:    mkTopic("聊聊键盘", "share"),
			cfg:      mkCfg([]string{"大模型"}, []string{"share"}),
			expected: true,
		},
		{
			name:     "关键字命中、节点未命中 → OR 通过",
			topic:    mkTopic("聊聊大模型", "movie"),
			cfg:      mkCfg([]string{"大模型"}, []string{"share"}),
			expected: true,
		},
		{
			name:     "大小写归一化：标题被 ToLower 后匹配小写关键字",
			topic:    mkTopic("GoLang Tips", "anynode"),
			cfg:      mkCfg([]string{"golang"}, nil),
			expected: true,
		},
		{
			name:     "关键字非空但未命中，节点列表空 → 节点维度自动通过 → OR 通过",
			topic:    mkTopic("无关标题", "anynode"),
			cfg:      mkCfg([]string{"大模型"}, nil),
			expected: true,
		},
		{
			name:     "多个关键字任一命中即可",
			topic:    mkTopic("机械键盘开箱", "anynode"),
			cfg:      mkCfg([]string{"大模型", "键盘"}, []string{"share"}),
			expected: true,
		},
	}

	for _, c := range cases {
		// 经典陷阱：在 for-range 里启动并发或用闭包时，循环变量 c 是被复用的同一个地址。
		// Go 1.22 之前必须像下面这样"shadow"一次（重新声明同名局部变量），1.22+ 已自动每轮新建。
		// 出于稳妥，加上这行不会有坏处。
		c := c

		// t.Run 启动一个"子测试"，第一参数是名字，第二参数是一个 func(*testing.T)。
		// 优点：1) 输出更清晰；2) 可以用 -run 过滤跑单个；3) 子测试可以并发（t.Parallel()）。
		t.Run(c.name, func(t *testing.T) {
			got := filterNewTopic(c.topic, c.cfg)
			if got != c.expected {
				// t.Fatalf：报错并立即停止当前子测试（其它子测试不受影响）。
				// 对比 t.Errorf：只报错不停。Fatal 系列适合"再往下也没意义"的场景。
				// 注意：Fatal 只能在测试 goroutine 里调，不能在子 goroutine 里调（会 panic）。
				t.Fatalf("filterNewTopic(%q, node=%q) = %v, want %v",
					c.topic.Title, c.topic.Node.Name, got, c.expected)
			}
		})
	}
}

// TestCleanOldURLs 验证按日期滑窗清理的边界。
// cutoff = now - 5 天；条件是 `date < cutoff`，所以"等于 cutoff"应保留。
func TestCleanOldURLs(t *testing.T) {
	// time.Date 显式构造时间，避免依赖系统当前时间（让测试可重复）。
	// 参数顺序：年, 月, 日, 时, 分, 秒, 纳秒, Location。
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	// cutoff = 2026-05-11

	// 直接用 &V2EX{...} 构造，绕过 NewV2EX —— 测试只关心 pushedURLs 这个字段，
	// httpClient / notifier 用不上，留零值（nil）也无所谓，因为我们不会调到它们。
	v := &V2EX{pushedURLs: map[string]string{
		"https://v2ex.com/t/old":    "20260510", // 早于 cutoff → 应删
		"https://v2ex.com/t/edge":   "20260511", // 等于 cutoff → 应保留（date < cutoff 为 false）
		"https://v2ex.com/t/recent": "20260515", // 晚于 cutoff → 应保留
	}}

	v.cleanOldURLs(now)

	// 用"逗号 ok"语法判断 key 是否存在。
	// _, ok := m[k]：值丢弃（如果不需要），ok 是 bool。
	if _, ok := v.pushedURLs["https://v2ex.com/t/old"]; ok {
		t.Errorf("超过 5 天的记录应该被删除") // Errorf：报错但继续往下跑
	}
	if _, ok := v.pushedURLs["https://v2ex.com/t/edge"]; !ok {
		t.Errorf("正好等于 cutoff 的记录应该保留")
	}
	if _, ok := v.pushedURLs["https://v2ex.com/t/recent"]; !ok {
		t.Errorf("近期记录不应被删除")
	}
}
