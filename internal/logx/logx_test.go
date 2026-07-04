package logx

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// newTestLogger 返回一个写到 buf 的、带 task_id 注入的 logger。
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(New(slog.NewJSONHandler(buf, nil)))
}

// TestHandlerInjectsTaskID：ctx 里有 task_id 时，日志应带上 "task_id"。
func TestHandlerInjectsTaskID(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	ctx := WithTaskID(context.Background(), "abc12345")
	logger.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, `"task_id":"abc12345"`) {
		t.Errorf("日志应包含 task_id，实际输出: %s", out)
	}
}

// TestHandlerNoTaskID：ctx 里没有 task_id（如 Background）时，不应出现 task_id 字段。
func TestHandlerNoTaskID(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	logger.InfoContext(context.Background(), "hello")

	if strings.Contains(buf.String(), "task_id") {
		t.Errorf("无 task_id 时不应出现该字段，实际输出: %s", buf.String())
	}
}

// TestWithAttrsKeepsInjection：经过 With(...) 派生后，task_id 注入仍然生效
// （验证 WithAttrs 重新包裹了 Handler，没退化成裸 handler）。
func TestWithAttrsKeepsInjection(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf).With("comp", "x")

	ctx := WithTaskID(context.Background(), "deadbeef")
	logger.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, `"task_id":"deadbeef"`) {
		t.Errorf("With 派生后仍应注入 task_id，实际输出: %s", out)
	}
}

// TestNewTaskID：生成的 id 非空且每次不同。
func TestNewTaskID(t *testing.T) {
	a, b := NewTaskID(), NewTaskID()
	if a == "" || b == "" {
		t.Fatal("NewTaskID 不应返回空")
	}
	if a == b {
		t.Errorf("两次 NewTaskID 不应相同: %s == %s", a, b)
	}
}
