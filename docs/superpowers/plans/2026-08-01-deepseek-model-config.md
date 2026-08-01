# DeepSeek 模型可配置化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把写死在 Go 代码里的三处 DeepSeek 模型名挪进 `config.toml`，换模型只需改配置重启。

**Architecture:** `[deepseek]` 段新增两个必填字段 `model`（`Summarize`/`Advise` 用）和 `agent_model`（agent 用，需支持 function calling）。`config.FromFile` 解析后立即校验非空，缺失即返回 error 让启动失败——因此 Go 代码里不保留任何模型名兜底默认值。两个构造函数各扩一个 `model string` 参数，由 `main` 从配置传入。

**Tech Stack:** Go ≥ 1.22，`github.com/BurntSushi/toml`，`github.com/sashabaranov/go-openai`，标准库 `testing`。

## Global Constraints

- 设计文档：`docs/superpowers/specs/2026-08-01-deepseek-model-config-design.md`
- **房规：构造函数不返回 error**（见 `internal/ai/deepseek.go:24` 的注释）。`NewDeepSeek` / `NewAgent` 只加参数，签名仍返回单值。
- **房规：中文教学注释密度**。本仓库几乎每行都有解释 Go 语义的中文注释，新增/修改的代码必须保持同样密度和风格，不要精简掉注释。
- **不动 `BaseURL`**：`https://api.deepseek.com/v1` 在 `internal/ai/deepseek.go:27` 和 `internal/agent/agent.go:63` 仍然写死。
- **不加环境变量覆盖**，不拆 `Summarize`/`Advise` 的模型。
- 完成后 Go 代码里不得再出现模型名字面量（测试 fixture 除外）。
- 提交信息沿用仓库风格：`#feat` / `#fix` / `#docs` 前缀 + 中文描述。
- 当前分支 `dev`，直接在该分支提交。

---

### Task 1: 配置字段、启动校验与配置文件迁移

新增两个配置字段并让缺失时启动失败。同一任务内把三个配置文件（模板、本地配置、CLAUDE.md 文档）一起补齐，否则校验一上线就会让 `main` 和 `internal/agent` 的端到端测试立刻失效。

**Files:**
- Modify: `internal/config/config.go:76-78`（`DeepSeek` 结构体）、`internal/config/config.go:97-105`（`FromFile`）
- Test: `internal/config/config_test.go`（已存在，追加用例）
- Modify: `config.toml:35-36`
- Modify: `myconfig.toml`（本地文件，`.gitignore` 内，**不要 `git add`**）
- Modify: `CLAUDE.md`（Config 段落）

**Interfaces:**
- Consumes: 无（本计划的第一个任务）
- Produces:
  - `config.DeepSeek` 结构体新增两个导出字段：`Model string`、`AgentModel string`
  - `config.FromFile(path string) (*Config, error)` 语义变更：`[deepseek] model` 或 `agent_model` 为空（或纯空白）时返回非 nil error，`*Config` 为 nil
  - Task 2 用 `cfg.DeepSeek.Model`，Task 3 用 `cfg.DeepSeek.AgentModel`

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 末尾追加（文件已有 `package config` 和 `import "testing"`，需要把 import 改成分组形式）：

```go
// 把 import "testing" 改成：
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)
```

```go
// writeTempTOML 把配置内容写进临时文件并返回路径。
// t.TempDir() 给每个测试一个独立目录，测试结束后由 testing 包自动清理，
// 所以不需要手动 defer os.Remove。
func writeTempTOML(t *testing.T, content string) string {
	t.Helper() // 标记为辅助函数：断言失败时行号指向调用方，方便定位
	path := filepath.Join(t.TempDir(), "cfg.toml")
	// 0o600 = 仅属主可读写；配置里有 api_token，权限收紧是好习惯。
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

// TestFromFileDeepSeekModel 验证两个模型字段能被正确解析出来。
func TestFromFileDeepSeekModel(t *testing.T) {
	path := writeTempTOML(t, `
[deepseek]
api_token = "k"
model = "deepseek-v4-flash"
agent_model = "deepseek-chat"
`)
	cfg, err := FromFile(path)
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if cfg.DeepSeek.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %q, want %q", cfg.DeepSeek.Model, "deepseek-v4-flash")
	}
	if cfg.DeepSeek.AgentModel != "deepseek-chat" {
		t.Errorf("AgentModel = %q, want %q", cfg.DeepSeek.AgentModel, "deepseek-chat")
	}
}

// TestFromFileMissingModel 验证模型缺失时 FromFile 报错（而不是回落默认值）。
// 这是「模型名唯一真相源是配置文件」这条设计的守门测试。
func TestFromFileMissingModel(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantKey string // 期望错误信息里出现的配置项名
	}{
		{
			name:    "缺 model",
			content: "[deepseek]\napi_token = \"k\"\nagent_model = \"deepseek-chat\"\n",
			wantKey: "model",
		},
		{
			name:    "缺 agent_model",
			content: "[deepseek]\napi_token = \"k\"\nmodel = \"deepseek-v4-flash\"\n",
			wantKey: "agent_model",
		},
		{
			name:    "model 只有空白字符",
			content: "[deepseek]\napi_token = \"k\"\nmodel = \"   \"\nagent_model = \"deepseek-chat\"\n",
			wantKey: "model",
		},
		{
			name:    "整个 deepseek 段缺失",
			content: "[telegram]\napi_token = \"t\"\n",
			wantKey: "model",
		},
	}
	for _, c := range cases {
		c := c // Go 1.22 之后其实不必，但仓库里其他测试也这么写，保持一致
		t.Run(c.name, func(t *testing.T) {
			cfg, err := FromFile(writeTempTOML(t, c.content))
			if err == nil {
				t.Fatalf("FromFile 期望报错，却成功返回 %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("报错时应返回 nil *Config，实际 %+v", cfg)
			}
			if !strings.Contains(err.Error(), c.wantKey) {
				t.Errorf("错误信息 %q 未提到配置项 %q", err.Error(), c.wantKey)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/config -run 'TestFromFile' -v
```

预期：FAIL。`TestFromFileDeepSeekModel` 报 `cfg.DeepSeek.Model` undefined（编译错误：结构体还没这两个字段）。

- [ ] **Step 3: 加结构体字段**

`internal/config/config.go:75-78` 整段替换为：

```go
// DeepSeek 段：AI 摘要服务的 key 和模型名。
// 两个模型分开配是有意的：agent 走 function calling，必须用支持 tools 的模型；
// Summarize/Advise 只是纯文本生成，用便宜的轻量模型就够。
// 合成一个字段的话，换轻量模型时会顺手把 agent 的工具调用打挂。
type DeepSeek struct {
	APIToken   string `toml:"api_token"`
	Model      string `toml:"model"`       // Summarize / Advise 用
	AgentModel string `toml:"agent_model"` // agent 用，必须支持 function calling
}
```

- [ ] **Step 4: 加 FromFile 校验**

`internal/config/config.go:97-105` 的 `FromFile` 整个函数替换为：

```go
// FromFile 读取并解析配置文件。
// 返回 (*Config, error)：成功返回填好的指针 + nil；失败返回 nil + error。
func FromFile(path string) (*Config, error) {
	var cfg Config // 零值结构体；所有字段默认零值
	// toml.DecodeFile：第二个参数必须是指针（&cfg），库才能写入。
	// 第一个返回值是 MetaData（哪些 key 被识别等），这里用 _ 丢弃。
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, err
	}
	// 模型名故意不在 Go 代码里留兜底默认值：缺配置就启动失败，
	// 这样「模型名写在哪」只有一个答案——配置文件。
	// TrimSpace 是防御误填空格（TOML 里 model = "  " 也算填了）。
	if strings.TrimSpace(cfg.DeepSeek.Model) == "" {
		return nil, fmt.Errorf("[deepseek] model 未配置")
	}
	if strings.TrimSpace(cfg.DeepSeek.AgentModel) == "" {
		return nil, fmt.Errorf("[deepseek] agent_model 未配置")
	}
	return &cfg, nil
}
```

`fmt` 和 `strings` 在 `internal/config/config.go:6-8` 已经 import 了，不用动 import 块。

- [ ] **Step 5: 运行测试确认通过**

```bash
go test ./internal/config -v
```

预期：PASS，包含 `TestParseMention`、`TestFromFileDeepSeekModel`、`TestFromFileMissingModel` 全部子用例。

- [ ] **Step 6: 更新配置模板 config.toml**

`config.toml:35-36` 两行替换为：

```toml
[deepseek]
api_token="key"
# 摘要 / 穿衣建议用的模型（对应 ai.DeepSeek 的 Summarize、Advise）。
# 换模型改这里重启即可，代码里没有默认值——留空会导致启动失败。
model = "deepseek-v4-flash"
# agent（/summary 指令）用的模型，必须支持 function calling（工具调用），
# 否则 agent 无法在 python / jina 两个正文提取器之间做选择。同样留空即启动失败。
agent_model = "deepseek-chat"
```

- [ ] **Step 7: 更新本地配置 myconfig.toml**

`myconfig.toml` 的 `[deepseek]` 段（第 22-23 行附近，只有 `api_token` 一行）在 `api_token` 下方补两行，**保持已有的 api_token 值不变**：

```toml
model = "deepseek-v4-flash"
agent_model = "deepseek-chat"
```

这个文件在 `.gitignore` 里，**不要 `git add`**。不补的话 `internal/agent/agent_test.go:26` 的 `FromFile` 会因校验失败返回 error，端到端测试会静默 skip 掉。

- [ ] **Step 8: 更新 CLAUDE.md**

在 `CLAUDE.md` 的 `### Config` 段落里，把描述 `[deepseek]` 的部分改成提到新字段。原文里这个片段（注意是半角括号和逗号）：

```
`[deepseek]`, `[jina]` (api key for the `agent`'s jina fallback fetcher)
```

替换为：

```
`[deepseek]` (api key + `model` for `Summarize`/`Advise`, `agent_model` for the tool-calling agent — both **required**; `FromFile` rejects empty values so startup fails fast, and no model name is hardcoded in Go), `[jina]` (api key for the `agent`'s jina fallback fetcher)
```

保持 CLAUDE.md 原有的英文行文风格，不要改成中文。

- [ ] **Step 9: 验证并提交**

```bash
gofmt -l .
go vet ./...
go test ./... -short
```

预期：`gofmt -l .` 无输出；`go vet` 无输出；测试全部 PASS（`-short` 会跳过联网的 agent 端到端测试）。

```bash
git add internal/config/config.go internal/config/config_test.go config.toml CLAUDE.md
git commit -m "#feat deepseek 模型名改为配置项并在启动时校验"
```

---

### Task 2: ai.DeepSeek 使用配置传入的模型

把 `Summarize` 和 `Advise` 两处 `deepseek-v4-flash` 字面量换成结构体字段。构造函数签名变了，同一任务内改掉 `main` 的调用点，保证任务结束时整个仓库可编译。

**Files:**
- Modify: `internal/ai/deepseek.go:16-29`（结构体 + 构造函数）、`internal/ai/deepseek.go:54`、`internal/ai/deepseek.go:89`
- Create: `internal/ai/deepseek_test.go`
- Modify: `cmd/news2tg/main.go:108`

**Interfaces:**
- Consumes: `config.DeepSeek.Model`（Task 1 产出）
- Produces: `ai.NewDeepSeek(apiKey, model string) *DeepSeek` —— 参数顺序为 (key, model)，仍不返回 error；未导出字段 `model string`

- [ ] **Step 1: 写失败测试**

新建 `internal/ai/deepseek_test.go`：

```go
package ai

import "testing"

// TestNewDeepSeekModel 验证构造函数把模型名存了下来。
// 测试和被测代码同包（package ai），所以能读未导出字段 model。
// 这个测试很轻，但它锁住的是「模型名来自参数、不是硬编码」这条不变式。
func TestNewDeepSeekModel(t *testing.T) {
	d := NewDeepSeek("fake-key", "some-model")
	if d.model != "some-model" {
		t.Errorf("model = %q, want %q", d.model, "some-model")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/ai -run TestNewDeepSeekModel -v
```

预期：FAIL，编译错误 `too many arguments in call to NewDeepSeek`（构造函数当前只收一个参数）。

- [ ] **Step 3: 改结构体和构造函数**

`internal/ai/deepseek.go:13-29` 整段替换为：

```go
// DeepSeek 实现 ai.Helper 接口。
// DeepSeek 的 API 是 OpenAI 协议兼容的，所以直接复用 go-openai 这个 SDK，
// 把 BaseURL 换成 DeepSeek 的地址即可。
type DeepSeek struct {
	client *openai.Client // SDK 客户端，内部维护 HTTP client
	// 模型名，由 main 从 [deepseek] model 传入。
	// 不写死是为了换模型只改配置重启，不用重新编译。
	model string
	// 上下文窗口大小
	contextLength int
	// AI总结后给用户阅读的最大长度
	readerLength int
}

// NewDeepSeek 构造函数。注意没有返回 error：这里只是配置，没真发请求。
// model 由调用方（main）从配置传入，非空性已在 config.FromFile 里校验过。
func NewDeepSeek(apiKey, model string) *DeepSeek {
	cfg := openai.DefaultConfig(apiKey)         // 默认配置（OpenAI 官方地址）
	cfg.BaseURL = "https://api.deepseek.com/v1" // 改成 DeepSeek 的地址
	return &DeepSeek{client: openai.NewClientWithConfig(cfg), model: model, contextLength: 60000, readerLength: 2000}
}
```

- [ ] **Step 4: 替换两处模型字面量**

`internal/ai/deepseek.go:54`（`Summarize` 里）：

```go
		Model: d.model,
```

`internal/ai/deepseek.go:89`（`Advise` 里，注意把旧的行尾注释一起换掉）：

```go
		Model: d.model, // 与 Summarize 同一个配置项
```

- [ ] **Step 5: 改 main 调用点**

`cmd/news2tg/main.go:108`：

```go
	aiClient := ai.NewDeepSeek(cfg.DeepSeek.APIToken, cfg.DeepSeek.Model)
```

- [ ] **Step 6: 运行测试确认通过**

```bash
go build ./... && go test ./internal/ai -v
```

预期：build 无输出（成功），`TestNewDeepSeekModel` PASS。

- [ ] **Step 7: 提交**

```bash
gofmt -l . && go vet ./...
git add internal/ai/deepseek.go internal/ai/deepseek_test.go cmd/news2tg/main.go
git commit -m "#feat ai.DeepSeek 改用配置传入的模型名"
```

---

### Task 3: agent 使用配置传入的模型

删掉 `defaultModel` 常量，模型名从参数进来。同样在本任务内改掉 `main` 和已有测试的调用点。

**Files:**
- Modify: `internal/agent/agent.go:29-31`（删常量）、`internal/agent/agent.go:59-74`（构造函数）
- Modify: `internal/agent/agent_test.go:39`
- Modify: `cmd/news2tg/main.go:115`

**Interfaces:**
- Consumes: `config.DeepSeek.AgentModel`（Task 1 产出）
- Produces: `agent.NewAgent(apiKey, model string, python, jina digest.Fetcher) *Agent` —— model 排在 apiKey 之后、两个 Fetcher 之前，与 `ai.NewDeepSeek` 的 (key, model, …) 顺序保持一致

- [ ] **Step 1: 写失败测试**

在 `internal/agent/agent_test.go` 末尾追加：

```go
// TestNewAgentModel 验证构造函数把模型名存了下来（同包测试，可读未导出字段）。
// 传 nil 作为两个 Fetcher：构造函数只是把它们塞进 map，不会调用，所以安全。
func TestNewAgentModel(t *testing.T) {
	a := NewAgent("fake-key", "some-model", nil, nil)
	if a.model != "some-model" {
		t.Errorf("model = %q, want %q", a.model, "some-model")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/agent -run TestNewAgentModel -v
```

预期：FAIL，编译错误 `too many arguments in call to NewAgent`。

- [ ] **Step 3: 删掉 defaultModel 常量**

删除 `internal/agent/agent.go:29-31` 这三行（常量和它上面两行注释）：

```go
// defaultModel 用支持工具调用（function calling）的 DeepSeek 模型。
// 注意：ai/deepseek.go 里用的 "deepseek-v4-flash" 不一定支持 tools，所以这里单列。
const defaultModel = "deepseek-chat"
```

「为什么和 ai 包不同款」这个信息已经写进 `config.toml` 的 `agent_model` 注释里（Task 1 Step 6），不用在代码里重复。

- [ ] **Step 4: 改构造函数**

`internal/agent/agent.go:59-74` 整段替换为：

```go
// NewAgent 构造函数。
// apiKey 复用 DeepSeek 的 key；model 由 main 从 [deepseek] agent_model 传入，
// 必须是支持 function calling 的模型，否则下面的 toolDefs 形同虚设。
// python / jina 都是 digest.Fetcher（用接口便于换实现/测试）。
func NewAgent(apiKey, model string, python, jina digest.Fetcher) *Agent {
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = "https://api.deepseek.com/v1" // 同 ai/deepseek.go：DeepSeek 兼容 OpenAI 协议
	return &Agent{
		llm:   openai.NewClientWithConfig(cfg),
		model: model,
		tools: map[string]digest.Fetcher{
			toolPython: python,
			toolJina:   jina,
		},
		toolDefs: buildToolDefs(),
		maxSteps: defaultMaxSteps,
	}
}
```

- [ ] **Step 5: 改已有端到端测试的调用点**

`internal/agent/agent_test.go:39`：

```go
	a := NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, python, jina)
```

- [ ] **Step 6: 改 main 调用点**

`cmd/news2tg/main.go:115`：

```go
	summaryAgent := agent.NewAgent(cfg.DeepSeek.APIToken, cfg.DeepSeek.AgentModel, digestFetcher, jinaFetcher)
```

- [ ] **Step 7: 运行测试确认通过**

```bash
go build ./... && go test ./internal/agent -short -v
```

预期：build 成功；`TestNewAgentModel` PASS；`TestAgentSummarize` SKIP（`-short`）。

- [ ] **Step 8: 全量验证**

```bash
gofmt -l .
go vet ./...
go test ./... -short
```

预期：`gofmt -l .` 无输出，`go vet` 无输出，全部测试 PASS/SKIP，无 FAIL。

再确认 Go 代码里不再有模型名字面量：

```bash
grep -rn "deepseek-v4-flash\|deepseek-chat" --include=*.go .
```

预期：只在 `internal/config/config_test.go` 里出现（那是测试 fixture）。`internal/ai/deepseek.go`、`internal/agent/agent.go`、`cmd/news2tg/main.go` 都不应出现。

- [ ] **Step 9: 提交**

```bash
git add internal/agent/agent.go internal/agent/agent_test.go cmd/news2tg/main.go
git commit -m "#feat agent 改用配置传入的模型名并删除 defaultModel 常量"
```

---

## 完成后的人工验收

以上三个任务做完后，建议手动跑一次真实启动，验证「改配置即换模型」这条路径：

1. 把 `myconfig.toml` 的 `model` 改成一个不存在的模型名，`go run ./cmd/news2tg -c myconfig.toml`，触发一次 HN 推送，日志里应出现 `大模型返回异常`（兜底行为保留，推送照发）。
2. 改回 `deepseek-v4-flash`，重启，摘要恢复正常。
3. 把 `model` 那行整个注释掉重启，进程应在启动阶段报 `[deepseek] model 未配置` 并退出。
