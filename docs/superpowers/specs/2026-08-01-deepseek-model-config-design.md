# DeepSeek 模型名改为可配置

日期：2026-08-01

## 背景

DeepSeek 的模型名目前写死在三处 Go 代码里：

- `internal/ai/deepseek.go:54` — `Summarize` 用 `deepseek-v4-flash`
- `internal/ai/deepseek.go:89` — `Advise`（穿衣建议）用 `deepseek-v4-flash`
- `internal/agent/agent.go:31` — `const defaultModel = "deepseek-chat"`

后两者故意不同款：agent 走 function calling，需要支持 tools 的模型，而摘要/建议只要便宜的轻量模型。

换模型现在需要改代码重新编译。目标是改成「改配置 + 重启」。

## 目标

模型名只在 `config.toml` 里出现，Go 代码中不保留任何模型名字面量。

## 非目标

- 不动 `BaseURL`（两处仍写死 `https://api.deepseek.com/v1`）。换 provider 是另一件事。
- 不把 `Summarize` 和 `Advise` 的模型拆成两个配置项——它们当前就是同款，拆开属于 YAGNI。
- 不加环境变量覆盖。

## 设计

### 配置结构

`[deepseek]` 段新增两个**必填**字段：

```toml
[deepseek]
api_token = "key"
# 摘要/穿衣建议用的轻量模型（Summarize / Advise）
model = "deepseek-v4-flash"
# agent 用的模型，必须支持 function calling（工具调用）
agent_model = "deepseek-chat"
```

`internal/config/config.go` 的 `DeepSeek` 结构体：

```go
type DeepSeek struct {
	APIToken   string `toml:"api_token"`
	Model      string `toml:"model"`       // Summarize / Advise 用
	AgentModel string `toml:"agent_model"` // agent 用，需支持 tools
}
```

两个字段而非一个：它们的能力要求本就不同，合并成一个字段的话，换轻量模型时会连带把 agent 的工具调用打挂。

### 校验

在 `config.FromFile` 内、`toml.DecodeFile` 成功之后做校验，任一字段 `strings.TrimSpace` 后为空即返回 error：

```go
if strings.TrimSpace(cfg.DeepSeek.Model) == "" {
	return nil, fmt.Errorf("[deepseek] model 未配置")
}
if strings.TrimSpace(cfg.DeepSeek.AgentModel) == "" {
	return nil, fmt.Errorf("[deepseek] agent_model 未配置")
}
```

选择「缺配置就启动失败」而非「回落默认值」：这样 Go 代码里不需要保留模型名兜底，模型名的唯一真相源就是配置文件。

校验放 config 包而非 main：配置合法性归 config 管，一处收口；且 `main.go:108/115` 无条件构造 `ai.NewDeepSeek` 和 `agent.NewAgent`（不看 `[features]` 开关），不存在「这个字段用不到所以可以缺」的情况。

`main.go:47` 现有的错误处理会把这个 error 变成启动失败，main 的错误分支不用改。

### 传参链路

构造函数各扩一个参数，**不返回 error**（保持 `internal/ai/deepseek.go:24` 注释所述的房规：构造函数只配置、不发请求、不返回 error）：

- `ai.NewDeepSeek(apiKey, model string) *DeepSeek`
  存进新字段 `DeepSeek.model`；`Summarize` 和 `Advise` 的 `ChatCompletionRequest.Model` 都改用 `d.model`。
- `agent.NewAgent(apiKey, model string, python, jina digest.Fetcher) *Agent`
  替换 `defaultModel` 常量。常量连同其上方「为什么和 ai 包不同款」的注释一并删除，该信息移到 `config.toml` 的字段注释里。
- `cmd/news2tg/main.go:108` 传 `cfg.DeepSeek.Model`，`main.go:115` 传 `cfg.DeepSeek.AgentModel`。

## 测试

新增 `internal/config` 单元测试（本次唯一的新增纯逻辑，值得锁住）：

- 合法 TOML 解析出正确的 `Model` / `AgentModel`
- 缺 `model` → `FromFile` 返回 error
- 缺 `agent_model` → `FromFile` 返回 error

用 `t.TempDir()` 写临时 TOML 文件，不依赖仓库里的真实配置。

已有测试受影响处：

- `internal/agent/agent_test.go:39` 的 `NewAgent` 调用点补参数 `cfg.DeepSeek.AgentModel`。
- 该测试读 `../../myconfig.toml`。`myconfig.toml` 在 `.gitignore` 内但本地存在，需同步补上两个新字段；否则 `FromFile` 会因校验失败返回 error，测试走 `t.Skipf` 静默跳过。

## 验证

```bash
go build ./...
go vet ./...
gofmt -l .
go test ./... -short   # -short 跳过联网的 agent 端到端测试
```

## 涉及文件

| 文件 | 改动 |
| --- | --- |
| `internal/config/config.go` | `DeepSeek` 加两字段；`FromFile` 加校验 |
| `internal/config/config_test.go` | 新增：解析 + 两个缺字段用例 |
| `internal/ai/deepseek.go` | 结构体加 `model` 字段；构造函数加参数；两处请求改用 `d.model` |
| `internal/agent/agent.go` | 删 `defaultModel` 常量；构造函数加 `model` 参数 |
| `internal/agent/agent_test.go` | 调用点补参数 |
| `cmd/news2tg/main.go` | 两处调用点传配置值 |
| `config.toml` | 模板补两个字段 + 注释 |
| `myconfig.toml` | 本地配置补两个字段（不入库） |
| `CLAUDE.md` | Config 段落补一句新字段说明 |
