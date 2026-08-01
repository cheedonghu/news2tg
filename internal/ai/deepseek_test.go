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
