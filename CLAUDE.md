# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go service that scrapes V2EX and Hacker News, then pushes posts to a Telegram bot/channel. HN posts additionally get a Chinese AI summary via DeepSeek. Ported from an earlier Rust version (see "legacy parity" notes below).

## Commands

```bash
go mod tidy                                   # sync deps
go build -o bin/news2tg ./cmd/news2tg # build (binary name = directory name)
./bin/news2tg -c config.toml              # run; -c / --config selects the TOML file

go test ./...                                 # all tests
go test ./internal/monitor                    # one package
go test ./internal/monitor -run TestFilterNewTopic -v  # single test
go vet ./...
gofmt -l .                                    # list unformatted files
```

Requires Go ≥ 1.22 (relies on the Go 1.22 loop-variable semantics, though some code still defensively shadows — see `cmd/news2tg/main.go:127`).

## External runtime dependency

HN body extraction is **not** done in Go. It goes through the `digest.Fetcher` interface; the current implementation (`digest.Python`, `internal/digest/python.go`) calls a separate Python sidecar over HTTP at `http://127.0.0.1:50051/digest` ([hacker-news-digest](https://github.com/cheedonghu/hacker-news-digest)). Without it running, HN posts fall back to a "网页摘要获取失败" placeholder and skip the AI step. The `Dockerfile` clones and runs both the sidecar and this binary in one container; `docker-compose.yml` is the intended deployment.

## Architecture

`main.go` wires concrete implementations together, then runs each monitor as a goroutine under a single signal-cancellable `context.Context`. If any monitor returns a non-context error, `main` calls `cancel()` to bring all of them down together (`cmd/news2tg/main.go:130`).

The design is interface-based dependency injection — business code (`monitor`) depends only on interfaces, so swapping channels/providers means adding an implementation, not editing callers:

- **`monitor.Monitor`** (`internal/monitor/monitor.go`) — `Run(ctx, cfg) error`, blocks until ctx cancelled. Implementations: `V2EX`, `HackerNews`, and `command.Bot` (structurally — it isn't in the `monitor` package but satisfies the interface). Contract: return `ctx.Err()` for normal shutdown; handle transient errors internally (log + continue) rather than returning them.
- **`notify.Notifier`** (`internal/notify/notify.go`) — `Notify` (default chat) / `NotifyTo` (arbitrary chat, e.g. replying to a command sender) / `NotifyBatch`. Implementation: `Telegram`. **Escaping convention:** `Notify`/`NotifyTo` treat `content` as plain text and `EscapeMarkdownV2` it internally — callers must NOT pre-escape. `NotifyBatch` sends **pre-rendered MarkdownV2** (HN/V2EX craft `*bold*`/`[link]` markup and escape only the dynamic substrings), so it does not escape. `NotifyBatch` sends serially with a 1.5s gap to dodge Telegram rate limits.
- **`ai.Helper`** (`internal/ai/ai.go`) — `Summarize`. Implementation: `DeepSeek` (uses `go-openai` SDK pointed at `api.deepseek.com`, since DeepSeek is OpenAI-protocol compatible).
- **`digest.Fetcher`** (`internal/digest/digest.go`) — `Fetch(ctx, originURL)` for web body extraction. Implementations: `Python` (`python.go`, the Python sidecar over HTTP — preferred) and `Jina` (`jina.go`, `r.jina.ai` reader — fallback, optional `[jina]` api key). Injected into `HackerNews` via `NewHackerNews`.
- **`agent`** (`internal/agent/agent.go`) — a URL→Chinese-summary LLM agent that uses DeepSeek **function calling** to pick between the two `digest.Fetcher`s (python preferred; jina on failure/poor content), then summarizes. Reuses the `cfg.DeepSeek.APIToken`; uses model `deepseek-chat` (tool-capable, unlike `ai.DeepSeek`'s model). The LLM call is behind a small `chatCompleter` interface for testability. Wired into `main` and driven by the command bot. Appends a `本次消耗 tokens: N` footer (accumulated `usage.TotalTokens`) and slog-dumps the full `messages` transcript for audit before returning.
- **`command.Bot`** (`internal/command/bot.go`) — the **receive** side of Telegram (the rest of the app only sends). Long-polls `getUpdates`; on `/summary <url>` from a whitelisted admin (`[telegram] admin_ids`) it calls the `agent` and pushes the summary to the configured channel via `notify.Notifier`, acking the requester. Holds its own `*tgbotapi.BotAPI` (the notify client never polls, so no getUpdates conflict). Decision logic is in the pure `classify` method (unit-tested without a live bot); depends on a local `Summarizer` interface, not the `agent` package directly.
- **`model`** (`internal/model/model.go`) — shared DTOs (`NotifyBase`, V2EX `Topic`/`Node`/`Member`), separated into its own package to avoid import cycles.
- **`tools`** (`internal/tools/markdown.go`) — `EscapeMarkdownV2` (called inside `notify.Notify`/`NotifyTo` for plain-text sends, and by the monitors' markdown builders for the dynamic parts of `NotifyBatch` messages) and `TruncateUTF8` (rune-aware truncation; never slice strings by byte).

Monitors share a single `*http.Client` (one connection pool) created in `main`. They poll on a `time.Ticker` (V2EX every 2 min, HN every 5 min) and `select` on `ctx.Done()` vs `ticker.C`.

### Dedup

Each monitor keeps an in-memory `map[id]yyyymmdd` guarded by an `RWMutex`, pruned by a date window (V2EX 5 days, HN 15 days). **This state is lost on restart** — a known, intentional tradeoff (no persistence). After a restart, recently-pushed posts may be re-sent.

### Config

TOML (`internal/config/config.go`), mapped via struct tags. `[telegram]` (incl. `admin_ids` — the `/summary` command whitelist), `[features]` (per-source on/off switches + V2EX keyword/node filters + HN count/time-gap), `[deepseek]`, `[jina]` (api key for the `agent`'s jina fallback fetcher). `chat_id` and each `admin_ids` entry are stored as strings and `ParseInt`'d in `main` to avoid TOML int issues. `config.toml` is the template; `myconfig.toml` is a local (gitignored-style) variant.

## Conventions specific to this repo

- **Bilingual teaching comments.** Almost every line carries a Chinese comment explaining Go semantics (the original author was learning Go). When editing, match this density and style rather than stripping comments — they are the house style here, not noise.
- **AI/digest failures never abort a push.** `DeepSeek.Summarize` and the digest fetch return fallback strings (`"大模型返回异常"`, etc.) with `nil` error on purpose, so the post still goes out. Preserve this behavior unless explicitly changing it.
- **Legacy parity.** `formatHNMessage` (`internal/monitor/hackernews.go:320`) reproduces the old Rust output format byte-for-byte (spacing, newlines). `judgeNewsDate` only accepts the `"N hours ago"` form, matching Rust. Don't "clean up" these formats casually.
- **Known intentional quirk:** `filterNewTopic` combines keyword/node filters with **OR**, even though `config.toml` comments say "AND". A test in `internal/monitor/v2ex_test.go` locks the current OR behavior — changing it will (correctly) turn that test red.
- Logging is mixed: structured `slog` (JSON to stderr) in most places, but some flows still use `fmt.Printf` to stdout. Prefer `slog` for new code.
- **Task correlation (`task_id`):** `internal/logx` wraps the JSON handler (installed in `main.init`) and auto-injects a `task_id` attribute pulled from `context`. Each logical task tags its ctx once at entry via `logx.WithTaskID(ctx, logx.NewTaskID())` — **per poll-cycle** in `V2EX`/`HackerNews` `Run`, **per `/summary` command** in `command.Bot.handle` — and in-task logs use the `slog.*Context(ctx, …)` variants so the id flows through. Infra logs (startup/shutdown in `main`) use plain `slog.*` and carry no id. When adding logging inside a task path, use `slog.InfoContext`/`ErrorContext` with the task ctx, not `slog.Info`.
- Docker env var is named `RUST_CONFIG_PATH` for backward compatibility with the Rust deployment — not a typo.
