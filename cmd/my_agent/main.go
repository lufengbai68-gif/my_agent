// my_agent —— 基于 cloudwego/eino 的多模态多 Agent 系统后端。
//
// 装配流程：config → logging → llm 注册表 → agent 构建 → session 存储 → chat 服务 → HTTP。
// 依赖方向严格单向：api → chat → {session, agent} → {llm, config}。
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/api"
	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/config"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
	"github.com/lufengbai68-gif/my_agent/internal/logging"
	"github.com/lufengbai68-gif/my_agent/internal/session"
	"github.com/lufengbai68-gif/my_agent/internal/session/memory"
	"github.com/lufengbai68-gif/my_agent/internal/tools"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
	memoryupload "github.com/lufengbai68-gif/my_agent/internal/uploads/memory"
)

func main() {
	// 默认配置加载顺序(显式 -config 参数 优先):
	//   1. -config 显式指定
	//   2. configs/local.yaml(本地覆盖,gitignored,放密钥)
	//   3. configs/config.yaml(默认模板,提交进 git)
	cfgPath := flag.String("config", "", "config file path (empty = auto-detect local.yaml then config.yaml)")
	flag.Parse()
	resolved := resolveConfigPath(*cfgPath)
	cfgPath = &resolved

	cfg, err := config.Load(resolved)
	if err != nil {
		slog.Error("load config failed", "path", *cfgPath, "err", err)
		os.Exit(1)
	}

	// 日志系统(必须在任何业务日志之前初始化)
	logging.Init(logging.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
		Redact: cfg.Logging.Redact,
	})
	slog.Info("config_loaded",
		"path", *cfgPath,
		"logging_level", cfg.Logging.Level,
		"logging_format", cfg.Logging.Format,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. 模型实例（工厂注册表：新供应商加一个工厂文件即可）
	models := llm.NewRegistry()
	llm.RegisterBuiltins(models)
	if err := models.BuildAll(ctx, cfg.Models); err != nil {
		slog.Error("build models failed", "err", err)
		os.Exit(1)
	}

	// 2. agent（V2 工具 / V3 编排只改 agent.Build）
	// 通用生成工具: 主 agent dispatcher 用它们路由到具体的 generation model
	agentTools := []tool.BaseTool{
		tools.NewImageGenerator(models),
		tools.NewVideoGenerator(models),
	}
	agents, err := agent.Build(ctx, cfg, models, agentTools)
	if err != nil {
		slog.Error("build agents failed", "err", err)
		os.Exit(1)
	}

	// 3. 会话存储（V4：按 cfg.Sessions.Store 换 Redis 实现）
	var store session.Store
	switch cfg.Sessions.Store {
	case "memory":
		store = memory.New()
	default:
		slog.Error("unsupported session store", "store", cfg.Sessions.Store)
		os.Exit(1)
	}

	// 4. 上传器(可选;配置缺失/OSS 不可达就 disable)
	// 同时配 uploadStore 元数据索引,支持按 session 级联清理
	// uploads 装配(fail-closed):
	//   - cfg.Uploads.Store == "" 表示用户没启用 uploads,跳过(upload 端点将返回 503)
	//   - store 非空 → 必须初始化成功;失败则进程退出,不允许"假装启动"
	var (
		uploader    uploads.Uploader
		uploadStore uploads.UploadStore
	)
	if cfg.Uploads.Store != "" {
		uploader, err = uploads.NewFromConfig(ctx, &cfg.Uploads)
		if err != nil {
			slog.Error("uploads init failed", "store", cfg.Uploads.Store, "err", err)
			os.Exit(1)
		}
		uploadStore = memoryupload.New()
		slog.Info("uploads_enabled", "store", cfg.Uploads.Store)
	} else {
		slog.Info("uploads_disabled", "reason", "no uploads.store configured")
	}

	// 5. 服务与路由
	svc := chat.New(store, agents, cfg.Sessions.MaxMessages, cfg.Uploads.Domains, uploadStore, uploader)
	router := api.NewRouter(cfg, svc, uploader, uploadStore)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		WriteTimeout:      0, // SSE 长连接，不设写超时
	}

	go func() {
		slog.Info("server_starting",
			"addr", cfg.Server.Addr,
			"chat_models", models.ChatNames(),
			"gen_models", models.GenerationNames(),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	// 5. 优雅停机：等待信号，给在途请求（含 SSE）10 秒收尾
	<-ctx.Done()
	slog.Info("shutting_down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
	slog.Info("shutdown_complete")
}

func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	for _, p := range []string{"configs/local.yaml", "configs/config.yaml"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// 都没找到,让后续 config.Load 报错
	return "configs/config.yaml"
}
