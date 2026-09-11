package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"

	"github.com/lufengbai68-gif/my_agent/internal/config"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

// Build 按配置构建全部 agent 并装入 Registry。
//
// 这是功能演进的唯一挂载点：
//   - V2 工具：读取 AgentConfig.Tools，填入 adk.ToolsConfig
//   - V3 编排：将子 agent 用 adk.NewAgentTool 包成工具挂到主管 agent
func Build(ctx context.Context, cfg *config.Root, models *llm.Registry) (*Registry, error) {
	reg := NewRegistry(cfg.Agents.Default)

	for name, ac := range cfg.Agents.Definitions {
		m, err := models.Get(ac.Model)
		if err != nil {
			return nil, fmt.Errorf("agent %q: %w", name, err)
		}

		a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        name,
			Description: ac.Description,
			Instruction: ac.Instruction,
			Model:       m,
			// V2: ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: [...]}}
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
