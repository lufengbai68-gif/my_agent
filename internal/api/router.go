package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/config"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
)

// Handler 汇集 HTTP 处理器的依赖。
type Handler struct {
	svc         *chat.Service
	cfg         *config.Root
	uploads     uploads.Uploader     // OSS 上传器, nil 表示 uploads 未配置
	uploadStore uploads.UploadStore  // upload 元数据索引(支持级联删), nil 表示不跟踪
}

// NewRouter 构建 gin 引擎与路由表。
//
// uploads 可以为 nil, 表示 uploads 端点会返回 503。
// uploadStore 可为 nil, 表示 upload 不参与 session 级联清理(匿名上传)。
func NewRouter(cfg *config.Root, svc *chat.Service, up uploads.Uploader, uploadStore uploads.UploadStore) *gin.Engine {
	gin.SetMode(cfg.Server.Mode)

	h := &Handler{svc: svc, cfg: cfg, uploads: up, uploadStore: uploadStore}

	r := gin.New()

	// middleware 顺序:
	//   1. RequestID()   生成/透传 X-Request-Id,塞 ctx,后续所有 slog 自动带 rid
	//   2. AccessLog()   我们的 access log(JSON,带 rid / error_code)
	//   3. gin.Recovery  panic 兜底
	// 注: 故意去掉 gin.Logger() —— 我们的 AccessLog() 已覆盖,且输出更结构化
	r.Use(RequestID(), AccessLog(), gin.Recovery())
	r.MaxMultipartMemory = cfg.Server.MaxBodySize

	r.Use(func(c *gin.Context) {
		if cfg.Server.MaxBodySize > 0 {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.Server.MaxBodySize)
		}
		c.Next()
	})

	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", h.handleChat)
		v1.GET("/sessions", h.handleListSessions)
		v1.GET("/sessions/:id", h.handleGetSession)
		v1.GET("/sessions/:id/uploads", h.handleListSessionUploads)
		v1.DELETE("/sessions/:id", h.handleDeleteSession)
		v1.GET("/models", h.handleListModels) // ← 新增

		// 上传接口: 文件存到对象存储,返回可被上游生成 API 直接 fetch 的 URL
		v1.POST("/uploads", h.uploadHandler)
		v1.GET("/uploads/:id/meta", h.metaUploadHandler)
		v1.DELETE("/uploads/:id", h.deleteUploadHandler)
	}
	r.GET("/healthz", h.handleHealthz)

	return r
}
