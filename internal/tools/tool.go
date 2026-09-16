// Package tools 提供 agent 可调用的工具集合。
//
// 核心抽象：
//   - Factory：按配置构建工具实例的工厂函数（按 type 路由）
//   - Registry：工具工厂与命名实例的注册表
//   - RegisterBuiltins()：内置工厂注册入口
//
// 工具被注入到 adk.ToolsConfig.ToolsNodeConfig.Tools，agent 自主决定何时调用。
package tools

import (
	"context"

	"github.com/cloudwego/eino/components/tool"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// Factory 按配置构建一个工具实例。type 字段路由到具体工厂。
type Factory func(ctx context.Context, cfg *config.ToolConfig) (tool.InvokableTool, error)

// Registry 工具工厂与命名实例的注册表。
type Registry struct {
	factories map[string]Factory
	instances map[string]tool.InvokableTool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
		instances: make(map[string]tool.InvokableTool),
	}
}

// RegisterFactory 注册工具工厂（按 yaml 的 type 字段路由）。
func (r *Registry) RegisterFactory(kind string, f Factory) {
	r.factories[kind] = f
}

// BuildAll 按配置构建全部命名工具实例（启动期调用一次）。
func (r *Registry) BuildAll(ctx context.Context, cfgs map[string]*config.ToolConfig) error {
	// 按 key 排序，保证报错顺序稳定
	names := make([]string, 0, len(cfgs))
	for name := range cfgs {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		cfg := cfgs[name]
		f, ok := r.factories[cfg.Type]
		if !ok {
			return &UnknownToolKindError{Kind: cfg.Type, Name: name}
		}
		t, err := f(ctx, cfg)
		if err != nil {
			return &BuildToolError{Name: name, Err: err}
		}
		r.instances[name] = t
	}
	return nil
}

// Names 已构建的工具名（有序）。
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.instances))
	for name := range r.instances {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// Get 取出已构建的工具实例。
func (r *Registry) Get(name string) (tool.InvokableTool, error) {
	t, ok := r.instances[name]
	if !ok {
		return nil, &ToolNotFoundError{Name: name}
	}
	return t, nil
}

// AllInstances 返回全部已构建工具（agent 启动时一次注入）。
func (r *Registry) AllInstances() []tool.BaseTool {
	out := make([]tool.BaseTool, 0, len(r.instances))
	for _, t := range r.instances {
		out = append(out, t)
	}
	return out
}

// sortStrings 插入排序，避开引入 sort 包。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// --- 错误类型 ---

type UnknownToolKindError struct {
	Kind string
	Name string
}

func (e *UnknownToolKindError) Error() string {
	return "tool " + e.Name + ": unknown tool kind " + e.Kind
}

type BuildToolError struct {
	Name string
	Err  error
}

func (e *BuildToolError) Error() string {
	return "build tool " + e.Name + ": " + e.Err.Error()
}

func (e *BuildToolError) Unwrap() error { return e.Err }

type ToolNotFoundError struct{ Name string }

func (e *ToolNotFoundError) Error() string {
	return "tool " + e.Name + " not found"
}
