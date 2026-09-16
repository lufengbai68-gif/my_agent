package logging

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// WithRequestID 把 request ID 塞 ctx,后续每条 log 自动带上。
func WithRequestID(ctx context.Context, rid string) context.Context {
	return context.WithValue(ctx, ctxKey{}, rid)
}

// RequestID 从 ctx 取 rid,没有返回空串。
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// ctxHandler 在每条 slog.Record 上自动附加 request_id(如果有)。
// 配合 slog.InfoContext / ErrorContext 等使用,调用方无需手动加 rid。
type ctxHandler struct{ slog.Handler }

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if rid := RequestID(ctx); rid != "" {
		r.AddAttrs(slog.String("request_id", rid))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h ctxHandler) WithGroup(name string) slog.Handler {
	return ctxHandler{Handler: h.Handler.WithGroup(name)}
}
