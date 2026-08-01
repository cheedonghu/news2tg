# 推送记录改用 SQLite 持久化 · 设计

日期：2026-08-01
状态：待评审

## 目标

把 V2EX / Hacker News 两个 monitor 的推送去重记录，从进程内的 `map[string]string` 迁移到
嵌入式 SQLite 数据库文件，使记录在重启后保留，并可用 `sqlite3` 命令行直接查询历史。

## 范围与非目标

- **做**：`internal/store` 新包（接口 + SQLite 实现）、V2EX/HackerNews 去重改走 store、
  永久去重（不再有滑窗清理）、`MarkPushed` 改到发送成功之后、`Notifier` 接口调整（逐条发送 +
  客户端级限速）、`model.NotifyBase` 补 `Source` / `ExternalID` 字段、新增 `[storage]` 配置段、
  docker-compose 新增数据卷。
- **不做**：归档推送正文 / AI 摘要原文（只存基础溯源字段）、运行统计与失败留痕表、
  weather 与 `/summary` 的推送流水（它们不需要去重，历史在 Telegram 频道里已完整存在）、
  多实例部署支持、HN 重试时的正文/摘要缓存。

## 关键决策与理由

| 决策 | 选择 | 理由 |
|---|---|---|
| 存储引擎 | `modernc.org/sqlite` | `Dockerfile` 是 `CGO_ENABLED=0`，排除 `mattn/go-sqlite3`（cgo）。相比 bbolt，SQL 让"查最近一周推了什么"无需写 Go 代码 |
| 记录字段 | 仅基础溯源 | 不存正文/摘要，表小、长期留存无压力 |
| 去重窗口 | 永久 | 推过即永不重推；滑窗清理整体移除 |
| 记录保留 | 永久 | 单行约 200 字节，按每天新增 500 条计一年约 35MB |
| 库文件位置 | 独立卷 `/data` | 配置只读、数据可写，职责分离 |
| 记录范围 | 仅 V2EX + HN | 只有这两个源需要去重 |
| 写入时机 | 发送成功之后 | 避免 Telegram 发送失败导致条目在永久去重下永久丢失 |

## 包结构

新增 `internal/store`，遵循仓库既有的接口注入风格（同 `notify.Notifier` / `digest.Fetcher` / `ai.Helper`）：

```
internal/store/
  store.go        // Store 接口 + Record DTO —— monitor 只依赖这里
  sqlite.go       // SQLite 实现：Open / 建表 / 查询与写入
  sqlite_test.go
```

```go
type Record struct {
    Source     string    // "v2ex" | "hackernews"
    ExternalID string    // v2ex 用帖子 URL，hn 用帖子数字 id
    Title      string
    URL        string
    ChatID     string
    PushedAt   time.Time
}

type Store interface {
    AlreadyPushed(ctx context.Context, source, externalID string) (bool, error)
    MarkPushed(ctx context.Context, rec Record) error
    Close() error
}
```

接口刻意做窄，只有当前真正用到的动作。**没有 `CleanOld`** —— 永久去重，滑窗清理不复存在。

`V2EX` / `HackerNews` 结构体删除 `mu sync.RWMutex` 与 `pushedURLs map[string]string` 两个字段，
换成 `store store.Store`；构造函数各加一个参数，由 `main` 注入。并发安全从 `RWMutex` 下沉给
SQLite 自身（WAL + busy_timeout）。

这不是新增耦合：monitor 原本就自带一套存储实现，改为声明"我需要一个 Store"后耦合度下降，
换实现时 monitor 一行不改。

## 表结构

启动时执行，即全部的"迁移"：

```sql
CREATE TABLE IF NOT EXISTS pushed_posts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source      TEXT NOT NULL,              -- 'v2ex' | 'hackernews'
    external_id TEXT NOT NULL,              -- v2ex: 帖子 URL；hn: 帖子数字 id
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    pushed_at   TEXT NOT NULL,              -- RFC3339，带时区
    UNIQUE(source, external_id)
);
CREATE INDEX IF NOT EXISTS idx_pushed_at ON pushed_posts(pushed_at);
```

- **`(source, external_id)` 联合唯一**，而非每源一张表。两源 id 空间不同（URL vs 数字），
  加 source 前缀即可区分；单表让"最近推了什么"是一条 SQL 而非 union。该唯一索引同时充当
  去重查询的索引，无需另建。
- **`pushed_at` 存 RFC3339 而非原来的 `yyyymmdd`**。原格式是为了让字典序等于时间序以便比大小；
  永久去重后不再比时间，该约束消失。RFC3339 字典序同样等于时间序，且能直接做区间查询。
- **写入用 `INSERT ... ON CONFLICT(source, external_id) DO NOTHING`**，使重复写入幂等，
  避免"发送成功但写库失败、下轮重推"的路径因唯一约束冲突刷 ERROR。
- 打开时设 `PRAGMA journal_mode=WAL` 与 `busy_timeout`：多个 monitor goroutine 并发访问同一库，
  WAL 让读不阻塞写，busy_timeout 避免偶发 `database is locked` 直接失败。

## Notifier 接口调整

要做到"发成功才记账"，需要逐条的成功信号。该信号在 `telegram.go:67` 本就存在，只是被丢弃
（单条失败仅 log 并继续）。方案是取消"批量"这层假象，而非让 `NotifyBatch` 返回 `[]error`
——后者要求调用方靠下标对齐 `contents` / `errors` / `records` 三个切片，易在后续修改中错位。

```go
type Notifier interface {
    Notify(ctx, content string) error                  // 纯文本，内部转义，默认 chat
    NotifyTo(ctx, chatID int64, content string) error  // 纯文本，内部转义，指定 chat
    NotifyMarkdown(ctx, content string) error          // 已渲染 MarkdownV2，不转义，默认 chat
}
```

`NotifyBatch` 删除，其两个职责各归其位：

- **逐条发送**归调用方 —— 只有调用方知道发成功之后要做什么。
- **限速**归 `Telegram` 实现 —— 结构体加 `mu sync.Mutex` + `lastSend time.Time`，
  任意发送前先等够距上次 1.5s，等待用 `select` 监听 `ctx.Done()`，保持现有可取消语义。

限速下沉后比现状更正确：现在它只保护"批量"路径，weather 的推送与 `/summary` 的回复都绕过了它。
代价是全局串行——HN 推 20 条约占 30 秒，期间 `/summary` 回复会排队。考虑到 Telegram 本身有
每分钟群组限额，这是期望行为。

调用方改动：`weather.go:225` 的 `NotifyBatch(ctx, []string{msg})` 是发单条预渲染消息，
1:1 映射为 `NotifyMarkdown(ctx, msg)`。`weather.go:264-267` 依赖"ctx 取消时返回 `ctx.Err()`"
的判断仍然成立。

## model.NotifyBase 扩展

现有 `model.NotifyBase`（`model.go:10-16`）只有 `URL` / `OriginURL` / `Title` / `Content` /
`ContentTransferedByAIFlag`，**不承载帖子 id**：HN 的数字 id 在 `process(ctx, id, ...)` 里
用完即丢（当场 `markPushed(id)`），返回值中没有它。写入后移到发送成功之后后，`Run` 需要这个
id 才能构造 `Record`，故补两个字段：

```go
type NotifyBase struct {
    Source     string // "v2ex" | "hackernews"，由各 monitor 的 fetch 填
    ExternalID string // v2ex 填帖子 URL，hn 填帖子数字 id
    // ...现有字段不变
}
```

`Record` 的其余字段来源：`Title` / `URL` 取自同一个 `NotifyBase`；`ChatID` 取自
`cfg.Telegram.ChatID`（`Run` 的入参 `cfg` 中已有，本就是字符串，无需转换）；
`PushedAt` 取发送成功那一刻的 `time.Now()`。

## 数据流

以 V2EX 为例，HN 同构：

```
fetch()  只产出候选，不再写库
   └─ 逐条：store.AlreadyPushed → 过滤器 filterNewTopic → 轮内 seen 去重 → 拼消息

Run:
   for _, item := range results {
       if err := notifier.NotifyMarkdown(cctx, item.Content); err != nil {
           slog.ErrorContext(...)   // 不写库 → 下一轮自然重试
           continue
       }
       if err := store.MarkPushed(cctx, rec); err != nil {
           slog.ErrorContext(...)   // 已发出，只能告警；下轮会重推一次
       }
   }
```

现状对比：`markPushed` 目前发生在 `fetch` 内（`v2ex.go:158,180`）与 `process` 内
（`hackernews.go:243`），都早于发送。在永久去重下，发送失败将导致条目**永久丢失**，故必须后移。

**轮内 seen 集合**：原先同一 URL 同时出现在热帖与新帖列表时，靠 `markPushed` 在 `fetch` 中
即时写入挡住；`fetch` 不再写库后该保护消失，需在 `fetch` 内部用局部 `map[string]bool` 顶上。
该 map 依赖 `fetch` 单 goroutine 执行（`v2ex.go:117-132` 与 `hackernews.go:134-148` 均为顺序
执行，无 `go` 关键字），实现时须加注释写明此约束。

**语义变化**：从"至多一次"变为"至少一次"。发送成功但写库失败、或两者之间进程崩溃，
下一轮会重推一次。用一次可能的重复换掉永久丢失，是划算的。

## 错误处理

1. **打开 DB 失败 → 启动失败退出**（`slog.Error` + `os.Exit(1)`）。与 `[deepseek] model`
   缺配置即拒绝启动同一哲学：去重是核心功能，静默降级成内存 map 会让人在不知情下刷屏。
   该策略同时覆盖"忘了加 `./data:/data` 挂载"的场景——第一次就失败，而非跑几天后才发现没存。
2. **`AlreadyPushed` 读失败 → log ERROR，当作"已推过"跳过该条**。方向偏向少推而非多推：
   DB 抖动时宁可漏几条，也不要把整页热帖重发炸频道。
3. **`MarkPushed` 写失败 → log ERROR，不阻断本轮推送**。延续 CLAUDE.md"AI/digest 失败不阻断
   推送"的既有约定。条目下轮会被重推一次。

第 2、3 条意味着 monitor 内部的 `alreadyPushed` / `markPushed` 方法签名要变（原来无 error）。
错误在 monitor 层就地消化，不往 `Run` 的返回值上冒 —— 符合 `monitor.Monitor` 的既有契约
（瞬时错误内部处理，不要返回）。

## 配置

新增一段，字段必填、缺失即启动失败，做法同 `config.go:112-117` 对模型名的校验：

```toml
[storage]
db_path = "/data/news2tg.db"
```

Go 侧不留兜底默认值："库放哪"只有配置文件一个答案。本地开发在 `myconfig.toml` 里指到
`./target/dev.db`（`target/` 已在 ignore 中）。

**`config.toml` 与 `myconfig.toml` 两个文件都要改**，否则本地运行会直接启动失败。

## 部署

`docker-compose.yml` 新增卷：

```yaml
volumes:
  - ./config:/config
  - ./data:/data        # 新增
```

`Dockerfile` 的 `RUN mkdir /config` 改为同时创建 `/data`（卷挂载时宿主目录会覆盖，
但未挂载的场景下需要该目录存在）。

**首次上线行为**：新库为空，第一轮会把当前榜单上的帖子全推一遍（V2EX 热帖 + 新帖，
HN 各 `hn_fetch_num` 条）。这与今日重启后的表现一致，不算回退；且因永久去重，
此后不再发生。旧的内存状态无从导入——它本就只存在于进程内。

## 测试

1. **`internal/store/sqlite_test.go`** —— 对着 `t.TempDir()` 中的真实库文件测，不 mock。
   `modernc.org/sqlite` 是纯 Go 的，CI 与 Windows 上均可运行。覆盖：未推过返回 false →
   `MarkPushed` → 返回 true；同一条重复 `MarkPushed` 幂等不报错；不同 source 下相同
   external_id 互不影响。
2. **V2EX `fetch()` 集成测试** —— 用 `httptest.Server` 提供固定的热帖/新帖 JSON，
   配一个指向 `t.TempDir()` 的**真实** SQLite store（不用 fake）。验证：库中已有的 URL
   不再产出；同一 URL 同时出现在热帖与新帖列表时只产出一次。V2EX 的 `fetch` 只调 V2EX 自身
   HTTP API，不涉及 Python sidecar。
3. **修改既有测试** —— 删除 `v2ex_test.go` 的 `TestCleanOldURLs`（其所测行为已不存在）；
   `weather_test.go:148-151` 的 `fakeNotifier` 把 `NotifyBatch` 换成 `NotifyMarkdown`，
   捕获字段从 `batch []string` 改为单值。`TestFilterNewTopic` 不受影响。

验收注意：用 `go test -short ./...`（agent 包的 e2e 测试会真实调用 DeepSeek API 产生费用）；
**不要**用 `gofmt -l .` 当门禁（本仓库因 CRLF 天然列出二十余个文件），只检查本次新增/修改的文件。

## 文档更新

`CLAUDE.md` 有三处会失效，必须同步修改：

- **Dedup 一节**整段重写——"in-memory map / 重启丢失 / 有意妥协"全部不再成立。
- **`notify.Notifier` 段的转义约定**：`NotifyBatch` 不再存在，替换为 `NotifyMarkdown`，
  并写明限速已下沉到 `Telegram` 客户端、对所有发送路径生效。
- **Config 一节**补充 `[storage] db_path` 及其必填语义。

同时在 Architecture 的接口清单中加入 `store.Store`。

## 风险与已知取舍

- **`modernc.org/sqlite` 可能要求 Go ≥ 1.23**，而当前 `go.mod` 为 `go 1.22`。撞上则把 `go.mod`
  提到 1.23（本项目无其它依赖卡在 1.22），而非锁一个兼容 1.22 的旧版本。
- **依赖体积**：`modernc.org/sqlite` 依赖树较大，二进制会增大数 MB，首次构建变慢。
- **`fetch` 内的 `seen` map 依赖单 goroutine 执行**。若将来把热帖/新帖改为并行抓取会引入数据
  竞争；代码中加注释说明该约束，`go test -race` 可兜底但需测试覆盖到该路径。
- **HN 发送失败的条目下轮重试会重复消耗 digest 抓取与 DeepSeek 调用**，有真实费用。
  本次不优化（要做需引入正文/摘要缓存，属另一件事）。
- **`AlreadyPushed` 读失败跳过分支无单测覆盖**：注入错误需要 fake store，为一条
  log-and-continue 降级路径引入 fake 不值当，靠 code review 兜。（轮内 `seen` 去重
  已由测试 2 覆盖。）
- **`chat_id` 字段在当前单频道配置下每行相同**，属冗余；保留是为将来多频道场景免于改表。
- **永久去重的后果**：V2EX 热榜上反复上榜的老帖、HN 长期挂首页的帖子，推过一次后永远不再出现。
  这是有意选择的语义。
