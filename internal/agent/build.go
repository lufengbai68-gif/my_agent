package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/lufengbai68-gif/my_agent/internal/config"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

// Build 按配置构建全部 agent 并装入 Registry。
//
// 这是功能演进的唯一挂载点：
//   - 当前：主 agent (dispatcher) 接收调用方传入的工具列表
//   - V3 编排：将子 agent 用 adk.NewAgentTool 包成工具挂到主管 agent
//
// 工具列表由 main.go 装配传入,避免 agent 反向依赖 tools 包(打破导入循环,
// 因为 tools→chat→agent,tools 不能同时被 agent 引用)。
func Build(ctx context.Context, cfg *config.Root, models *llm.Registry, agentTools []tool.BaseTool) (*Registry, error) {
	reg := NewRegistry(cfg.Agents.Default)

	for name, ac := range cfg.Agents.Definitions {
		m, err := models.GetChat(ac.Model)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", name, err)
		}

		a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        name,
			Description: ac.Description,
			Instruction: ac.Instruction,
			Model:       m,
			ToolsConfig: adk.ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: agentTools,
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("build agent %q: %w", name, err)
		}
		if err := reg.Register(name, a); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
