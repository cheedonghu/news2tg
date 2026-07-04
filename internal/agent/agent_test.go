package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
	"github.com/cheedonghu/news2tg/internal/digest"
)

// TestAgentSummarize 真实端到端跑一遍 agent：
// 真实 DeepSeek 客户端 + 真实的两个提取器（python sidecar + jina），对目标网址产出中文总结。
//
// 依赖本地 myconfig.toml 里的真实 deepseek/jina key；缺失或 -short 时跳过，保证 CI/离线不挂。
// 注意：python sidecar（127.0.0.1:50051）通常没起，此时模型应自动回退到 jina——
// 正好顺带验证了 function-calling 的回退链路。
func TestAgentSummarize(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过联网/真实大模型测试（-short）")
	}

	// 读本地真实配置：test 的工作目录是包目录 internal/agent，仓库根在 ../../。
	cfg, err := config.FromFile("../../myconfig.toml")
	if err != nil {
		t.Skipf("未找到 myconfig.toml，跳过：%v", err)
	}
	if cfg.DeepSeek.APIToken == "" {
		t.Skip("myconfig.toml 缺少 deepseek api_token，跳过")
	}

	// 共享一个 http 客户端给两个提取器；超时给宽松点（jina 抓取偶尔慢）。
	httpClient := &http.Client{Timeout: 300 * time.Second}
	python := digest.NewPython(httpClient)
	jina := digest.NewJina(httpClient, cfg.Jina.APIToken)

	a := NewAgent(cfg.DeepSeek.APIToken, python, jina)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	summary, err := a.Summarize(ctx, "https://luyublog.com/PluginOfPikPak/")
	if err != nil {
		t.Fatalf("Summarize 返回错误: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatalf("Summarize 返回空总结")
	}
	// 末尾应带上 token 统计页脚。
	if !strings.Contains(summary, "tokens:") {
		t.Errorf("总结末尾未带 token 统计，summary=%q", summary)
	}

	t.Logf("agent 总结结果:\n%s", summary)
}
