// 测试文件命名规范：必须以 _test.go 结尾，Go 工具链只在 `go test` 时编译它，
// 正常 `go build` 不会包含进二进制。
//
// package 声明必须和被测代码相同（这里都是 monitor），这样可以访问包内私有函数（如 filterNewTopic）。
// 也可以写成 `package monitor_test` 做"黑盒测试"，那时只能访问导出的（首字母大写的）符号。
package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing" // Go 官方测试框架，提供 *testing.T、t.Run、t.Fatalf 等
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/store"
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

// TestFetchDedup 用 httptest 假装 v2ex API，配真实的临时 SQLite 库，
// 验证两件事：库里已有的 URL 不再产出；同一 URL 同时出现在热帖和新帖时只产出一次。
//
// V2EX 的 fetch 只调 v2ex 自己的 HTTP 接口，不涉及 Python sidecar，所以能这样测。
func TestFetchDedup(t *testing.T) {
	// 两个端点返回的 JSON：/hot 有 t/1 和 t/2；/latest 有 t/2（与热帖重复）和 t/3。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hot":
			w.Write([]byte(`[{"id":1,"title":"帖子一","url":"https://v2ex.com/t/1","node":{"name":"share"}},
			                 {"id":2,"title":"帖子二","url":"https://v2ex.com/t/2","node":{"name":"share"}}]`))
		case "/latest":
			w.Write([]byte(`[{"id":2,"title":"帖子二","url":"https://v2ex.com/t/2","node":{"name":"share"}},
			                 {"id":3,"title":"帖子三","url":"https://v2ex.com/t/3","node":{"name":"share"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, _ := newTestStore(t) // 复用 deliver_test.go 里的辅助函数（同一个包）
	ctx := context.Background()

	// 预先把 t/1 标记成推过，验证"库里已有的不再产出"。
	if err := st.MarkPushed(ctx, store.Record{
		Source: "v2ex", ExternalID: "https://v2ex.com/t/1",
		Title: "帖子一", URL: "https://v2ex.com/t/1",
		ChatID: "-100123", PushedAt: time.Now(),
	}); err != nil {
		t.Fatalf("预置记录失败: %v", err)
	}

	// 直接构造 V2EX（同包测试可以访问私有字段），把端点指向 httptest 服务器。
	m := &V2EX{
		httpClient: srv.Client(),
		store:      st,
		hotURL:     srv.URL + "/hot",
		latestURL:  srv.URL + "/latest",
	}
	cfg := &config.Config{
		Features: config.Features{
			V2exFetchHot:    true,
			V2exFetchLatest: true,
			// 关键字/节点都留空 = 该维度不限制，filterNewTopic 恒为 true
		},
	}

	results, err := m.fetch(ctx, cfg)
	if err != nil {
		t.Fatalf("fetch 报错: %v", err)
	}

	// 期望只剩 t/2 和 t/3：t/1 已推过被过滤，t/2 出现两次但只保留一次。
	if len(results) != 2 {
		t.Fatalf("期望 2 条结果，实际 %d 条: %+v", len(results), results)
	}
	gotURLs := map[string]bool{}
	for _, r := range results {
		gotURLs[r.ExternalID] = true
		if r.Source != "v2ex" {
			t.Errorf("Source = %q, want %q", r.Source, "v2ex")
		}
	}
	if gotURLs["https://v2ex.com/t/1"] {
		t.Errorf("已推过的 t/1 不应出现在结果里")
	}
	if !gotURLs["https://v2ex.com/t/2"] || !gotURLs["https://v2ex.com/t/3"] {
		t.Errorf("t/2 和 t/3 都应出现，实际: %v", gotURLs)
	}
}
