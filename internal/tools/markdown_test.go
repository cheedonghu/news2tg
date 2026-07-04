package tools

import "testing"

// TestEscapeMarkdownV2 验证转义器对各种特殊字符的处理。
// 这是简化版的表驱动测试：用结构体切片当用例表，没用 t.Run 拆子测试 —— 用例少时也够用。
func TestEscapeMarkdownV2(t *testing.T) {
	// 匿名结构体切片；in 是输入，want 是期望输出。
	cases := []struct {
		in, want string
	}{
		// 普通字符串：没有特殊字符，原样返回
		{"hello", "hello"},

		// 反引号字符串：` 内不需要转义反斜杠；`a\.b` = a + \ + . + b
		{"a.b", `a\.b`},

		// 多个特殊字符同时出现，全部要转义
		{"a_b*c", `a\_b\*c`},
		{"(x)[y]", `\(x\)\[y\]`},

		// 注意双引号字符串里 \\ 才是一个反斜杠；这里期望 "\`code\`"
		{"`code`", "\\`code\\`"},
	}
	for _, tc := range cases {
		got := EscapeMarkdownV2(tc.in)
		if got != tc.want {
			// %q 占位符会把字符串带引号、转义不可见字符地打印 —— 调试时比 %s 清楚得多。
			t.Errorf("EscapeMarkdownV2(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTruncateUTF8 验证按"字符"截断（不是字节）。
// 没用表驱动，因为只有两个用例；写两个 if 更直接。
func TestTruncateUTF8(t *testing.T) {
	// 中文 4 个字符，每个字符在 UTF-8 下占 3 字节。
	// 若错误地用 s[:2]，会把第一个"中"字切坏；正确实现应取前 2 个 rune = "中文"。
	if got := TruncateUTF8("中文测试", 2); got != "中文" {
		t.Errorf("TruncateUTF8 expected 中文, got %q", got)
	}
	// 边界：maxChars 大于实际长度 → 返回原串
	if got := TruncateUTF8("ab", 5); got != "ab" {
		t.Errorf("TruncateUTF8 expected ab, got %q", got)
	}
}
