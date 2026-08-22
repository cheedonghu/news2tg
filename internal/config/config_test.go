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

[storage]
db_path = "./target/dev.db"
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
		name        string
		content     string
		wantErrText string // 期望的完整错误信息（不是子串式的配置项名，避免 "model" 误命中 "agent_model"）
	}{
		{
			name:        "缺 model",
			content:     "[deepseek]\napi_token = \"k\"\nagent_model = \"deepseek-chat\"\n",
			wantErrText: "[deepseek] model 未配置",
		},
		{
			name:        "缺 agent_model",
			content:     "[deepseek]\napi_token = \"k\"\nmodel = \"deepseek-v4-flash\"\n",
			wantErrText: "[deepseek] agent_model 未配置",
		},
		{
			name:        "model 只有空白字符",
			content:     "[deepseek]\napi_token = \"k\"\nmodel = \"   \"\nagent_model = \"deepseek-chat\"\n",
			wantErrText: "[deepseek] model 未配置",
		},
		{
			name:        "整个 deepseek 段缺失",
			content:     "[telegram]\napi_token = \"t\"\n",
			wantErrText: "[deepseek] model 未配置",
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
			if !strings.Contains(err.Error(), c.wantErrText) {
				t.Errorf("错误信息 %q 未包含期望文本 %q", err.Error(), c.wantErrText)
			}
		})
	}
}

// musicTOML 拼一份带 [music] 段的最小可用配置。
// deepseek/storage 是 FromFile 的必填项，每个用例都得带上，抽成 helper 免得重复。
func musicTOML(musicSection string) string {
	return `
[deepseek]
api_token = "k"
model = "deepseek-v4-flash"
agent_model = "deepseek-chat"

[storage]
db_path = "./target/dev.db"
` + musicSection
}

// TestFromFileMusicAbsent 验证：整个 [music] 段缺失时不报错，Configured() 为 false。
// 这是「可选功能不能让现有部署升级即挂」这条设计的守门测试。
func TestFromFileMusicAbsent(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML("")))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if cfg.Music.Configured() {
		t.Errorf("[music] 段缺失时 Configured() 应为 false")
	}
}

// TestFromFileMusicComplete 验证：六项齐全时解析正确且 Configured() 为 true。
func TestFromFileMusicComplete(t *testing.T) {
	cfg, err := FromFile(writeTempTOML(t, musicTOML(`
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://alist:5244/dav/onedrive/Music"
`)))
	if err != nil {
		t.Fatalf("FromFile 意外报错: %v", err)
	}
	if !cfg.Music.Configured() {
		t.Fatalf("六项齐全时 Configured() 应为 true")
	}
	if cfg.Music.WebdavName1 != "阿里云盘" {
		t.Errorf("WebdavName1 = %q, want %q", cfg.Music.WebdavName1, "阿里云盘")
	}
	if cfg.Music.WebdavURL2 != "http://alist:5244/dav/onedrive/Music" {
		t.Errorf("WebdavURL2 = %q", cfg.Music.WebdavURL2)
	}
	if cfg.Music.WebdavPass != "pw" {
		t.Errorf("WebdavPass = %q, want %q", cfg.Music.WebdavPass, "pw")
	}
}

// TestFromFileMusicPartial 验证：填了一部分就直接启动失败。
// 半配置一定是打字漏了，静默降级只会让人对着「上传失败」抓瞎。
func TestFromFileMusicPartial(t *testing.T) {
	cases := []struct {
		name    string
		section string
	}{
		{
			name: "只填了凭据没填目标",
			section: `
[music]
webdav_user = "alist"
webdav_pass = "pw"
`,
		},
		{
			name: "第二个目标缺 url",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "pw"
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
`,
		},
		{
			name: "只有空白字符也算填了",
			section: `
[music]
webdav_user = "   "
`,
		},
		{
			// 复现 review 指出的 Critical：五项填了正经值，只有一项是纯空格
			// （典型的打字失误——多敲了个空格、或者复制粘贴漏了值）。
			// touched() 看原始值，六项都非空，不会被误判成"整段没配"；
			// usable() 一 TrimSpace，webdav_pass 就现形成空串，必须报错，
			// 不能像 Configured() 那样悄悄把整个 [music] 判成"未配置"就完事。
			name: "五项真值加一项纯空格",
			section: `
[music]
webdav_user   = "alist"
webdav_pass   = "   "
webdav_name_1 = "阿里云盘"
webdav_url_1  = "http://alist:5244/dav/aliyun/Music"
webdav_name_2 = "OneDrive"
webdav_url_2  = "http://alist:5244/dav/onedrive/Music"
`,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			cfg, err := FromFile(writeTempTOML(t, musicTOML(c.section)))
			if err == nil {
				t.Fatalf("FromFile 期望报错，却成功返回 %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("报错时应返回 nil *Config，实际 %+v", cfg)
			}
			if !strings.Contains(err.Error(), "[music]") {
				t.Errorf("错误信息 %q 应指明是 [music] 段的问题", err.Error())
			}
		})
	}
}
