# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go service that scrapes V2EX and Hacker News, then pushes posts to a Telegram bot/channel. HN posts additionally get a Chinese AI summary via DeepSeek. Ported from an earlier Rust version (see "legacy parity" notes below).

## Commands

```bash
go mod tidy                                   # sync deps
go build -o bin/news2tg ./cmd/news2tg # build (binary name = directory name)
./bin/news2tg -c config.toml              # run; -c / --config selects the TOML file

go test -short ./...                          # all tests, skipping live-API e2e (see note below)
go test ./internal/monitor                    # one package
go test ./internal/monitor -run TestFilterNewTopic -v  # single test
go vet ./...
gofmt -l .                                    # list unformatted files
```

`-short` matters: `internal/agent/agent_test.go` has an e2e test that, unless skipped, makes a real call to the DeepSeek API using the key in `myconfig.toml` and costs real money. Plain `go test ./...` will hit it.

Unit tests focus on pure/decision logic (no live bot or network): `internal/monitor/v2ex_test.go` (locks the OR filter behavior; also `TestFetchDedup`, an httptest + real temp SQLite file test of push dedup), `internal/monitor/deliver_test.go` (only marks a post pushed after the send succeeds), `internal/monitor/hackernews_test.go` (HN post title parsing), `internal/store/sqlite_test.go` (against a real temp DB file), `internal/command/bot_test.go` (`classify`), `internal/agent/agent_test.go`, `internal/logx/logx_test.go`, `internal/tools/markdown_test.go`, `internal/digest/jina_test.go`.

Requires Go ≥ 1.25 (`go.mod` pins `go 1.25.0`, pulled up by `modernc.org/sqlite`'s toolchain requirement; relies on the Go 1.22 loop-variable semantics, though some code still defensively shadows — see `cmd/news2tg/main.go:183`).

## External runtime dependency

HN body extraction is **not** done in Go. It goes through the `digest.Fetcher` interface; the current implementation (`digest.Python`, `internal/digest/python.go`) calls a separate Python sidecar over HTTP at `http://127.0.0.1:50051/digest` ([hacker-news-digest](https://github.com/cheedonghu/hacker-news-digest)). Without it running, HN posts fall back to a "网页摘要获取失败" placeholder and skip the AI step. The `Dockerfile` clones and runs both the sidecar and this binary in one container; `docker-compose.yml` is the intended deployment.

## Architecture

`main.go` wires concrete implementations together, then runs each monitor as a goroutine under a single signal-cancellable `context.Context`. If any monitor returns a non-context error, `main` calls `cancel()` to bring all of them down together (`cmd/news2tg/main.go:193`).

The design is interface-based dependency injection — business code (`monitor`) depends only on interfaces, so swapping channels/providers means adding an implementation, not editing callers:

- **`monitor.Monitor`** (`internal/monitor/monitor.go`) — `Run(ctx, cfg) error`, blocks until ctx cancelled. Implementations: `V2EX`, `HackerNews`, and `command.Bot` (structurally — it isn't in the `monitor` package but satisfies the interface). Contract: return `ctx.Err()` for normal shutdown; handle transient errors internally (log + continue) rather than returning them.
- **`notify.Notifier`** (`internal/notify/notify.go`) — `Notify` (default chat) / `NotifyTo` (arbitrary chat, e.g. replying to a command sender) / `NotifyMarkdown` (default chat, sends **pre-rendered MarkdownV2**). Implementation: `Telegram`. **Escaping convention:** `Notify`/`NotifyTo` treat `content` as plain text and `EscapeMarkdownV2` it internally — callers must NOT pre-escape. `NotifyMarkdown` sends pre-rendered MarkdownV2 (HN/V2EX/weather craft `*bold*`/`[link]` markup and escape only the dynamic substrings themselves), so the implementation does not escape it. **Rate limiting lives inside the `Telegram` client itself** (a `mu`/`lastSend` pair enforcing a ≥1.5s gap between any two sends, `select`-ing on ctx so it's cancellable) — it applies to every send path (HN/V2EX pushes, weather, `/summary` replies alike), not just a batch helper. The `Telegram` bot client also carries a 30s HTTP timeout so a stuck connection can't freeze that shared lock forever.
- **`ai.Helper`** (`internal/ai/ai.go`) — `Summarize`. Implementation: `DeepSeek` (`internal/ai/deepseek.go`, uses `go-openai` SDK pointed at `api.deepseek.com`, since DeepSeek is OpenAI-protocol compatible).
- **`digest.Fetcher`** (`internal/digest/digest.go`) — `Fetch(ctx, originURL)` for web body extraction. Implementations: `Python` (`python.go`, the Python sidecar over HTTP — preferred) and `Jina` (`jina.go`, `r.jina.ai` reader — fallback, optional `[jina]` api key). Injected into `HackerNews` via `NewHackerNews`.
- **`store.Store`** (`internal/store/store.go`) — `AlreadyPushed(ctx, source, externalID)` / `MarkPushed(ctx, rec)` / `Close()`, push-record persistence. Implementation: `SQLite` (`sqlite.go`, `modernc.org/sqlite` pure-Go driver, no cgo, pairs with `CGO_ENABLED=0` builds). Injected into both `V2EX` and `HackerNews`.
- **`agent`** (`internal/agent/agent.go`) — a URL→Chinese-summary LLM agent that uses DeepSeek **function calling** to pick between the two `digest.Fetcher`s (python preferred; jina on failure/poor content), then summarizes. Reuses the `cfg.DeepSeek.APIToken`; the model comes from `cfg.DeepSeek.AgentModel` (must be tool-calling capable — distinct from `ai.DeepSeek`'s `cfg.DeepSeek.Model`, which only needs plain text generation). No model name is hardcoded in Go. The LLM call is behind a small `chatCompleter` interface for testability. Wired into `main` and driven by the command bot. Appends a `本次消耗 tokens: N` footer (accumulated `usage.TotalTokens`) and slog-dumps the full `messages` transcript for audit before returning.
- **`command.Bot`** (`internal/command/bot.go`) — the **receive** side of Telegram (the rest of the app only sends). Long-polls `getUpdates`; on `/summary <url>` from a whitelisted admin (`[telegram] admin_ids`) it calls the `agent` and pushes the summary to the configured channel via `notify.Notifier`, acking the requester. Holds its own `*tgbotapi.BotAPI` (the notify client never polls, so no getUpdates conflict). Decision logic is in the pure `classify` method (unit-tested without a live bot); depends on a local `Summarizer` interface, not the `agent` package directly.
- **`model`** (`internal/model/model.go`) — shared DTOs (`NotifyBase`, V2EX `Topic`/`Node`/`Member`), separated into its own package to avoid import cycles.
- **`tools`** (`internal/tools/markdown.go`) — `EscapeMarkdownV2` (called inside `notify.Notify`/`NotifyTo` for plain-text sends, and by the monitors' markdown builders for the dynamic parts of `NotifyMarkdown` messages) and `TruncateUTF8` (rune-aware truncation; never slice strings by byte).

Monitors share a single `*http.Client` (one connection pool) created in `main`. They poll on a `time.Ticker` (V2EX every 2 min, HN every 5 min) and `select` on `ctx.Done()` vs `ticker.C`.

### Dedup

推送记录存在 SQLite 文件里（路径由 `[storage] db_path` 指定），表 `pushed_posts`，
`(source, external_id)` 联合唯一。去重是**永久**的：推过一次的帖子永不重推，
记录也永久保留（不再有滑窗清理）。库文件打不开时进程直接退出，不会降级成内存去重。

写入时机是 **Telegram 发送成功之后**（`monitor.deliver`），语义为"至少一次"：
发送成功但写库失败、或两者之间崩溃，下一轮会重推一次。这是刻意选择 ——
改造前在抓取阶段就记账，发送失败会导致条目在永久去重下永久丢失。

`fetch` 内另有一个局部 `seen` 集合做轮内去重（同一条可能同时出现在热帖和新帖列表里），
它依赖 `fetch` 单 goroutine 顺序执行，改成并行抓取时必须加锁。

### Config

TOML (`internal/config/config.go`), mapped via struct tags. `[telegram]` (incl. `admin_ids` — the `/summary` command whitelist), `[features]` (per-source on/off switches + V2EX keyword/node filters + HN count/time-gap), `[deepseek]` (api key + `model` for `Summarize`/`Advise`, `agent_model` for the tool-calling agent — both **required**; `FromFile` rejects empty values so startup fails fast, and no model name is hardcoded in Go), `[jina]` (api key for the `agent`'s jina fallback fetcher). `[storage]`（`db_path`，推送记录 SQLite 文件路径，**必填** —— `FromFile` 拒绝空值，容器里对应 `docker-compose.yml` 挂载的 `./data:/data` 卷）。`chat_id` and each `admin_ids` entry are stored as strings and `ParseInt`'d in `main` to avoid TOML int issues. `config.toml` is the template; `myconfig.toml` is a local (gitignored-style) variant.

## Conventions specific to this repo

- **Bilingual teaching comments.** Almost every line carries a Chinese comment explaining Go semantics (the original author was learning Go). When editing, match this density and style rather than stripping comments — they are the house style here, not noise.
- **AI/digest failures never abort a push.** `DeepSeek.Summarize` and the digest fetch return fallback strings (`"大模型返回异常"`, etc.) with `nil` error on purpose, so the post still goes out. Preserve this behavior unless explicitly changing it.
- **Legacy parity.** `formatHNMessage` (`internal/monitor/hackernews.go:302`) reproduces the old Rust output format byte-for-byte (spacing, newlines). `judgeNewsDate` only accepts the `"N hours ago"` form, matching Rust. Don't "clean up" these formats casually.
- **Known intentional quirk:** `filterNewTopic` combines keyword/node filters with **OR**, even though `config.toml` comments say "AND". A test in `internal/monitor/v2ex_test.go` locks the current OR behavior — changing it will (correctly) turn that test red.
- Logging is mixed: structured `slog` (JSON to stderr) in most places, but some flows still use `fmt.Printf` to stdout. Prefer `slog` for new code.
- **Task correlation (`task_id`):** `internal/logx` wraps the JSON handler (installed in `main.init`) and auto-injects a `task_id` attribute pulled from `context`. Each logical task tags its ctx once at entry via `logx.WithTaskID(ctx, logx.NewTaskID())` — **per poll-cycle** in `V2EX`/`HackerNews` `Run`, **per `/summary` command** in `command.Bot.handle` — and in-task logs use the `slog.*Context(ctx, …)` variants so the id flows through. Infra logs (startup/shutdown in `main`) use plain `slog.*` and carry no id. When adding logging inside a task path, use `slog.InfoContext`/`ErrorContext` with the task ctx, not `slog.Info`.
- Docker env var is named `RUST_CONFIG_PATH` for backward compatibility with the Rust deployment — not a typo.
