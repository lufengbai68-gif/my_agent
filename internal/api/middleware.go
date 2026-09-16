package api

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/lufengbai68-gif/my_agent/internal/logging"
)

// maxBodyLogSize 请求/响应体日志最大字节数。
//
// 超过则只打前 N 字节 + truncated=true 标记。
//   - 请求体: 防止恶意大 body OOM, 也避免日志爆炸
//   - 响应体: SSE / 大文件不会撑爆日志
const maxBodyLogSize = 64 * 1024

// RequestID 给每个请求生成(或透传) X-Request-Id,塞 ctx +响应 header。
//
// 客户端可显式带 X-Request-Id header(便于跨服务追踪);
// 否则服务端生成 UUID v4。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-Id")
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Header("X-Request-Id", rid)
		ctx := logging.WithRequestID(c.Request.Context(), rid)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// AccessLog 打每条 HTTP 请求的访问日志:method / path / status / dur / client_ip / rid。
// 错误响应额外加 error_code(从 gin context 读)。
//
// 同时打印请求体 / 响应体(摘要):
//   - 仅 application/json 请求体会被打(跳过 multipart / binary)
//   - 响应体由 bodyLogWriter 截获, 超 maxBodyLogSize 自动 truncate
//   - SSE 流式响应会被自动截断到 64 KiB,不会撑爆日志
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// 1. peek 请求体(application/json only)
		reqBody, reqTrunc := peekJSONRequestBody(c)

		// 2. wrap response writer 以捕获响应体
		blw := &bodyLogWriter{ResponseWriter: c.Writer, max: maxBodyLogSize}
		c.Writer = blw

		start := time.Now()
		c.Next()
		dur := time.Since(start)

		// c.Next() 已返回, buf 不再增长, snapshot 拿到最终响应体
		if blw.buf.Len() > 0 {
			blw.captured = blw.buf.String()
		}

		attrs := []any{
			slog.String("method", c.Request.Method),
			slog.String("path", c.FullPath()),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("dur", dur),
			slog.String("client_ip", c.ClientIP()),
		}
		if reqBody != "" {
			attrs = append(attrs,
				slog.String("req_body", reqBody),
				slog.Bool("req_truncated", reqTrunc),
			)
		}
		if blw.captured != "" {
			attrs = append(attrs,
				slog.String("resp_body", blw.captured),
				slog.Bool("resp_truncated", blw.truncated),
			)
		}
		if c.Writer.Status() >= 400 {
			if code, ok := c.Get("error_code"); ok {
				attrs = append(attrs, slog.String("error_code", code.(string)))
			}
		}
		slog.InfoContext(ctx, "http_request", attrs...)
	}
}

// peekJSONRequestBody 读 c.Request.Body(只 application/json), 放回, 返回截断副本。
//
// 返回 ("", false) 表示跳过: 非 JSON / Content-Length 已知超大 / body 为空。
func peekJSONRequestBody(c *gin.Context) (string, bool) {
	if c.Request.Body == nil {
		return "", false
	}
	ct := c.GetHeader("Content-Type")
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		return "", false
	}
	// 已知超大直接跳过(节省一次 read)
	if c.Request.ContentLength > 0 && c.Request.ContentLength > maxBodyLogSize {
		return "<body too large to log>", true
	}

	limited := io.LimitReader(c.Request.Body, maxBodyLogSize+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		// body 读失败 → 把空 body 放回(让下游自己处理), 不打请求体
		c.Request.Body = io.NopCloser(bytes.NewReader(nil))
		return "", false
	}
	truncated := false
	if len(buf) > maxBodyLogSize {
		buf = buf[:maxBodyLogSize]
		truncated = true
	}
	// 读到的部分 + 余下的 body 拼回去
	rest, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(append(buf, rest...)))
	if len(buf) == 0 {
		return "", false
	}
	return string(buf), truncated
}

// bodyLogWriter 包装 gin.ResponseWriter, 复制 Write 的数据到内部 buf (上限 max 字节)。
//
// SSE / 大文件响应会自然 truncate 到 max, 主流程不受影响。
type bodyLogWriter struct {
	gin.ResponseWriter
	max       int
	buf       bytes.Buffer
	captured  string // buf 的字符串快照(<= max)
	truncated bool   // true 表示实际写入 > max, 日志不完整
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	if w.buf.Len() < w.max {
		remaining := w.max - w.buf.Len()
		if len(b) > remaining {
			w.buf.Write(b[:remaining])
			w.truncated = true
		} else {
			w.buf.Write(b)
		}
	} else {
		w.truncated = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *bodyLogWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

