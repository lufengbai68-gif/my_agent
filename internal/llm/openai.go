package llm

import (
	"context"

	openaillm "github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// RegisterBuiltins 注册内置供应商工厂。
// 新供应商在此追加一行（工厂函数放同包新文件）。
func RegisterBuiltins(r *Registry) {
	// Chat models（对话用 LLM）
	r.RegisterChatFactory("chat", "openai", newOpenAI)

	// Generation models（图片/视频生成）
	r.RegisterGenerationFactory("image", "openai", newOpenAIImage)
	r.RegisterGenerationFactory("image", "ark", newArkImage) // 火山方舟原生图片 API
	r.RegisterGenerationFactory("video", "ark", newSeedanceVideo)
}

// newOpenAI 构建 OpenAI 兼容模型。
// 通过 BaseURL 指向任意兼容端点：OpenAI / DeepSeek / Qwen / vLLM 等。
func newOpenAI(ctx context.Context, cfg *config.ModelConfig) (ChatModel, error) {
	return openaillm.NewChatModel(ctx, &openaillm.ChatModelConfig{
		APIKey:      cfg.APIKey,
		BaseURL:     cfg.BaseURL,
		Model:       cfg.Model,
		Timeout:     cfg.Timeout,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
}
