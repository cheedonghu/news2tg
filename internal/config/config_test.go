package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMention(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantID  int64
		wantN   string
		wantErr bool
	}{
		{"纯 id，显示名回落管理员", "123456", 123456, "管理员", false},
		{"id:显示名", "234567:老王", 234567, "老王", false},
		{"显示名带空格前后被 trim", " 345 : 小李 ", 345, "小李", false},
		{"冒号后为空 → 回落管理员", "456:", 456, "管理员", false},
		{"非法 id → 报错", "abc", 0, "", true},
		{"空串 → 报错", "", 0, "", true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseMention(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseMention(%q) 期望报错，却成功返回 %+v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMention(%q) 意外报错: %v", c.in, err)
			}
			if got.ID != c.wantID || got.Name != c.wantN {
				t.Fatalf("ParseMention(%q) = {%d,%q}, want {%d,%q}", c.in, got.ID, got.Name, c.wantID, c.wantN)
			}
		})
	}
}

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
