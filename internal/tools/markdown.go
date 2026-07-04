// Package tools 放跨包通用的小工具函数（Markdown 转义、UTF-8 截断等）。
package tools

import "strings"

// mdv2Escaper 是包级私有变量。
//
// 关键概念：
//   - var 在函数外定义就是"包级变量"，包内任何地方都能用；
//   - 小写开头 = 包外不可见；
//   - 这里只创建一次（程序启动时），后续 EscapeMarkdownV2 复用同一个实例 —— 比每次都新建快得多。
//
// strings.NewReplacer 接收"成对"的参数：旧串 1, 新串 1, 旧串 2, 新串 2 ...
// 它会一次性匹配所有 pattern，比写多个 strings.ReplaceAll 高效。
//
// 反斜杠两种写法：
//   - `\_`     反引号是"原始字符串"，里面的 \ 就是 \ 本身
//   - "\\`"    双引号是"普通字符串"，需要用 \\ 转义出一个 \
//     两种写法等价，按可读性挑：含反引号的串没法用反引号字面量包，所以那行只能用双引号。
var mdv2Escaper = strings.NewReplacer(
	"_", `\_`,
	"*", `\*`,
	"[", `\[`,
	"]", `\]`,
	"(", `\(`,
	")", `\)`,
	"~", `\~`,
	"`", "\\`",
	">", `\>`,
	"#", `\#`,
	"+", `\+`,
	"-", `\-`,
	"=", `\=`,
	"|", `\|`,
	"{", `\{`,
	"}", `\}`,
	".", `\.`,
	"!", `\!`,
)

// EscapeMarkdownV2 给字符串里的 Telegram MarkdownV2 特殊字符前加反斜杠。
// 大写开头 = 包外可见，是这个包的对外 API。
func EscapeMarkdownV2(s string) string {
	return mdv2Escaper.Replace(s)
}

// TruncateUTF8 按"字符数"截断字符串（不是字节数）。
//
// 为什么不用 s[:n]？
//   - 字符串底层是字节数组，s[:n] 取的是前 n 个字节；
//   - 中文一个字符 3 字节，s[:5] 可能切到字符中间，得到乱码 / invalid UTF-8。
//
// 解法：先转成 []rune（每个 rune 是一个 Unicode 码点），再切片，再转回 string。
func TruncateUTF8(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	// string → []rune：会按 UTF-8 解码遍历整个字符串，O(n)；不是免费操作但最直观。
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s // 已经够短，直接返回原串（避免一次无意义的复制）
	}
	// 切片 + 转回 string：用前 maxChars 个 rune 重新编码成 UTF-8 字符串。
	return string(runes[:maxChars])
}
