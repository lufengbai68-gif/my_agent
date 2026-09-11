// Package llm 提供模型供应商的工厂注册表。
// 新增供应商 = 新增一个工厂文件 + RegisterBuiltins 里注册，其余代码不动。
package llm

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// ChatModel 本系统统一使用的模型接口（eino 基础接口，*schema.Message 形态）。
type ChatModel = model.BaseModel[*schema.Message]

// Factory 按配置构建一个模型实例。type 字段路由到具体工厂。
type Factory func(ctx context.Context, cfg *config.ModelConfig) (ChatModel, error)

// Registry 模型工厂与命名实例的注册表。
// 命名实例让多个 agent 复用同一份配置（如 main + cheap 同 type 不同 model）。
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
	built     map[string]ChatModel
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
		built:     make(map[string]ChatModel),
	}
}

// RegisterFactory 注册一个供应商工厂（按 config 的 type 字段路由）。
func (r *Registry) RegisterFactory(providerType string, f Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[providerType] = f
}

// BuildAll 按配置构建全部命名模型实例（启动期调用一次）。
func (r *Registry) BuildAll(ctx context.Context, cfgs map[string]*config.ModelConfig) error {
	// 按 key 排序，保证报错顺序稳定
	names := make([]string, 0, len(cfgs))
	for name := range cfgs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := cfgs[name]
		f, ok := r.factories[cfg.Type]
		if !ok {
			return fmt.Errorf("model %q: unknown provider type %q", name, cfg.Type)
		}
		m, err := f(ctx, cfg)
		if err != nil {
			return fmt.Errorf("build model %q: %w", name, err)
		}
		r.mu.Lock()
		r.built[name] = m
		r.mu.Unlock()
	}
	return nil
}

// Get 取命名模型实例；未知名称报错。
func (r *Registry) Get(name string) (ChatModel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.built[name]
	if !ok {
		return nil, fmt.Errorf("model instance %q not found", name)
	}
	return m, nil
}

// Names 已构建的模型实例名（有序）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.built))
	for name := range r.built {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
