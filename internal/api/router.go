package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// Handler 汇集 HTTP 处理器的依赖。
type Handler struct {
	svc        *chat.Service
	agentNames func() []string // 由 agent.Registry.Names 提供
}

// AgentLister 提供 agent 枚举（*agent.Registry 天然满足）。
type AgentLister interface {
	Names() []string
}

// NewRouter 构建 gin 引擎与路由表。
func NewRouter(cfg *config.Root, svc *chat.Service, agents AgentLister) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	h := &Handler{svc: svc, agentNames: agents.Names}
	if h.agentNames == nil {
		h.agentNames = func() []string { return nil }
	}

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.MaxMultipartMemory = cfg.Server.MaxBodySize

	// 请求体大小限制（多模态 base64 可能很大）
	r.Use(func(c *gin.Context) {
		if cfg.Server.MaxBodySize > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.Server.MaxBodySize)
		}
		c.Next()
	})

	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", h.handleChat)
		v1.GET("/sessions/:id", h.handleGetSession)
		v1.DELETE("/sessions/:id", h.handleDeleteSession)
		v1.GET("/agents", h.handleListAgents)
	}
	r.GET("/healthz", h.handleHealthz)

	return r
}
