package config

import "testing"

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
