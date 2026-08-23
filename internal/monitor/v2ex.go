package monitor

// 标准库 import
import (
	"context"       // 上下文：传递 cancel/超时
	"encoding/json" // JSON 序列化/反序列化
	"fmt"           // 格式化输出（Printf / Sprintf / Errorf）
	"io"            // io.ReadAll 等通用 I/O 工具
	"log/slog"      // Go 1.21+ 官方结构化日志
	"net/http"      // HTTP 客户端/服务端
	"strings"       // 字符串处理（Contains / ToLower 等）
	"time"          // 时间/定时器

	// 本项目内部包
	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/logx"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/store"
	"github.com/cheedonghu/news2tg/internal/tools"
)

// const 块：一次声明多个常量。
// 字符串常量类型自动推断，不需要写 string。
const (
	v2exHotURL    = "https://www.v2ex.com/api/topics/hot.json"
	v2exLatestURL = "https://www.v2ex.com/api/topics/latest.json"
	v2exUserAgent = "PostmanRuntime/7.37.3" // 伪装成 Postman，避免被默认 UA 拦
)

// V2EX 是一个结构体（struct），代表 v2ex 的监控器实例。
// Go 没有 class，结构体 + 方法就是"对象"。
//
// 字段首字母大写=对外可见（exported），小写=包内私有。这里全部小写：外部不能直接读改这些字段，
// 只能通过 NewV2EX 构造、通过方法操作。
type V2EX struct {
	httpClient *http.Client    // 指针：HTTP 客户端是共享资源（连接池），不要拷贝
	notifier   notify.Notifier // 接口类型：通过依赖注入，测试时可以传 mock
	store      store.Store     // 接口类型：推送记录持久化，实现是 SQLite

	// 端点地址做成字段（默认取上面的常量），只为让测试能指向 httptest 服务器。
	hotURL    string
	latestURL string
}

// NewV2EX 是"构造函数"。Go 没有 constructor 语法，约定用 New<Type> 函数代替。
// 返回 *V2EX（指针），让调用方拿到的是同一个实例，不会被拷贝。
func NewV2EX(httpClient *http.Client, notifier notify.Notifier, st store.Store) *V2EX {
	// &V2EX{...} 创建结构体并返回其地址；这是 Go 里最常见的"new 一个对象"写法。
	return &V2EX{
		httpClient: httpClient,
		notifier:   notifier,
		store:      st,
		hotURL:     v2exHotURL,
		latestURL:  v2exLatestURL,
	}
}

// Run 是 V2EX 的方法。`(m *V2EX)` 叫"receiver"（接收者），
// 写成指针 *V2EX 是为了避免每次调用都拷贝 httpClient/notifier/store 这些字段；
// 实践中只要结构体不是几个字段的小值类型，几乎都用指针 receiver，避免拷贝。
//
// 由于这个方法签名和 Monitor 接口里的 Run 一致，*V2EX 就自动"实现"了 Monitor 接口。
func (m *V2EX) Run(ctx context.Context, cfg *config.Config) error {
	// time.NewTicker(d) 返回一个每 d 时间触发一次的定时器。
	ticker := time.NewTicker(2 * time.Minute)
	// defer：把 ticker.Stop() 推迟到当前函数返回时执行，无论是正常 return 还是 panic。
	// 用来释放资源（关连接、关文件、解锁），保证不会忘。
	defer ticker.Stop()

	// 无限 for 循环：Go 的 for 没有 while 关键字，`for { ... }` 就是死循环。
	for {
		// select 用来同时等多个 channel；哪个先就绪就执行哪个 case。
		// 这里同时等"ctx 被取消"和"ticker 触发"。
		select {
		case <-ctx.Done(): // <-chan 表示从 channel 读取（这里不关心读到的值）
			// ctx.Err() 返回取消原因（context.Canceled / context.DeadlineExceeded）。
			return ctx.Err()
		case <-ticker.C: // ticker.C 是一个 chan Time，每 2 分钟会写入一个值
			// 这个 case 体留空：只是用来"唤醒"，跳出 select 后继续执行下面的代码。
		}

		// 本轮一个 task_id：这轮拉取/通知链路的日志都带上它，便于和并发的别的流程区分。
		cctx := logx.WithTaskID(ctx, logx.NewTaskID())

		// 多返回值：Go 函数可以返回多个值。这里第一个是结果，第二个是 error。
		// `:=` 是"短变量声明"，相当于 `var results []model.NotifyBase; var err error; results, err = ...`。
		results, err := m.fetch(cctx, cfg)
		if err != nil {
			// slog.Error 的可变参数是 key, value, key, value... 形式的结构化日志。
			slog.ErrorContext(cctx, "获取V2EX信息失败", "err", err)
			return err
		}

		// len() 是内置函数，对切片/数组/map/字符串都能用。
		if len(results) > 0 {
			// 逐条发送，发成功才记账。共用的实现见 monitor.go 的 deliver。
			deliver(cctx, m.notifier, m.store, cfg.Telegram.ChatID, results)
		}
	}
}

// fetch 把热帖、新帖拉回来组装成消息列表。
// 单个端点失败只 log、继续走，不让一个失败拖垮整轮。
func (m *V2EX) fetch(ctx context.Context, cfg *config.Config) ([]model.NotifyBase, error) {
	// var 声明零值变量：切片的零值是 nil，对 nil 切片用 len、range、append 都是安全的。
	var hotTopics, newTopics []model.Topic

	if cfg.Features.V2exFetchHot {
		topics, err := m.fetchTopics(ctx, m.hotURL)
		if err != nil {
			slog.ErrorContext(ctx, "v2ex fetch_hot error", "err", err)
		} else {
			hotTopics = topics
		}
	}
	if cfg.Features.V2exFetchLatest {
		topics, err := m.fetchTopics(ctx, m.latestURL)
		if err != nil {
			slog.ErrorContext(ctx, "v2ex fetch_new error", "err", err)
		} else {
			newTopics = topics
		}
	}

	// 函数内的 const 也是合法的，作用域只在本函数内。
	const hotTitle = "热帖推送"
	const newTitle = "新帖推送"

	var result []model.NotifyBase // nil 切片，append 会按需创建底层数组

	// 轮内去重：同一个 URL 可能同时出现在热帖和新帖列表里。
	// 改造前靠"抓取时立即写库"挡住，现在写库后移到发送成功之后，需要这个局部集合顶上。
	//
	// 不加锁是安全的：fetch 全程在单个 goroutine 里顺序执行（上面两个 fetchTopics
	// 是串行调用，没有 go 关键字）。若将来改成并行抓取，这里必须加锁。
	seen := make(map[string]bool)

	// 热帖：不走过滤，直接全推
	for _, topic := range hotTopics {
		// 注意：range 出来的 topic 是值的"拷贝"。要原值用 topics[i]。
		if seen[topic.URL] {
			continue // 本轮已经收过这条了
		}
		// AlreadyPushed 读失败时倾向"少推"：宁可漏几条，也不要在 DB 抖动时
		// 把整页热帖重发一遍炸频道。
		pushed, err := m.store.AlreadyPushed(ctx, store.SourceV2EX, topic.URL)
		if err != nil {
			slog.ErrorContext(ctx, "查询推送记录失败，跳过本条", "url", topic.URL, "err", err)
			continue
		}
		if pushed {
			continue
		}
		title := tools.TruncateUTF8(topic.Title, 4000)
		contentTitle := tools.EscapeMarkdownV2(title)
		// 字面量初始化结构体：字段名: 值，逗号结尾（包括最后一个，Go 强制要求）。
		out := model.NotifyBase{
			Source:     store.SourceV2EX,
			ExternalID: topic.URL, // v2ex 用帖子 URL 作为去重键
			Title:      title,
			PostTitle:  title, // v2ex 的抬头就是帖子标题，两者相同
			URL:        topic.URL,
			Content:    fmt.Sprintf("*%s*: [%s](%s)\n", hotTitle, contentTitle, topic.URL),
		}
		result = append(result, out)
		seen[topic.URL] = true
	}

	// 新帖：走 filterNewTopic（关键字 OR 节点）
	for _, topic := range newTopics {
		if seen[topic.URL] {
			continue // 本轮已经收过这条了
		}
		// AlreadyPushed 读失败时倾向"少推"：宁可漏几条，也不要在 DB 抖动时
		// 把整页热帖重发一遍炸频道。
		pushed, err := m.store.AlreadyPushed(ctx, store.SourceV2EX, topic.URL)
		if err != nil {
			slog.ErrorContext(ctx, "查询推送记录失败，跳过本条", "url", topic.URL, "err", err)
			continue
		}
		if pushed {
			continue
		}
		title := tools.TruncateUTF8(topic.Title, 4000)
		// &topic 取地址：filterNewTopic 接收 *model.Topic，避免拷贝整个结构体。
		// 注意：range 的 topic 在每次迭代被复用，&topic 在循环内用是安全的，
		// 但如果把 &topic 存到外面用，多个元素的指针都会指向同一个位置（经典陷阱）。
		if !filterNewTopic(&topic, cfg) {
			continue
		}
		contentTitle := tools.EscapeMarkdownV2(title)
		out := model.NotifyBase{
			Source:     store.SourceV2EX,
			ExternalID: topic.URL,
			Title:      title,
			PostTitle:  title,
			URL:        topic.URL,
			Content:    fmt.Sprintf("*%s*: [%s](%s)\n", newTitle, contentTitle, topic.URL),
		}
		result = append(result, out)
		seen[topic.URL] = true
	}

	return result, nil // 多返回值用逗号分隔
}

// fetchTopics 拉一个 v2ex JSON 端点。
func (m *V2EX) fetchTopics(ctx context.Context, url string) ([]model.Topic, error) {
	// 创建带 ctx 的请求：ctx 取消时这个请求会被自动中断。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// 早返回（early return）：Go 里非常常见，避免深层嵌套 if-else。
		return nil, err
	}
	req.Header.Set("User-Agent", v2exUserAgent)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		// fmt.Errorf 配合 %w 是错误"包装"：保留原始错误，外层可用 errors.Is/As 判断。
		return nil, fmt.Errorf("network: %w", err)
	}
	// defer 立即压栈，函数结束才执行；这里保证响应体一定被关闭，避免连接泄漏。
	// 注意：defer 注册时机是这一行；如果上面 if err 已经 return，是不会执行 defer 的（resp 也是 nil）。
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var topics []model.Topic
	// json.Unmarshal 把字节解析到 &topics（注意取地址，否则无法修改 topics）。
	if err := json.Unmarshal(body, &topics); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return topics, nil
}

// filterNewTopic 决定一条"新帖"要不要推送。
//
// 语义：
//   - 关键字列表为空 → 视作"该维度不限制"（titleHasKeyword = true）；
//   - 节点列表为空   → 同上；
//   - 两个维度的最终结果用 OR 组合。
//
// 注意：config.toml 的注释说"和上面的关键字是与的关系"，但代码是 OR。
// 实现和注释不一致 —— 测试里专门有用例锁住当前行为，将来想改 AND 测试会变红提醒。
func filterNewTopic(topic *model.Topic, cfg *config.Config) bool {
	// 链式调用：先截断、再 ToLower，让关键字匹配大小写不敏感。
	title := strings.ToLower(tools.TruncateUTF8(topic.Title, 4000))
	nodeName := topic.Node.Name // 嵌套字段访问：点号一层层下去

	// 初值：如果关键字列表为空，直接 true（"不限制"）。
	titleHasKeyword := len(cfg.Features.V2exFetchLatestKeyword) == 0
	if !titleHasKeyword {
		// 列表非空：任一关键字出现在标题里就算命中。
		for _, kw := range cfg.Features.V2exFetchLatestKeyword {
			if strings.Contains(title, kw) {
				titleHasKeyword = true
				break // 命中一个就够，跳出 for
			}
		}
	}

	nodeIncluded := len(cfg.Features.V2exFetchLatestNodeName) == 0
	if !nodeIncluded {
		for _, n := range cfg.Features.V2exFetchLatestNodeName {
			if n == nodeName {
				nodeIncluded = true
				break
			}
		}
	}

	// 最终 OR；想改成 AND 就把 || 换成 &&。
	return titleHasKeyword || nodeIncluded
}
