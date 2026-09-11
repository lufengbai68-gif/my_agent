// Package agent 管理 agent 的注册与查找。
// V2（工具）/ V3（多 agent 编排）只需改动 build.go，
// 对外暴露的 Runner 接口保持不变。
package agent

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// ErrAgentNotFound 请求的 agent 不存在。
var ErrAgentNotFound = errors.New("agent not found")

// Runner 是 chat.Service 依赖的运行接口——包一层 adk.Runner，
// 使服务层与测试都可注入替身。
type Runner interface {
	Run(ctx context.Context, msgs []adk.Message, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent]
}

// adkRunner 将 adk.Runner 适配为本包 Runner 接口。
type adkRunner struct{ r *adk.Runner }

func (a adkRunner) Run(ctx context.Context, msgs []adk.Message, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.r.Run(ctx, msgs, opts...)
}

// Registry name → Runner 的注册表，附带默认 agent 解析。
type Registry struct {
	mu           sync.RWMutex
	defaultAgent string
	runners      map[string]Runner
}

// NewRegistry 创建注册表；defaultAgent 为空名请求的兜底。
func NewRegistry(defaultAgent string) *Registry {
	return &Registry{
		defaultAgent: defaultAgent,
		runners:      make(map[string]Runner),
	}
}

// Register 注册 agent（内部包一层 adk.Runner，EnableStreaming: true，
// 同步与 SSE 共用同一 runner）。同名重复注册报错以便尽早暴露配置错误。
func (r *Registry) Register(name string, a adk.Agent) error {
	if name == "" {
		return errors.New("agent name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.runners[name]; dup {
		return errors.New("duplicate agent name: " + name)
	}
	r.runners[name] = adkRunner{r: adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent:           a,
		EnableStreaming: true,
	})}
	return nil
}

// RegisterRunner 直接注册 Runner（供测试/编排组合 agent 使用）。
func (r *Registry) RegisterRunner(name string, run Runner) error {
	if name == "" {
		return errors.New("agent name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.runners[name]; dup {
		return errors.New("duplicate agent name: " + name)
	}
	r.runners[name] = run
	return nil
}

// Runner 取 agent 的 runner；name 为空时返回默认 agent。
func (r *Registry) Runner(name string) (Runner, error) {
	_, run, err := r.Resolve(name)
	return run, err
}

// Resolve 解析 agent 名（空名 → 默认 agent）并返回名字与 runner。
// 名字用于响应回显，保证客户端拿到实际生效的 agent。
func (r *Registry) Resolve(name string) (string, Runner, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name == "" {
		name = r.defaultAgent
	}
	run, ok := r.runners[name]
	if !ok {
		return "", nil, ErrAgentNotFound
	}
	return name, run, nil
}

// Names 已注册的 agent 名（有序）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.runners))
	for name := range r.runners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
