// my_agent —— 基于 cloudwego/eino 的多模态多 Agent 系统后端。
//
// 装配流程：config → llm 注册表 → agent 构建 → session 存储 → chat 服务 → HTTP。
// 依赖方向严格单向：api → chat → {session, agent} → {llm, config}。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/api"
	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/config"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
	"github.com/lufengbai68-gif/my_agent/internal/session"
	"github.com/lufengbai68-gif/my_agent/internal/session/memory"
)

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. 模型实例（工厂注册表：新供应商加一个工厂文件即可）
	models := llm.NewRegistry()
	llm.RegisterBuiltins(models)
	if err := models.BuildAll(ctx, cfg.Models); err != nil {
		log.Fatalf("build models: %v", err)
	}

	// 2. agent（V2 工具 / V3 编排只改 agent.Build）
	agents, err := agent.Build(ctx, cfg, models)
	if err != nil {
		log.Fatalf("build agents: %v", err)
	}

	// 3. 会话存储（V4：按 cfg.Sessions.Store 换 Redis 实现）
	var store session.Store
	switch cfg.Sessions.Store {
	case "memory":
		store = memory.New()
	default:
		log.Fatalf("unsupported session store: %s", cfg.Sessions.Store)
	}

	// 4. 服务与路由
	svc := chat.New(store, agents, cfg.Sessions.MaxMessages)
	router := api.NewRouter(cfg, svc, agents)

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		WriteTimeout:      0, // SSE 长连接，不设写超时
	}

	go func() {
		log.Printf("my_agent listening on %s (agents: %v)", cfg.Server.Addr, agents.Names())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	// 5. 优雅停机：等待信号，给在途请求（含 SSE）10 秒收尾
	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	log.Println("bye")
}
