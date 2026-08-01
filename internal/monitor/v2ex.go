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
	"sync"          // 并发原语（Mutex、RWMutex、WaitGroup 等）
	"time"          // 时间/定时器

	// 本项目内部包
	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/logx"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/notify"
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

	mu         sync.RWMutex      // 读写锁：保护下面这个 map（map 在 Go 里不是线程安全的）
	pushedURLs map[string]string // map[键类型]值类型；这里是 url → yyyymmdd
}

// NewV2EX 是"构造函数"。Go 没有 constructor 语法，约定用 New<Type> 函数代替。
// 返回 *V2EX（指针），让调用方拿到的是同一个实例，不会被拷贝。
func NewV2EX(httpClient *http.Client, notifier notify.Notifier) *V2EX {
	// &V2EX{...} 创建结构体并返回其地址；这是 Go 里最常见的"new 一个对象"写法。
	return &V2EX{
		httpClient: httpClient,
		notifier:   notifier,
		// map 必须用 make 初始化，否则是 nil，写入会 panic。
		pushedURLs: make(map[string]string),
	}
}

// Run 是 V2EX 的方法。`(m *V2EX)` 叫"receiver"（接收者），
// 写成指针 *V2EX 是因为方法里要修改 m.pushedURLs；如果只读，可以写值 receiver `(m V2EX)`。
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
			// make([]T, 长度, 容量)：预分配容量避免 append 时反复扩容。
			contents := make([]string, 0, len(results))
			// for range：遍历切片，第一个返回值是下标（这里用 _ 丢弃），第二个是元素。
			for _, r := range results {
				// append 给切片追加元素。注意必须用返回值赋回去（切片可能扩容换底层数组）。
				contents = append(contents, r.Content)
			}
			for _, c := range contents {
				if err := m.notifier.NotifyMarkdown(cctx, c); err != nil {
					slog.ErrorContext(cctx, "V2EX 通知失败", "err", err)
				}
			}
		}

		m.cleanOldURLs(time.Now())
	}
}

// fetch 把热帖、新帖拉回来组装成消息列表。
// 单个端点失败只 log、继续走，不让一个失败拖垮整轮。
func (m *V2EX) fetch(ctx context.Context, cfg *config.Config) ([]model.NotifyBase, error) {
	// var 声明零值变量：切片的零值是 nil，对 nil 切片用 len、range、append 都是安全的。
	var hotTopics, newTopics []model.Topic

	if cfg.Features.V2exFetchHot {
		topics, err := m.fetchTopics(ctx, v2exHotURL)
		if err != nil {
			slog.ErrorContext(ctx, "v2ex fetch_hot error", "err", err)
		} else {
			hotTopics = topics
		}
	}
	if cfg.Features.V2exFetchLatest {
		topics, err := m.fetchTopics(ctx, v2exLatestURL)
		if err != nil {
			slog.ErrorContext(ctx, "v2ex fetch_new error", "err", err)
		} else {
			newTopics = topics
		}
	}

	// 函数内的 const 也是合法的，作用域只在本函数内。
	const hotTitle = "热帖推送"
	const newTitle = "新帖推送"
	// time.Now() 当前时间；Format 的参数是固定的"参考时间" 2006-01-02 15:04:05，
	// 这是 Go 独特的格式化方式（不是 yyyy-MM-dd），记住就好。
	currentDate := time.Now().Format("20060102")

	var result []model.NotifyBase // nil 切片，append 会按需创建底层数组

	// 热帖：不走过滤，直接全推
	for _, topic := range hotTopics {
		// 注意：range 出来的 topic 是值的"拷贝"。要原值用 topics[i]。
		if m.alreadyPushed(topic.URL) {
			continue // 跳过本次循环
		}
		title := tools.TruncateUTF8(topic.Title, 4000)
		contentTitle := tools.EscapeMarkdownV2(title)
		// 字面量初始化结构体：字段名: 值，逗号结尾（包括最后一个，Go 强制要求）。
		out := model.NotifyBase{
			Title:   title,
			URL:     topic.URL,
			Content: fmt.Sprintf("*%s*: [%s](%s)\n", hotTitle, contentTitle, topic.URL),
		}
		result = append(result, out)
		m.markPushed(topic.URL, currentDate)
	}

	// 新帖：走 filterNewTopic（关键字 OR 节点）
	for _, topic := range newTopics {
		if m.alreadyPushed(topic.URL) {
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
			Title:   title,
			URL:     topic.URL,
			Content: fmt.Sprintf("*%s*: [%s](%s)\n", newTitle, contentTitle, topic.URL),
		}
		result = append(result, out)
		m.markPushed(topic.URL, currentDate)
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

// 下面三个方法实现"按 URL 去重 + 按日期滑窗清理"。
// 进程重启会丢失这个 map（无持久化），这是已知妥协。

// alreadyPushed 用读锁（RLock）：多个 goroutine 可以同时读。
func (m *V2EX) alreadyPushed(url string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock() // defer 解锁：哪怕中间 panic，也不会死锁
	// 从 map 取值：返回"值, 是否存在"。第二返回值习惯叫 ok。
	_, ok := m.pushedURLs[url]
	return ok
}

// markPushed 用写锁（Lock）：独占，写期间没有读也没有写。
func (m *V2EX) markPushed(url, date string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushedURLs[url] = date // map 写入：和读语法一样，但左值写法
}

// cleanOldURLs 删超过 5 天的记录。
// 用字符串比较 yyyymmdd 是 OK 的：字典序刚好等于时间序。
func (m *V2EX) cleanOldURLs(now time.Time) {
	// AddDate(年, 月, 日)，负数表示往前推。
	cutoff := now.AddDate(0, 0, -5).Format("20060102")
	m.mu.Lock()
	defer m.mu.Unlock()
	// range map 同时拿到 key 和 value；Go 在循环中删除 map 元素是安全的（特例！slice 不行）。
	for url, date := range m.pushedURLs {
		if date < cutoff {
			delete(m.pushedURLs, url) // 内置函数 delete，第一个参数是 map，第二个是 key
		}
	}
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
