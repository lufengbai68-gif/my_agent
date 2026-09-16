package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

// handleChat POST /v1/chat/completions
// Stream=false 返回 JSON；Stream=true 切换 SSE（同一端点，OpenAI 惯例）。
func (h *Handler) handleChat(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			fail(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds limit")
			return
		}
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := req.Validate(); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if req.Stream {
		h.streamChat(c, &req)
		return
	}

	opts := llm.GenerateOptions{
		ModelKey:        req.Model,
		GenerationParams: req.GenerationParams,
	}
	result, err := h.svc.Complete(c.Request.Context(), req.SessionID, opts, req.ToUserMessage())
	if err != nil {
		failErr(c, err)
		return
	}

	resp := ChatResponse{
		SessionID: result.SessionID,
		Agent:     result.AgentName,
		Role:      string(result.Message.Role),
		Content:   result.Message.Content,
		Media:     result.Media,
		CreatedAt: result.CreatedAt,
	}
	if result.Message.ResponseMeta != nil {
		resp.FinishReason = result.Message.ResponseMeta.FinishReason
		if u := result.Message.ResponseMeta.Usage; u != nil {
			resp.Usage = &Usage{
				PromptTokens:     u.PromptTokens,
				CompletionTokens: u.CompletionTokens,
				TotalTokens:      u.TotalTokens,
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// streamChat SSE 分支。请求校验错误在切换响应头之前已处理；
// 流中途的错误以 {"type":"error"} 事件下发（头已发出，无法再改状态码）。
func (h *Handler) streamChat(c *gin.Context, req *ChatRequest) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(c.Writer)
	flusher, _ := c.Writer.(http.Flusher)

	sink := func(ev chat.StreamEvent) error {
		if _, err := io.WriteString(c.Writer, "data: "); err != nil {
			return err
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
		// Encode 自带换行；SSE 事件之间需要空行分隔
		if _, err := io.WriteString(c.Writer, "\n"); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	opts := llm.GenerateOptions{
		ModelKey:        req.Model,
		GenerationParams: req.GenerationParams,
	}
	_ = h.svc.Stream(c.Request.Context(), req.SessionID, opts, req.ToUserMessage(), sink)
	// 客户端断开（sink 出错）或上游失败都已通过 error 事件告知，此处直接结束响应
}
