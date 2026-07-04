// Package logx 给 slog 加一个"任务关联 id（task_id）"的能力：
//
// 思路：把 task_id 放进 context，再用一个包一层的 slog.Handler，在写每条日志时
// 自动从 ctx 取出 task_id 加到记录里。这样调用方只要在任务入口 WithTaskID 一次，
// 之后链路里用 slog.InfoContext(ctx, ...) 打的日志就都带上同一个 id —— 不用到处传 logger。
package logx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"time"
)

// ctxKey 是私有类型，避免和别的包往 context 里塞的 key 冲突。
type ctxKey struct{}

// WithTaskID 把 task_id 存进 ctx，返回派生的新 ctx。
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// taskID 从 ctx 取 task_id；没有则 ok=false。
func taskID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok && id != ""
}

// NewTaskID 生成一个短随机 id（8 位 hex）。
// 用 crypto/rand 取 4 字节；极端情况下取随机失败，用纳秒时间兜底，保证总能返回非空。
func NewTaskID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// Handler 包一层底层 slog.Handler：写日志时若 ctx 里有 task_id 就自动加上。
// 嵌入 slog.Handler 复用 Enabled；Handle/WithAttrs/WithGroup 自己实现以保持包裹。
type Handler struct {
	slog.Handler
}

// New 用给定的底层 handler 构造带 task_id 注入能力的 Handler。
func New(base slog.Handler) Handler {
	return Handler{Handler: base}
}

// Handle 在交给底层 handler 前，把 ctx 里的 task_id（若有）作为属性加上。
func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := taskID(ctx); ok {
		r.AddAttrs(slog.String("task_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs / WithGroup：底层返回的是裸 handler，这里重新包一层保持 task_id 注入。
func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return Handler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{Handler: h.Handler.WithGroup(name)}
}
