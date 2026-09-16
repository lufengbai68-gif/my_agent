package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config 日志配置(从 yaml 读取)。
type Config struct {
	Level  string   `yaml:"level"`  // debug | info | warn | error
	Format string   `yaml:"format"` // json | text
	Redact []string `yaml:"redact"` // 字段名匹配则值打码(大小写不敏感)
}

// Init 初始化全局 slog logger,所有 log/slog 调用走这。
//
// handler 选 JSON(生产)或 text(开发),敏感字段在 ReplaceAttr 里打码。
// ctx-aware:每条 log 自动附加 request_id(如果有)。
func Init(cfg Config) {
	InitTo(os.Stdout, cfg)
}

// InitTo 同 Init,但输出到指定 writer(便于测试)。
func InitTo(w io.Writer, cfg Config) {
	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactReplacer(cfg.Redact),
	}
	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		handler = slog.NewJSONHandler(w, opts)
	}
	slog.SetDefault(slog.New(&ctxHandler{Handler: handler}))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// redactReplacer 返回 ReplaceAttr:对 key 命中 redact 列表的字段打码。
func redactReplacer(keys []string) func(groups []string, a slog.Attr) slog.Attr {
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[strings.ToLower(k)] = true
	}
	return func(groups []string, a slog.Attr) slog.Attr {
		if set[strings.ToLower(a.Key)] {
			return slog.String(a.Key, "***REDACTED***")
		}
		return a
	}
}
