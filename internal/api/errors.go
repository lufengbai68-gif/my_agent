package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/session"
)

// 错误码约定：{"error":{"code":"...","message":"..."}}
//   400 invalid_request  | 404 session_not_found / agent_not_found
//   413 request_too_large | 502 upstream_error | 500 internal_error
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// fail 按状态码 + 错误码输出错误响应。
func fail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// failErr 将内部错误映射为 HTTP 错误响应。
func failErr(c *gin.Context, err error) {
	var mbErr *http.MaxBytesError
	switch {
	case errors.Is(err, agent.ErrAgentNotFound):
		fail(c, http.StatusNotFound, "agent_not_found", err.Error())
	case errors.Is(err, session.ErrNotFound):
		fail(c, http.StatusNotFound, "session_not_found", err.Error())
	case errors.As(err, &mbErr):
		fail(c, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds limit")
	default:
		// 模型/供应商调用失败等一切上游错误
		fail(c, http.StatusBadGateway, "upstream_error", err.Error())
	}
}
