# 推送记录改用 SQLite 持久化 · 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 V2EX / Hacker News 的推送去重记录从进程内 map 迁移到嵌入式 SQLite 文件，使其重启后保留、可直接用 `sqlite3` 查询历史。

**Architecture:** 新增 `internal/store` 包（`Store` 接口 + SQLite 实现），monitor 通过依赖注入持有接口，不感知具体存储。去重改为永久（删除滑窗清理）。`MarkPushed` 从"抓取时"后移到"Telegram 发送成功之后"，为此把 `Notifier.NotifyBatch` 拆成逐条的 `NotifyMarkdown` + 下沉到 Telegram 客户端的全局限速。

**Tech Stack:** Go 1.22+、`modernc.org/sqlite`（纯 Go，无 cgo）、`database/sql`、`net/http/httptest`

## Global Constraints

- **禁止 cgo**：`Dockerfile` 用 `CGO_ENABLED=0 GOOS=linux go build` 静态编译。只能用 `modernc.org/sqlite`，**不得**引入 `github.com/mattn/go-sqlite3`。
- **Go 版本**：`go.mod` 当前是 `go 1.22`。若 `go get modernc.org/sqlite` 报要求更高版本，把 `go.mod` 的 `go` 指令提升到该版本（本项目无其它依赖卡在 1.22），**不要**去锁一个兼容 1.22 的旧版驱动。
- **双语教学注释**：本仓库几乎每行都有解释 Go 语义的中文注释，这是house style。新增代码必须匹配这个密度和风格，不要写成"干净的"无注释代码。
- **AI/digest 失败不阻断推送**：`DeepSeek.Summarize` 和 digest 抓取返回兜底字符串 + `nil` error 是有意设计，不得改动。
- **Legacy parity**：`formatHNMessage`（`internal/monitor/hackernews.go:297`）逐字节复刻旧 Rust 版输出格式，`judgeNewsDate` 只认 `"N hours ago"`。**不得**改动这两处的行为，包括传给 `formatHNMessage` 的第一个参数（HN 的 `NotifyBase.Title` 是分类抬头如 `"Hacker News 热帖推送"`，不是帖子标题）。
- **日志**：任务链路内用 `slog.InfoContext` / `slog.ErrorContext` 配合带 `task_id` 的 ctx；`main` 里的基础设施日志用普通 `slog.*`。
- **测试命令**：`go test -short ./...`。**必须带 `-short`** —— `internal/agent` 有会真实调用 DeepSeek API 的 e2e 测试，不带会产生真实费用。
- **不要用 `gofmt -l .` 当验收门禁**：本仓库因 CRLF 天然会列出二十余个文件。只对本次新增/修改的文件跑 `gofmt -l <file>`。
- **提交信息前缀**：沿用仓库习惯 `#feat` / `#fix` / `#docs` / `#style`，中文描述。

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `internal/store/store.go` | 创建 | `Record` DTO + `Store` 接口。monitor 只依赖这里 |
| `internal/store/sqlite.go` | 创建 | SQLite 实现：打开、建表、查询、写入 |
| `internal/store/sqlite_test.go` | 创建 | 对真实临时库文件的单测 |
| `internal/config/config.go` | 修改 | 新增 `Storage` 段 + 必填校验 |
| `config.toml` | 修改 | 新增 `[storage]` 模板 |
| `docker-compose.yml` | 修改 | 新增 `./data:/data` 卷 |
| `Dockerfile` | 修改 | 创建 `/data` 目录 |
| `.gitignore` | 修改 | 忽略 `/data/` |
| `cmd/news2tg/main.go` | 修改 | 打开 DB、注入 monitor、退出时关闭 |
| `internal/notify/notify.go` | 修改 | `NotifyBatch` → `NotifyMarkdown` |
| `internal/notify/telegram.go` | 修改 | 客户端级限速 + `NotifyMarkdown` |
| `internal/monitor/weather.go` | 修改 | 改调 `NotifyMarkdown`（1 行） |
| `internal/monitor/weather_test.go` | 修改 | `fakeNotifier` 跟随接口变更 |
| `internal/model/model.go` | 修改 | `NotifyBase` 增 `Source` / `ExternalID` / `PostTitle` |
| `internal/monitor/monitor.go` | 修改 | 新增共享的 `deliver` 函数（发送成功才记账） |
| `internal/monitor/deliver_test.go` | 创建 | `deliver` 的单测（fake notifier + 真实临时库） |
| `internal/monitor/v2ex.go` | 修改 | 接入 store，删除 map/mutex/滑窗清理 |
| `internal/monitor/v2ex_test.go` | 修改 | 删 `TestCleanOldURLs`，加 `fetch` 集成测试 |
| `internal/monitor/hackernews.go` | 修改 | 接入 store，提取帖子标题 |
| `internal/monitor/hackernews_test.go` | 创建 | 帖子标题提取的纯函数测试 |
| `CLAUDE.md` | 修改 | 同步 Dedup / Notifier / Config / 接口清单四处 |

**任务顺序原则**：每个任务结束时整个仓库必须能编译、能跑通 `go test -short ./...`。因此接口变更任务（Task 3）内部要顺手把调用方机械改造到位，即使语义要到后续任务才完整。

---

### Task 1: `internal/store` 包

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/sqlite.go`
- Test: `internal/store/sqlite_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: 无（本任务是最底层）
- Produces:
  - `store.Record{Source, ExternalID, Title, URL, ChatID string; PushedAt time.Time}`
  - `store.Store` 接口：`AlreadyPushed(ctx context.Context, source, externalID string) (bool, error)`、`MarkPushed(ctx context.Context, rec Record) error`、`Close() error`
  - `store.OpenSQLite(path string) (*store.SQLite, error)`

- [ ] **Step 1: 添加依赖**

```bash
go get modernc.org/sqlite@latest
go mod tidy
```

如果报错要求 Go 版本高于 1.22，编辑 `go.mod` 把 `go 1.22` 改成它要求的版本（如 `go 1.23`），再重跑上面两条命令。

- [ ] **Step 2: 写接口与 DTO**

创建 `internal/store/store.go`：

```go
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
```

- [ ] **Step 3: 写失败的测试**

创建 `internal/store/sqlite_test.go`：

```go
package store

import (
	"context"          // 测试里用 context.Background()
	"path/filepath"    // 拼临时目录下的库文件路径
	"testing"          // Go 官方测试框架
	"time"
)

// newTestStore 开一个指向临时目录的真实 SQLite 库。
// t.TempDir() 返回的目录会在测试结束后自动删除，不用手动清理。
// t.Cleanup 注册收尾函数，保证连接一定被关掉。
func newTestStore(t *testing.T) *SQLite {
	t.Helper() // 标记为辅助函数：失败时行号指向调用处而不是这里
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%q) 失败: %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAlreadyPushedAndMark(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 1) 空库：没推过
	ok, err := s.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("空库应返回 false")
	}

	// 2) 写入后：推过了
	rec := Record{
		Source:     "v2ex",
		ExternalID: "https://v2ex.com/t/1",
		Title:      "测试帖子",
		URL:        "https://v2ex.com/t/1",
		ChatID:     "-100123",
		PushedAt:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("MarkPushed 报错: %v", err)
	}
	ok, err = s.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if !ok {
		t.Fatalf("写入后应返回 true")
	}
}

// 重复写入必须幂等：发送成功但写库失败的重试路径依赖这一点，
// 否则唯一约束冲突会在日志里刷 ERROR。
func TestMarkPushedIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := Record{
		Source:     "hackernews",
		ExternalID: "44556677",
		Title:      "Some HN Post",
		URL:        "https://news.ycombinator.com/item?id=44556677",
		ChatID:     "-100123",
		PushedAt:   time.Now(),
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("第一次 MarkPushed 报错: %v", err)
	}
	if err := s.MarkPushed(ctx, rec); err != nil {
		t.Fatalf("重复 MarkPushed 应幂等，却报错: %v", err)
	}
}

// 不同 source 下的相同 external_id 互不干扰（联合唯一，不是单列唯一）。
func TestSourcesAreIsolated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.MarkPushed(ctx, Record{
		Source: "v2ex", ExternalID: "42", Title: "t", URL: "u",
		ChatID: "c", PushedAt: time.Now(),
	}); err != nil {
		t.Fatalf("MarkPushed 报错: %v", err)
	}
	ok, err := s.AlreadyPushed(ctx, "hackernews", "42")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("另一个 source 的相同 id 不应被视作推过")
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `go test ./internal/store/ -v`
Expected: 编译失败，`undefined: OpenSQLite` / `undefined: SQLite`

- [ ] **Step 5: 写 SQLite 实现**

创建 `internal/store/sqlite.go`：

```go
package store

import (
	"context"
	"database/sql" // 标准库的 SQL 抽象层；具体驱动通过匿名 import 注册
	"errors"       // errors.Is 判断哨兵错误
	"fmt"
	"time"

	// 匿名 import（下划线别名）：只为触发包的 init()，把驱动注册进 database/sql。
	// modernc.org/sqlite 注册的驱动名是 "sqlite"（注意不是 "sqlite3"）。
	// 它是把 SQLite 的 C 源码机器翻译成的纯 Go 实现，所以 CGO_ENABLED=0 也能编译。
	_ "modernc.org/sqlite"
)

// SQLite 是 Store 接口的 SQLite 实现。
type SQLite struct {
	db *sql.DB // *sql.DB 本身是连接池，并发安全，不要拷贝
}

// schemaSQL 建表语句。用 IF NOT EXISTS，所以每次启动执行都是安全的 ——
// 这就是本项目全部的"迁移"机制。
//
// (source, external_id) 联合唯一：两个来源的 id 空间不同（URL vs 数字），
// 加上 source 前缀就不会撞车。这个唯一索引同时充当去重查询的索引，不用另建。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS pushed_posts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL,
    external_id TEXT NOT NULL,
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    pushed_at   TEXT NOT NULL,
    UNIQUE(source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_pushed_at ON pushed_posts(pushed_at);
`

// OpenSQLite 打开（或创建）库文件并建表。
//
// DSN 里的 _pragma 是 modernc 驱动的扩展语法，每个连接建立时都会执行：
//   - busy_timeout(5000)  锁冲突时最多等 5 秒再报错，而不是立刻失败。
//     必须写在 DSN 里 —— 它是"每连接"设置，连接池里每条连接都要有。
//   - journal_mode(WAL)   写前日志模式，读不阻塞写。这个设置会持久化进库文件。
func OpenSQLite(path string) (*SQLite, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)

	// sql.Open 只是准备好连接池，并不会真正连接，所以下面还要 Ping 一次。
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite %q 失败: %w", path, err)
	}
	// Ping 才会真正建连：路径不可写、目录不存在等问题在这里暴露。
	if err := db.Ping(); err != nil {
		db.Close() // 失败路径要记得关，否则连接池泄漏
		return nil, fmt.Errorf("连接 sqlite %q 失败: %w", path, err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}
	return &SQLite{db: db}, nil
}

// AlreadyPushed 查这条记录是否已存在。
// SELECT 1 ... LIMIT 1：只关心"有没有"，不取实际字段，最省事。
func (s *SQLite) AlreadyPushed(ctx context.Context, source, externalID string) (bool, error) {
	const q = `SELECT 1 FROM pushed_posts WHERE source = ? AND external_id = ? LIMIT 1`
	var one int
	// QueryRowContext + Scan：期望最多一行。没有行时 Scan 返回 sql.ErrNoRows。
	err := s.db.QueryRowContext(ctx, q, source, externalID).Scan(&one)
	// errors.Is 而不是 ==：驱动可能包装过这个哨兵错误。
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // 没查到 = 没推过，这不是错误
	}
	if err != nil {
		return false, fmt.Errorf("查询 pushed_posts 失败: %w", err)
	}
	return true, nil
}

// MarkPushed 写一条记录。
//
// ON CONFLICT ... DO NOTHING 让重复写入变成静默的空操作。
// 这很重要：发送成功但写库失败时，条目下一轮会被重推并再次写入，
// 若这里报唯一约束冲突，日志会被无意义的 ERROR 刷屏。
func (s *SQLite) MarkPushed(ctx context.Context, rec Record) error {
	const q = `INSERT INTO pushed_posts (source, external_id, title, url, chat_id, pushed_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(source, external_id) DO NOTHING`
	// 时间统一存 RFC3339 字符串：它的字典序等于时间序，
	// 所以 WHERE pushed_at > '2026-07-01' 这类区间查询能直接用。
	_, err := s.db.ExecContext(ctx, q,
		rec.Source, rec.ExternalID, rec.Title, rec.URL, rec.ChatID,
		rec.PushedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("写入 pushed_posts 失败: %w", err)
	}
	return nil
}

// Close 关闭连接池。main 里 defer 调用。
func (s *SQLite) Close() error {
	return s.db.Close()
}

// 编译期断言：如果 *SQLite 没有完整实现 Store 接口，这行会编译失败。
// 这是 Go 里检查"实现了某接口"的标准写法（变量名用 _ 表示不占用符号）。
var _ Store = (*SQLite)(nil)
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS，三个测试全绿

- [ ] **Step 7: 提交**

```bash
git add go.mod go.sum internal/store/
git commit -m "#feat 新增 store 包：推送记录的 SQLite 持久化

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: 配置、部署与 main 接线

**Files:**
- Modify: `internal/config/config.go`（`Config` 结构体 + `FromFile` 校验）
- Modify: `config.toml`
- Modify: `docker-compose.yml`
- Modify: `Dockerfile:28`
- Modify: `.gitignore`
- Modify: `cmd/news2tg/main.go`

**Interfaces:**
- Consumes: `store.OpenSQLite(path) (*store.SQLite, error)`（Task 1）
- Produces: `cfg.Storage.DBPath string`；`main` 中已打开、可注入 monitor 的 `pushStore` 变量

- [ ] **Step 1: 加配置结构体与校验**

`internal/config/config.go`，在 `Jina` 结构体（约 87-89 行）之后新增：

```go
// Storage 段：推送记录数据库文件路径。
// 和模型名一样不留 Go 侧默认值 —— "库放哪"只有配置文件一个答案，缺了就启动失败。
type Storage struct {
	DBPath string `toml:"db_path"`
}
```

在 `Config` 结构体里加一个字段：

```go
type Config struct {
	Telegram Telegram `toml:"telegram"`
	Features Features `toml:"features"`
	DeepSeek DeepSeek `toml:"deepseek"`
	Jina     Jina     `toml:"jina"`
	Storage  Storage  `toml:"storage"` // 新增
}
```

在 `FromFile` 里，紧跟现有两条模型名校验之后（`return &cfg, nil` 之前）加：

```go
	if strings.TrimSpace(cfg.Storage.DBPath) == "" {
		return nil, fmt.Errorf("[storage] db_path 未配置")
	}
```

- [ ] **Step 2: 更新配置模板**

`config.toml` 末尾追加：

```toml
# 推送记录数据库（SQLite 文件）。容器里对应 docker-compose 挂载的 ./data 卷。
# 本地开发可指到 ./target/dev.db（target/ 已在 .gitignore 里）。
# 留空会导致启动失败——代码里没有默认值。
[storage]
db_path = "/data/news2tg.db"
```

- [ ] **Step 3: 更新部署文件**

`docker-compose.yml` 的 `volumes` 段改为：

```yaml
    volumes:
      # 使用相对路径挂载本地目录（推荐）
      - ./config:/config
      # 推送记录数据库；与只读的配置分开，职责清晰
      - ./data:/data
```

`Dockerfile` 第 28 行 `RUN mkdir /config` 改为：

```dockerfile
# 创建配置目录和数据目录（数据目录在 compose 里会被宿主机卷覆盖，
# 但不挂载时也需要它存在，否则 SQLite 建库会失败）
RUN mkdir /config /data
```

`.gitignore` 在 `/target` 那行之后追加：

```
/data/
```

- [ ] **Step 4: main 里打开数据库**

`cmd/news2tg/main.go` 的 import 块加一行：

```go
	"github.com/cheedonghu/news2tg/internal/store"
```

在第 8 步（构造 `httpClient`）之前、第 7 步发启动通知之后插入：

```go
	// 7.5) 打开推送记录数据库。
	// 打不开就退出：去重是核心功能，静默降级会让人在不知情的情况下重复刷屏。
	// 这条同时兜住"忘了在 docker-compose 里加 ./data:/data 挂载"的场景。
	pushStore, err := store.OpenSQLite(cfg.Storage.DBPath)
	if err != nil {
		slog.Error("failed to open push record store", "path", cfg.Storage.DBPath, "err", err)
		os.Exit(1)
	}
	// defer 在 main 正常返回时执行；注意上面那些 os.Exit 路径不会触发 defer，
	// 但那时进程已经要死了，OS 会回收 fd，不影响。
	defer pushStore.Close()
	slog.Info("push record store opened", "path", cfg.Storage.DBPath)
```

- [ ] **Step 5: 准备本地配置**

`myconfig.toml` 是 `.gitignore` 里的本地文件（不进版本控制），需要手动加同样的一段，否则本地运行会因校验失败而退出：

```toml
[storage]
db_path = "./target/dev.db"
```

确认 `target/` 目录存在：`mkdir -p target`

- [ ] **Step 6: 验证编译与测试**

Run: `go build ./... && go test -short ./...`
Expected: 编译通过，测试全绿（此时 `pushStore` 只是被打开和关闭，还没注入 monitor）

- [ ] **Step 7: 提交**

```bash
git add internal/config/config.go config.toml docker-compose.yml Dockerfile .gitignore cmd/news2tg/main.go
git commit -m "#feat 新增 [storage] 配置段并在启动时打开推送记录库

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: Notifier 接口改造（逐条发送 + 客户端级限速）

**Files:**
- Modify: `internal/notify/notify.go:25-29`
- Modify: `internal/notify/telegram.go`
- Modify: `internal/monitor/weather.go:224-225`
- Modify: `internal/monitor/weather_test.go:148-154`
- Modify: `internal/monitor/v2ex.go:101-104`（机械改造，语义留到 Task 5）
- Modify: `internal/monitor/hackernews.go:91-93`（机械改造，语义留到 Task 6）

**Interfaces:**
- Consumes: 无
- Produces: `notify.Notifier` 接口的第三个方法 `NotifyMarkdown(ctx context.Context, content string) error`；`NotifyBatch` 从接口和实现中删除

**背景**：要做到"发送成功才记账"就需要逐条的成功信号。这个信号在 `telegram.go:67` 本来就存在，只是被丢掉了（单条失败仅 log 并继续）。这里取消"批量"这层假象，把两个职责拆开：逐条发送归调用方，限速归 Telegram 客户端。

- [ ] **Step 1: 改接口**

`internal/notify/notify.go`，把接口定义（25-29 行）改为：

```go
type Notifier interface {
	Notify(ctx context.Context, content string) error
	NotifyTo(ctx context.Context, chatID int64, content string) error
	NotifyMarkdown(ctx context.Context, content string) error
}
```

同时把上方注释块里的方法语义说明改为：

```go
// 方法语义：
//   - Notify          发单条消息到默认目标（配置的 chat/频道）
//   - NotifyTo        发单条消息到指定 chatID（如回复发指令的用户）
//   - NotifyMarkdown  发单条**已渲染好的 MarkdownV2** 到默认目标
//
// 转义约定（重要）：
//   - Notify / NotifyTo 把 content 当**纯文本**，由实现内部负责 MarkdownV2 转义，
//     调用方不要再自己 EscapeMarkdownV2。
//   - NotifyMarkdown 发的是**已经拼好的 MarkdownV2**（如 monitor 里带 *加粗*/[链接] 的消息），
//     实现不做整体转义；这类消息在构建时自行转义动态片段。
//
// 限速：由实现内部保证（Telegram 实现里任意两次发送间隔 ≥1.5s），
// 调用方直接循环调用即可，不需要自己 sleep。
```

- [ ] **Step 2: 改 Telegram 实现**

`internal/notify/telegram.go`：import 块加 `"sync"`，结构体和方法改为：

```go
// sendInterval 是任意两次发送之间的最小间隔，用来躲开 Telegram 的频率限制。
const sendInterval = 1500 * time.Millisecond

type Telegram struct {
	bot    *tgbotapi.BotAPI // SDK 的 bot 客户端实例
	chatID int64            // 目标聊天/频道 ID（Telegram 用 int64，可能是负数）

	// 限速状态。以前这个 sleep 埋在 NotifyBatch 里，只保护批量路径；
	// 下沉到客户端后，weather、/summary 回复等所有发送路径共享同一个节流阀。
	mu       sync.Mutex // 保护 lastSend，同时让发送整体串行化
	lastSend time.Time  // 上次发送完成的时刻；零值表示"从没发过"
}
```

`NewTelegram` 不变（新字段用零值即可，`time.Time{}` 的零值让第一次发送不等待）。

新增内部方法 `send`，并让三个公开方法都走它：

```go
// send 是所有发送路径的唯一出口：先取得限速许可，再真正发。
//
// 整个方法持有 mu，所以发送是全局串行的 —— 这正是想要的：
// Telegram 的限额是按 bot 算的，不是按调用点算的。
// 代价是 HN 推 20 条期间，/summary 的回复会排队等待。
func (t *Telegram) send(ctx context.Context, msg tgbotapi.MessageConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// time.Since(零值) 是一个巨大的正数，所以第一次发送 wait <= 0，不等待。
	if wait := sendInterval - time.Since(t.lastSend); wait > 0 {
		// 用 Timer 而不是 time.Sleep：这样等待期间可以被 ctx 取消。
		timer := time.NewTimer(wait)
		defer timer.Stop() // 提前返回时释放 timer，避免泄漏
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}

	_, err := t.bot.Send(msg)
	// 无论成功失败都更新时间戳：失败往往也是被服务端限速了，更该等。
	t.lastSend = time.Now()
	if err != nil {
		return fmt.Errorf("telegram 消息推送失败: %w", err)
	}
	return nil
}

// NotifyTo 发一条消息到指定 chatID（如回复发指令的用户）。
// content 当**纯文本**：MarkdownV2 转义在这里统一做，调用方不用再 EscapeMarkdownV2。
func (t *Telegram) NotifyTo(ctx context.Context, chatID int64, content string) error {
	msg := tgbotapi.NewMessage(chatID, tools.EscapeMarkdownV2(content))
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.DisableWebPagePreview = false
	if err := t.send(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "telegram单笔信息推送失败", "err", err)
		return err
	}
	return nil
}

// NotifyMarkdown 发一条**已渲染好的 MarkdownV2** 到默认 chat。
// 与 Notify 的区别：不做整体转义 —— 调用方（monitor）已经把动态片段各自转义好了，
// 再转一次会把 *加粗* 和 [链接](url) 的标记本身也转义掉。
func (t *Telegram) NotifyMarkdown(ctx context.Context, content string) error {
	slog.InfoContext(ctx, content)
	msg := tgbotapi.NewMessage(t.chatID, content)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.DisableWebPagePreview = false
	if err := t.send(ctx, msg); err != nil {
		slog.ErrorContext(ctx, "telegram预渲染消息推送失败", "err", err)
		return err
	}
	return nil
}
```

删除整个 `NotifyBatch` 方法。`Notify` 保持不变（它只是转调 `NotifyTo`）。

- [ ] **Step 3: 改 weather 调用点**

`internal/monitor/weather.go:224-225` 改为：

```go
	// 单条预渲染 MarkdownV2，正对应 NotifyMarkdown 的语义。
	return w.notifier.NotifyMarkdown(ctx, msg)
```

`weather.go:264` 的注释里 `NotifyBatch` 改成 `NotifyMarkdown`（行为不变：ctx 取消时仍返回 `ctx.Err()`）。

- [ ] **Step 4: 机械改造两个 monitor 的调用点**

这一步只保证编译和现有行为，语义调整留到 Task 5/6。

`internal/monitor/v2ex.go` 的 101-104 行改为：

```go
			for _, c := range contents {
				if err := m.notifier.NotifyMarkdown(cctx, c); err != nil {
					slog.ErrorContext(cctx, "V2EX 通知失败", "err", err)
				}
			}
```

`internal/monitor/hackernews.go` 的 91-93 行改为：

```go
			for _, c := range contents {
				if err := m.notifier.NotifyMarkdown(cctx, c); err != nil {
					slog.ErrorContext(cctx, "HN 通知失败", "err", err)
				}
			}
```

- [ ] **Step 5: 改测试里的 fake**

`internal/monitor/weather_test.go:148-154` 改为：

```go
type fakeNotifier struct{ batch []string }                                               // 捕获发出去的消息
func (f *fakeNotifier) Notify(ctx context.Context, content string) error                 { return nil }
func (f *fakeNotifier) NotifyTo(ctx context.Context, chatID int64, content string) error { return nil }
func (f *fakeNotifier) NotifyMarkdown(ctx context.Context, content string) error {
	f.batch = append(f.batch, content)
	return nil
}
```

注意从"整批赋值"（`f.batch = contents`）改成了 `append`。`TestPushOnce` 的两个子测试分别断言
`len(fn.batch) != 1` + `fn.batch[0]` 的内容，以及全部城市失败时不推送 —— 在 `append` 语义下
两者都仍然成立（推 1 条 → `len == 1`；推 0 条 → `fn.batch` 是 nil，`len == 0`），
`weather_test.go` 的断言无需改动。

- [ ] **Step 6: 验证编译与测试**

Run: `go build ./... && go test -short ./...`
Expected: 全绿。若 `weather_test.go` 有断言失败，检查 Step 5 的注意事项。

- [ ] **Step 7: 提交**

```bash
git add internal/notify/ internal/monitor/weather.go internal/monitor/weather_test.go internal/monitor/v2ex.go internal/monitor/hackernews.go
git commit -m "#refactor NotifyBatch 拆成 NotifyMarkdown，限速下沉到 telegram 客户端

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: `NotifyBase` 扩展 + HN 帖子标题提取

**Files:**
- Modify: `internal/model/model.go:10-16`
- Modify: `internal/monitor/hackernews.go`（`getNewsOriginURL` → `getNewsOriginInfo`，`process` 填新字段）
- Modify: `internal/monitor/v2ex.go`（`fetch` 填新字段）
- Test: `internal/monitor/hackernews_test.go`（创建）

**Interfaces:**
- Consumes: 无
- Produces:
  - `model.NotifyBase` 新增三个字段：`Source string`、`ExternalID string`、`PostTitle string`
  - `getNewsOriginInfo(ctx context.Context, htmlBody string) (originURL, postTitle string, err error)` 替代 `getNewsOriginURL`

**背景**：`Record` 需要 source / 外部 id / 帖子标题，但当前 `NotifyBase` 一个都没有 ——
HN 的数字 id 在 `process` 里 `markPushed(id)` 后就丢了；而 HN 的 `NotifyBase.Title` 存的是
**分类抬头**（`"Hacker News 热帖推送"`，`hackernews.go:139/145` 赋值），被 `formatHNMessage`
当消息头用（legacy parity，不能动）。所以帖子真实标题需要单独解析并单独存放。
好消息是 `getNewsOriginURL` 已经选中了 `span.titleline a` 这个元素，标题就是它的文本。

- [ ] **Step 1: 扩展 NotifyBase**

`internal/model/model.go`，`NotifyBase` 改为：

```go
type NotifyBase struct {
	Source                    string // 来源标识："v2ex" | "hackernews"，写推送记录用
	ExternalID                string // 该来源内的唯一标识：v2ex 填帖子 URL，hn 填帖子数字 id
	URL                       string // 帖子主页 URL
	OriginURL                 string // 原文链接（HN 才有，v2ex 是空）
	Title                     string // 消息抬头。v2ex 是帖子标题；**HN 是分类名**（如"Hacker News 热帖推送"）
	PostTitle                 string // 帖子真实标题。v2ex 与 Title 相同；HN 从帖子页解析而来
	Content                   string // 最终要发送的消息正文（已经拼好模板）
	ContentTransferedByAIFlag bool   // 标记：true=已交给 AI 处理过 / false=未处理（兜底文案）
}
```

- [ ] **Step 2: 写失败的测试**

创建 `internal/monitor/hackernews_test.go`：

```go
package monitor

import (
	"context"
	"testing"
)

// hnItemHTML 是一段裁剪过的 HN 帖子页 HTML，只保留解析要用到的结构。
// 用固定字符串而不是真实网络请求：这样测试不依赖外网，也不碰 Python sidecar。
const hnItemHTML = `<html><body><table><tbody>
<tr class="athing"><td class="title">
<span class="titleline"><a href="https://example.com/post">A Real Post Title</a>
<span class="sitebit comhead"> (example.com)</span></span></td></tr>
<tr><td class="subtext"><span class="age" title="2026-08-01T00:00:00">3 hours ago</span></td></tr>
</tbody></table></body></html>`

func TestGetNewsOriginInfo(t *testing.T) {
	ctx := context.Background()
	originURL, postTitle, err := getNewsOriginInfo(ctx, hnItemHTML)
	if err != nil {
		t.Fatalf("getNewsOriginInfo 报错: %v", err)
	}
	if originURL != "https://example.com/post" {
		t.Errorf("originURL = %q, want %q", originURL, "https://example.com/post")
	}
	if postTitle != "A Real Post Title" {
		t.Errorf("postTitle = %q, want %q", postTitle, "A Real Post Title")
	}
}

// 相对链接（如 "item?id=123"，Ask HN 这类没有外链的帖子）应被判为异常，
// 与改造前 getNewsOriginURL 的行为保持一致。
func TestGetNewsOriginInfoRejectsRelativeHref(t *testing.T) {
	const html = `<html><body><span class="titleline"><a href="item?id=123">Ask HN: something</a></span></body></html>`
	if _, _, err := getNewsOriginInfo(context.Background(), html); err == nil {
		t.Fatalf("相对链接应该返回 error")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/monitor/ -run TestGetNewsOriginInfo -v`
Expected: 编译失败，`undefined: getNewsOriginInfo`

- [ ] **Step 4: 改造解析函数**

`internal/monitor/hackernews.go`，把 `getNewsOriginURL`（387 行起）整个替换为：

```go
// getNewsOriginInfo 从 HN 帖子页同时提取原文链接和帖子标题。
// 两者来自同一个元素（span.titleline a 的 href 与文本），一次解析取两个值，
// 省掉再解析一遍 HTML 的开销。
//
// 返回 error 表示"没找到 / 格式异常"，调用方据此跳过这条帖子。
func getNewsOriginInfo(ctx context.Context, htmlBody string) (string, string, error) {
	slog.InfoContext(ctx, "开始解析源网址")

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return "", "", err
	}

	// Find 返回的是匹配集合；First 取第一个
	sel := doc.Find("span.titleline a").First()
	if sel.Length() == 0 {
		return "", "", errors.New("span.titleline a 没找到对应内容")
	}
	// Attr 返回"属性值, 是否存在"；ok==false 表示这个属性根本不存在
	href, ok := sel.Attr("href")
	if !ok {
		return "", "", errors.New("未找到源网址")
	}
	// 标题就是这个 <a> 的文本；TrimSpace 去掉 HTML 里的换行和缩进空白。
	postTitle := strings.TrimSpace(sel.Text())

	slog.InfoContext(ctx, "识别到的源网址为", "href", href, "title", postTitle)
	// 兜底：相对路径如 "item?id=..." 不算外链，跳过。
	if !strings.HasPrefix(href, "http") {
		return "", "", errors.New("识别到的源网址格式异常")
	}
	return href, postTitle, nil
}
```

**注意**：保留原函数尾部 `if !strings.HasPrefix(href, "http")` 之后的 `return href, nil` 逻辑，改成上面的三值返回。原函数的注释和 `slog` 调用照搬，不要精简。

- [ ] **Step 5: 更新 HN 的调用点**

`internal/monitor/hackernews.go` 的 `process` 方法（220-229 行）改为：

```go
	originURL, postTitle, err := getNewsOriginInfo(ctx, body)
	if err != nil {
		return nil
	}

	// 这里用 &model.NotifyBase{...} 拿到指针，后面才能给 out.Content 赋值并返回出去。
	out := &model.NotifyBase{
		Source:     "hackernews",
		ExternalID: id, // HN 用帖子数字 id 作为去重键，不是 URL
		URL:        pageURL,
		OriginURL:  originURL,
		PostTitle:  postTitle, // Title 稍后会被 fetch 覆盖成分类抬头，所以标题单独存这里
	}
```

- [ ] **Step 6: 更新 V2EX 的调用点**

`internal/monitor/v2ex.go` 里两处 `model.NotifyBase{...}` 字面量（152-156 行热帖、174-178 行新帖）各加三个字段。热帖那处：

```go
		out := model.NotifyBase{
			Source:     "v2ex",
			ExternalID: topic.URL, // v2ex 用帖子 URL 作为去重键
			Title:      title,
			PostTitle:  title, // v2ex 的抬头就是帖子标题，两者相同
			URL:        topic.URL,
			Content:    fmt.Sprintf("*%s*: [%s](%s)\n", hotTitle, contentTitle, topic.URL),
		}
```

新帖那处同理，只是 `Content` 里用 `newTitle`：

```go
		out := model.NotifyBase{
			Source:     "v2ex",
			ExternalID: topic.URL,
			Title:      title,
			PostTitle:  title,
			URL:        topic.URL,
			Content:    fmt.Sprintf("*%s*: [%s](%s)\n", newTitle, contentTitle, topic.URL),
		}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go test -short ./internal/monitor/ -v && go build ./...`
Expected: 全绿，包括新增的两个 `TestGetNewsOriginInfo*`

- [ ] **Step 8: 提交**

```bash
git add internal/model/model.go internal/monitor/hackernews.go internal/monitor/hackernews_test.go internal/monitor/v2ex.go
git commit -m "#feat NotifyBase 补 Source/ExternalID/PostTitle 并解析 HN 帖子标题

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: 共享的 `deliver` 函数（发送成功才记账）

**Files:**
- Modify: `internal/monitor/monitor.go`
- Test: `internal/monitor/deliver_test.go`（创建）

**Interfaces:**
- Consumes: `store.Store`、`store.Record`（Task 1）；`model.NotifyBase` 的 `Source`/`ExternalID`/`PostTitle`（Task 4）；`notify.Notifier.NotifyMarkdown`（Task 3）
- Produces: `deliver(ctx context.Context, n notify.Notifier, st store.Store, chatID string, items []model.NotifyBase)`（包内私有，V2EX 与 HackerNews 共用）

**背景**：这是整个改动的核心 —— 先发，发成功才记账。V2EX 和 HN 的这段逻辑完全相同，抽成一个函数避免两处重复。

- [ ] **Step 1: 写失败的测试**

创建 `internal/monitor/deliver_test.go`：

```go
package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/store"
)

// stubNotifier 可配置成"永远失败"，用来验证发送失败时不写库。
// 注意不要和 weather_test.go 里的 fakeNotifier 重名 —— 它们在同一个包里。
type stubNotifier struct {
	sent []string
	err  error // 非 nil 时 NotifyMarkdown 一律返回它
}

func (s *stubNotifier) Notify(ctx context.Context, content string) error { return nil }
func (s *stubNotifier) NotifyTo(ctx context.Context, chatID int64, content string) error {
	return nil
}
func (s *stubNotifier) NotifyMarkdown(ctx context.Context, content string) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, content)
	return nil
}

// newTestStore 开一个指向临时目录的真实 SQLite 库（不用 fake，直接测真实行为）。
func newTestStore(t *testing.T) *store.SQLite {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite 失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testItem() model.NotifyBase {
	return model.NotifyBase{
		Source:     "v2ex",
		ExternalID: "https://v2ex.com/t/1",
		Title:      "标题",
		PostTitle:  "标题",
		URL:        "https://v2ex.com/t/1",
		Content:    "*热帖推送*: [标题](https://v2ex.com/t/1)\n",
	}
}

// 发送成功 → 必须留下记录。
func TestDeliverRecordsOnSuccess(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{}
	st := newTestStore(t)

	deliver(ctx, n, st, "-100123", []model.NotifyBase{testItem()})

	if len(n.sent) != 1 {
		t.Fatalf("应发出 1 条消息，实际 %d 条", len(n.sent))
	}
	ok, err := st.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if !ok {
		t.Fatalf("发送成功后应写入记录")
	}
}

// 发送失败 → 不能留下记录，否则在永久去重下这条会永远丢失。
func TestDeliverSkipsRecordOnSendFailure(t *testing.T) {
	ctx := context.Background()
	n := &stubNotifier{err: errors.New("telegram 挂了")}
	st := newTestStore(t)

	deliver(ctx, n, st, "-100123", []model.NotifyBase{testItem()})

	ok, err := st.AlreadyPushed(ctx, "v2ex", "https://v2ex.com/t/1")
	if err != nil {
		t.Fatalf("AlreadyPushed 报错: %v", err)
	}
	if ok {
		t.Fatalf("发送失败时不应写入记录（否则下轮不会重试）")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/monitor/ -run TestDeliver -v`
Expected: 编译失败，`undefined: deliver`

- [ ] **Step 3: 实现 deliver**

`internal/monitor/monitor.go` 追加（并在 import 块补上需要的包）：

```go
// deliver 逐条推送并在**发送成功之后**记账。V2EX 和 HackerNews 共用。
//
// 为什么先发后记：去重是永久的，如果像改造前那样在抓取阶段就记账，
// 一旦 Telegram 发送失败，这条帖子就再也不会被推送了 —— 永久丢失。
// 先发后记把语义从"至多一次"变成"至少一次"：发成功但写库失败（或两者之间崩溃）时，
// 下一轮会重推一次。用一次可能的重复换掉永久丢失，是划算的。
//
// 不返回 error：单条失败属于瞬时问题，就地 log 消化即可，
// 符合 Monitor 接口"瞬时错误内部处理，不要往 Run 的返回值上冒"的契约。
func deliver(ctx context.Context, n notify.Notifier, st store.Store, chatID string, items []model.NotifyBase) {
	for _, item := range items {
		// 发送失败：不写库，下一轮 fetch 时 AlreadyPushed 仍返回 false，自然重试。
		if err := n.NotifyMarkdown(ctx, item.Content); err != nil {
			slog.ErrorContext(ctx, "推送失败，本条不记账",
				"source", item.Source, "external_id", item.ExternalID, "err", err)
			continue
		}
		rec := store.Record{
			Source:     item.Source,
			ExternalID: item.ExternalID,
			Title:      item.PostTitle, // 用帖子真实标题，不是消息抬头
			URL:        item.URL,
			ChatID:     chatID,
			PushedAt:   time.Now(),
		}
		// 写库失败：消息已经发出去了，只能告警。下一轮会重推一次。
		if err := st.MarkPushed(ctx, rec); err != nil {
			slog.ErrorContext(ctx, "推送记录写入失败，下轮会重推",
				"source", item.Source, "external_id", item.ExternalID, "err", err)
		}
	}
}
```

`internal/monitor/monitor.go` 的 import 块需要包含：

```go
import (
	"context"
	"log/slog"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/notify"
	"github.com/cheedonghu/news2tg/internal/store"
)
```

（`config` 是原有 `Monitor` 接口定义需要的，不要删。）

- [ ] **Step 4: 跑测试确认通过**

Run: `go test -short ./internal/monitor/ -run TestDeliver -v`
Expected: PASS，两个测试全绿

- [ ] **Step 5: 提交**

```bash
git add internal/monitor/monitor.go internal/monitor/deliver_test.go
git commit -m "#feat 新增 deliver：Telegram 发送成功后才写推送记录

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: V2EX 接入 store

**Files:**
- Modify: `internal/monitor/v2ex.go`
- Modify: `internal/monitor/v2ex_test.go`（删 `TestCleanOldURLs`，加 `fetch` 集成测试）
- Modify: `cmd/news2tg/main.go:112`（`NewV2EX` 调用点）

**Interfaces:**
- Consumes: `store.Store`（Task 1）、`deliver`（Task 5）
- Produces: `monitor.NewV2EX(httpClient *http.Client, notifier notify.Notifier, st store.Store) *V2EX`

- [ ] **Step 1: 改结构体与构造函数**

`internal/monitor/v2ex.go`：删除 import 里的 `"sync"`（不再需要），结构体改为：

```go
type V2EX struct {
	httpClient *http.Client    // 指针：HTTP 客户端是共享资源（连接池），不要拷贝
	notifier   notify.Notifier // 接口类型：通过依赖注入，测试时可以传 mock
	store      store.Store     // 接口类型：推送记录持久化，实现是 SQLite

	// 端点地址做成字段（默认取上面的常量），只为让测试能指向 httptest 服务器。
	hotURL    string
	latestURL string
}

func NewV2EX(httpClient *http.Client, notifier notify.Notifier, st store.Store) *V2EX {
	return &V2EX{
		httpClient: httpClient,
		notifier:   notifier,
		store:      st,
		hotURL:     v2exHotURL,
		latestURL:  v2exLatestURL,
	}
}
```

import 块加 `"github.com/cheedonghu/news2tg/internal/store"`。

- [ ] **Step 2: 删除去重三件套，改 fetch**

删除 `alreadyPushed`、`markPushed`、`cleanOldURLs` 三个方法（218-250 行）及其上方的注释块。

`fetch` 方法改动：

1. 两处 `m.fetchTopics(ctx, v2exHotURL)` / `v2exLatestURL` 改为 `m.hotURL` / `m.latestURL`。
2. 删除 `currentDate := time.Now().Format("20060102")` 这一行（不再需要）。
3. 在 `var result []model.NotifyBase` 之前加轮内去重集合：

```go
	var result []model.NotifyBase // nil 切片，append 会按需创建底层数组

	// 轮内去重：同一个 URL 可能同时出现在热帖和新帖列表里。
	// 改造前靠"抓取时立即写库"挡住，现在写库后移到发送成功之后，需要这个局部集合顶上。
	//
	// 不加锁是安全的：fetch 全程在单个 goroutine 里顺序执行（上面两个 fetchTopics
	// 是串行调用，没有 go 关键字）。若将来改成并行抓取，这里必须加锁。
	seen := make(map[string]bool)
```

4. 两个循环里，把 `if m.alreadyPushed(topic.URL) { continue }` 换成：

```go
		if seen[topic.URL] {
			continue // 本轮已经收过这条了
		}
		// AlreadyPushed 读失败时倾向"少推"：宁可漏几条，也不要在 DB 抖动时
		// 把整页热帖重发一遍炸频道。
		pushed, err := m.store.AlreadyPushed(ctx, "v2ex", topic.URL)
		if err != nil {
			slog.ErrorContext(ctx, "查询推送记录失败，跳过本条", "url", topic.URL, "err", err)
			continue
		}
		if pushed {
			continue
		}
```

5. 两个循环里，把 `m.markPushed(topic.URL, currentDate)` 换成 `seen[topic.URL] = true`。

**注意热帖循环里的顺序**：`seen` 判断放在最前面；新帖循环里 `filterNewTopic` 的位置不变（仍在标题截断之后）。

- [ ] **Step 3: 改 Run**

`internal/monitor/v2ex.go` 的 `Run`，把 93-107 行（`if len(results) > 0 {...}` 到 `m.cleanOldURLs(time.Now())`）整段替换为：

```go
		if len(results) > 0 {
			// 逐条发送，发成功才记账。共用的实现见 monitor.go 的 deliver。
			deliver(cctx, m.notifier, m.store, cfg.Telegram.ChatID, results)
		}
```

删除 `cleanOldURLs` 的调用。此时 `time` 包仍被 `time.NewTicker` 用到，不要删 import。

- [ ] **Step 4: 更新 main 接线**

`cmd/news2tg/main.go:112` 改为：

```go
	v2exMon := monitor.NewV2EX(httpClient, tgClient, pushStore)
```

- [ ] **Step 5: 删除过时测试，写新测试**

`internal/monitor/v2ex_test.go`：删除整个 `TestCleanOldURLs`（118-147 行）—— 它测的滑窗清理行为已不存在。若删完后 `time` import 变成未使用，一并删掉。

在文件末尾追加 `fetch` 的集成测试：

```go
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

	st := newTestStore(t) // 复用 deliver_test.go 里的辅助函数（同一个包）
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
```

`v2ex_test.go` 的 import 块需要包含：

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/model"
	"github.com/cheedonghu/news2tg/internal/store"
)
```

- [ ] **Step 6: 跑测试**

Run: `go build ./... && go test -short ./internal/monitor/ -v`
Expected: 全绿。`TestFilterNewTopic` 仍在，`TestCleanOldURLs` 已消失，`TestFetchDedup` 通过。

- [ ] **Step 7: 提交**

```bash
git add internal/monitor/v2ex.go internal/monitor/v2ex_test.go cmd/news2tg/main.go
git commit -m "#feat V2EX 去重改走 store 并移除滑窗清理

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: HackerNews 接入 store

**Files:**
- Modify: `internal/monitor/hackernews.go`
- Modify: `cmd/news2tg/main.go:111`（`NewHackerNews` 调用点）

**Interfaces:**
- Consumes: `store.Store`（Task 1）、`deliver`（Task 5）
- Produces: `monitor.NewHackerNews(httpClient *http.Client, notifier notify.Notifier, aiHelper ai.Helper, digestFetcher digest.Fetcher, st store.Store) *HackerNews`

- [ ] **Step 1: 改结构体与构造函数**

`internal/monitor/hackernews.go`：删除 import 里的 `"sync"`，结构体改为：

```go
type HackerNews struct {
	httpClient *http.Client
	notifier   notify.Notifier
	ai         ai.Helper      // 接口类型，运行时是 *ai.DeepSeek 的实例
	digest     digest.Fetcher // 接口类型，运行时是 *digest.Python 的实例
	store      store.Store    // 接口类型：推送记录持久化，实现是 SQLite
}

func NewHackerNews(httpClient *http.Client, notifier notify.Notifier, aiHelper ai.Helper, digestFetcher digest.Fetcher, st store.Store) *HackerNews {
	return &HackerNews{
		httpClient: httpClient,
		notifier:   notifier,
		ai:         aiHelper,
		digest:     digestFetcher,
		store:      st,
	}
}
```

import 块加 `"github.com/cheedonghu/news2tg/internal/store"`。

- [ ] **Step 2: 删除去重三件套**

删除 `alreadyPushed`、`markPushed`、`cleanOldURLs` 三个方法（308-334 行）及上方的注释行。

- [ ] **Step 3: 改 process**

`process` 方法签名不变，但去重判断改为由调用方（`fetch`）负责 —— 因为轮内 `seen` 集合在 `fetch` 里。删除方法开头的：

```go
	if m.alreadyPushed(id) {
		slog.InfoContext(ctx, "消息已经推送过", "id", id)
		return nil
	}
```

以及方法末尾的 `m.markPushed(id)` 那一行（`return out` 之前）。

- [ ] **Step 4: 改 fetch**

`internal/monitor/hackernews.go` 的 `fetch`，在 `var result []model.NotifyBase` 之前加：

```go
	// 轮内去重：同一个 id 可能同时出现在 top 和 new 两个列表里。
	// 与 v2ex 同理，不加锁是安全的 —— fetch 全程单 goroutine 顺序执行。
	seen := make(map[string]bool)
```

两个 `for _, id := range ...` 循环体改为（热帖用 `hotTitle`，新帖用 `newTitle`）：

```go
	for _, id := range hotIDs {
		if seen[id] {
			continue // 本轮已经收过这条了
		}
		// 读失败时倾向"少推"：宁可漏几条，也不要在 DB 抖动时重发一整页。
		pushed, err := m.store.AlreadyPushed(ctx, "hackernews", id)
		if err != nil {
			slog.ErrorContext(ctx, "查询推送记录失败，跳过本条", "id", id, "err", err)
			continue
		}
		if pushed {
			slog.InfoContext(ctx, "消息已经推送过", "id", id)
			continue
		}
		// process 返回 *model.NotifyBase（指针）；nil 表示"跳过这条"。
		if out := m.process(ctx, id, cfg.Features.HnFetchTimeGap); out != nil {
			out.Title = hotTitle          // 通过指针修改原值，不需要再赋回去
			result = append(result, *out) // *out 是"解引用"，把指针指向的结构体拷贝进切片
			seen[id] = true
		}
	}
```

新帖循环同构，只把 `hotIDs` 换成 `newIDs`、`hotTitle` 换成 `newTitle`。

- [ ] **Step 5: 改 Run**

`internal/monitor/hackernews.go` 的 `Run`，把 80-96 行（`if len(results) > 0 {...}` 到 `m.cleanOldURLs(time.Now())`）整段替换为：

```go
		if len(results) > 0 {
			// AI 摘要单独一步，方便日后加开关时只摘掉这一段。
			processed, err := m.aiTransfer(cctx, results)
			if err != nil {
				slog.ErrorContext(cctx, "HN ai_transfer failed", "err", err)
				continue // continue：跳到下一轮 for 循环
			}
			// 逐条发送，发成功才记账。共用的实现见 monitor.go 的 deliver。
			deliver(cctx, m.notifier, m.store, cfg.Telegram.ChatID, processed)
		}
```

**注意**：`aiTransfer` 返回的是 `[]model.NotifyBase` 的新切片，`Source` / `ExternalID` / `PostTitle` 三个字段会随值拷贝一起传递（`aiTransfer` 只改 `Content`），所以 `deliver` 拿得到它们。

删除 `cleanOldURLs` 的调用。检查 `time` import 是否还被使用（`time.NewTicker` 仍在用，保留）。

- [ ] **Step 6: 更新 main 接线**

`cmd/news2tg/main.go:111` 改为：

```go
	hnMon := monitor.NewHackerNews(httpClient, tgClient, aiClient, digestFetcher, pushStore)
```

- [ ] **Step 7: 验证**

Run: `go build ./... && go test -short ./... && go vet ./...`
Expected: 全部通过

Run: `gofmt -l internal/store internal/monitor internal/notify internal/model internal/config cmd/news2tg`
Expected: 无输出（如果有输出，对列出的文件跑 `gofmt -w <file>`）

- [ ] **Step 8: 提交**

```bash
git add internal/monitor/hackernews.go cmd/news2tg/main.go
git commit -m "#feat HackerNews 去重改走 store 并移除滑窗清理

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 8: 文档同步

**Files:**
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: 前 7 个任务的全部产出
- Produces: 无代码产出

- [ ] **Step 1: 改 Dedup 一节**

`CLAUDE.md` 的 `### Dedup` 整段替换为：

```markdown
### Dedup

推送记录存在 SQLite 文件里（路径由 `[storage] db_path` 指定），表 `pushed_posts`，
`(source, external_id)` 联合唯一。去重是**永久**的：推过一次的帖子永不重推，
记录也永久保留（不再有滑窗清理）。库文件打不开时进程直接退出，不会降级成内存去重。

写入时机是 **Telegram 发送成功之后**（`monitor.deliver`），语义为"至少一次"：
发送成功但写库失败、或两者之间崩溃，下一轮会重推一次。这是刻意选择 ——
改造前在抓取阶段就记账，发送失败会导致条目在永久去重下永久丢失。

`fetch` 内另有一个局部 `seen` 集合做轮内去重（同一条可能同时出现在热帖和新帖列表里），
它依赖 `fetch` 单 goroutine 顺序执行，改成并行抓取时必须加锁。
```

- [ ] **Step 2: 改 Notifier 那段**

找到 `**`notify.Notifier`** (`internal/notify/notify.go`)` 开头的条目，整条替换为：

```markdown
- **`notify.Notifier`** (`internal/notify/notify.go`) — `Notify`（默认 chat）/ `NotifyTo`（任意 chat，如回复指令发送者）/ `NotifyMarkdown`（默认 chat，发**已渲染好的 MarkdownV2**）。实现：`Telegram`。**转义约定：** `Notify`/`NotifyTo` 把 `content` 当纯文本并在内部 `EscapeMarkdownV2`，调用方不要预转义；`NotifyMarkdown` 发预渲染 MarkdownV2（HN/V2EX/weather 自行拼 `*bold*`/`[link]` 并只转义动态片段），实现不做整体转义。**限速在 `Telegram` 客户端内部**（任意两次发送间隔 ≥1.5s，`select` 等 ctx 可取消），对所有发送路径生效，调用方直接循环调用即可。
```

- [ ] **Step 3: 加 store 到接口清单**

在 `**`digest.Fetcher`**` 那条之后插入：

```markdown
- **`store.Store`** (`internal/store/store.go`) — `AlreadyPushed(ctx, source, externalID)` / `MarkPushed(ctx, rec)` / `Close()`，推送记录持久化。实现：`SQLite`（`sqlite.go`，`modernc.org/sqlite` 纯 Go 驱动，无 cgo，配合 `CGO_ENABLED=0` 构建）。注入 `V2EX` 与 `HackerNews`。
```

- [ ] **Step 4: 改 Config 一节**

在 `### Config` 段落里，`[jina]` 的说明之后补一句：

```markdown
`[storage]`（`db_path`，推送记录 SQLite 文件路径，**必填** —— `FromFile` 拒绝空值，
容器里对应 `docker-compose.yml` 挂载的 `./data:/data` 卷）。
```

- [ ] **Step 5: 改单元测试清单**

`## Commands` 一节里列举测试文件的那句，把 `internal/monitor/v2ex_test.go`（描述里提到锁 OR 过滤行为）保留，并补上新增文件：

```markdown
`internal/store/sqlite_test.go`（对真实临时库文件）、`internal/monitor/deliver_test.go`（发送成功才记账）、`internal/monitor/hackernews_test.go`（HN 帖子标题解析）
```

同时把 `go test ./...` 改成 `go test -short ./...` 并说明原因（`internal/agent` 的 e2e 测试会真实调 API 产生费用）。

- [ ] **Step 6: 提交**

```bash
git add CLAUDE.md
git commit -m "#docs 同步推送记录 SQLite 化后的架构说明

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## 最终验收

- [ ] `go build ./...` 通过
- [ ] `go test -short ./...` 全绿
- [ ] `go vet ./...` 无告警
- [ ] `gofmt -l` 对本次改动的文件无输出
- [ ] 用本地配置跑一次：`go run ./cmd/news2tg -c myconfig.toml`，确认日志里有 `push record store opened`
- [ ] `sqlite3 ./target/dev.db "select source, external_id, title, pushed_at from pushed_posts limit 5;"` 能查到推送记录
- [ ] 故意把 `db_path` 指到一个不可写的路径（如 `/nonexistent/x.db`），确认进程启动失败并打印清晰错误
- [ ] 确认 `docker-compose.yml` 已加 `./data:/data`，部署时宿主机 `./data` 目录存在
