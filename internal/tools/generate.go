// Package tools 的当前形态：
//
// 历史：曾注册 get_current_time 工具。
// 重构后：
//   - 生成能力（图片/视频）下沉到 internal/llm 包
//   - 本包提供"通用生成工具"——主 agent dispatcher 通过它们
//     调用具体的 generation model，按 user-selected model_key 路由
//
// 暴露给主 agent 的工具接口是固定的：
//   - generate_image(model_key, prompt, ...)
//   - generate_video(model_key, prompt, ...)
//
// 实现按 model_key 查找 internal/llm.Registry 中的 GenerationModel 并执行。
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

// GenerateTool 通用生成工具（按 kind 区分 image / video）。
type GenerateTool struct {
	kind   string // "image" | "video"
	models *llm.Registry
}

// NewImageGenerator 构造图片生成工具。
func NewImageGenerator(models *llm.Registry) tool.InvokableTool {
	return &GenerateTool{kind: "image", models: models}
}

// NewVideoGenerator 构造视频生成工具。
func NewVideoGenerator(models *llm.Registry) tool.InvokableTool {
	return &GenerateTool{kind: "video", models: models}
}

// generateInput 工具入参（LLM 看到的就是这套字段）。
//
// LLM 只负责决定"调哪个工具 + 用什么 prompt":
//   - model_key: 用户选的生成模型（来自 dispatcher hint）
//   - prompt: 精炼后的英文提示词
//
// 其他多模态参数（比例/分辨率/时长/数量/种子等）由前端 UI 通过 ctx 透传，
// LLM 不必传、也不必读——工具层会自动合并。
type generateInput struct {
	ModelKey string `json:"model_key" jsonschema:"description=用户选的生成模型 key（如 seedance_pro / seedream），决定路由到哪个具体生成器"`
	Prompt   string `json:"prompt" jsonschema:"description=精炼后的生成提示词（英文为佳，可翻译用户原文）"`
}

// generateOutput 工具出参。
//
// 注意：Kind 字段是 chat.Service 用来提取媒体 URL 的关键标记。
// 服务层从 Tool 角色消息的 Content JSON 里读 urls + kind，组装成 MediaItem 列表
// 作为响应里的独立 media 字段发给前端。
type generateOutput struct {
	URLs  []string          `json:"urls"`
	Kind  string            `json:"kind"` // image | video（与 g.kind 一致）
	Usage *llm.GenerateUsage `json:"usage,omitempty"`
	Model string            `json:"model"` // 实际生效的模型 key
}

// Info 实现 eino InvokableTool。
func (g *GenerateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	name := fmt.Sprintf("generate_%s", g.kind)
	desc := fmt.Sprintf("Generate a %s using a user-selected generation model. Provide model_key (from /v1/models) and a detailed prompt.", g.kind)
	tl, err := utils.InferTool(name, desc, g.run)
	if err != nil {
		return nil, err
	}
	return tl.Info(ctx)
}

// InvokableRun 实现 InvokableTool。
func (g *GenerateTool) InvokableRun(ctx context.Context, argsInJSON string, opts ...tool.Option) (string, error) {
	tl, err := utils.InferTool(
		fmt.Sprintf("generate_%s", g.kind),
		"placeholder",
		g.run,
	)
	if err != nil {
		return "", err
	}
	return tl.InvokableRun(ctx, argsInJSON, opts...)
}

// run 实际的工具函数（被 utils.InferTool 包装）。
//
// 参数来源策略（重要）：
//   - model_key + prompt: 来自 LLM tool call（dispatcher 决定调哪个模型、用什么提示词）
//   - 其他多模态参数(比例/分辨率/时长等): 来自 chatOpts(ctx 透传的前端 UI 选择)
//     这样 LLM 提示词保持干净，只看 user message + model_key。
//     用户在前端选的参数不被 LLM 重新解释,直接喂到生成 API。
//   - 都没有的字段: applyDefaults 兜底
func (g *GenerateTool) run(ctx context.Context, input generateInput) (out generateOutput, err error) {
	start := time.Now()
	slog.InfoContext(ctx, "tool_invoked",
		"tool", "generate_"+g.kind,
		"model_key", input.ModelKey,
		"prompt_len", len(input.Prompt),
	)
	defer func() {
		urls := out.URLs
		slog.InfoContext(ctx, "tool_done",
			"tool", "generate_"+g.kind,
			"model_key", input.ModelKey,
			"dur", time.Since(start),
			"output_count", len(urls),
			"err", errString(err),
		)
	}()

	if input.ModelKey == "" {
		return generateOutput{}, fmt.Errorf("model_key is required")
	}
	if input.Prompt == "" {
		return generateOutput{}, fmt.Errorf("prompt is required")
	}

	// 路由：按 kind 找对应 generation model
	m, err := g.models.GetGeneration(input.ModelKey)
	if err != nil {
		return generateOutput{}, fmt.Errorf("model %q not found: %w", input.ModelKey, err)
	}
	if !strings.EqualFold(m.Kind(), g.kind) {
		return generateOutput{}, fmt.Errorf("model %q is %s, not %s", input.ModelKey, m.Kind(), g.kind)
	}

	// prompt 来自 LLM;多模态参数来自 ctx 中的 chatOpts(前端 UI 选择)
	chatOpts, _ := llm.GenerateOptionsFromContext(ctx)
	req := llm.GenerateRequest{
		Prompt: input.Prompt,
	}
	applyChatOpts(&req, chatOpts.GenerationParams)
	applyDefaults(&req, g.kind)
	result, err := m.Generate(ctx, req)
	if err != nil {
		return generateOutput{}, fmt.Errorf("generate failed: %w", err)
	}

	return generateOutput{
		URLs:  result.URLs,
		Kind:  g.kind,
		Usage: result.Usage,
		Model: input.ModelKey,
	}, nil
}

// applyChatOpts 把前端 UI 选择的 GenerationParams 字段填到 req。
//
// 只填零值字段,不影响已设置的值(LLM 输入或上游默认)。
// chatOpts.GenerationParams 为 nil 时整段跳过。
func applyChatOpts(req *llm.GenerateRequest, gp *llm.GenerationParams) {
	if gp == nil {
		return
	}
	if req.NegativePrompt == "" {
		req.NegativePrompt = gp.NegativePrompt
	}
	if req.Size == "" {
		req.Size = gp.Size
	}
	if req.AspectRatio == "" {
		req.AspectRatio = gp.AspectRatio
	}
	if req.Width == 0 {
		req.Width = gp.Width
	}
	if req.Height == 0 {
		req.Height = gp.Height
	}
	if req.Resolution == "" {
		req.Resolution = gp.Resolution
	}
	if req.FPS == 0 {
		req.FPS = gp.FPS
	}
	if req.Duration == 0 {
		req.Duration = gp.Duration
	}
	if req.N == 0 {
		req.N = gp.N
	}
	if req.Seed == 0 {
		req.Seed = gp.Seed
	}
	if req.CameraMotion == "" {
		req.CameraMotion = gp.CameraMotion
	}
	if req.Watermark == nil {
		req.Watermark = gp.Watermark
	}
	if len(req.ReferenceImages) == 0 {
		req.ReferenceImages = gp.ReferenceImages
	}
}

// 默认尺寸:仅在零值时填充,不覆盖前端显式传入的值。
//
// 设计原则:
//   - 只填"通用且不会让用户意外"的默认值
//   - 用户显式给的值(包括奇怪值)原样保留
//   - 不填 watermark / seed / negative_prompt:
//     这些是有"用户偏好"语义的,填默认值会强制覆盖用户意图
var (
	DefaultImageAspectRatio = "1:1"
	DefaultVideoAspectRatio = "16:9"
	DefaultVideoResolution  = "720p"
	DefaultVideoDuration    = 5  // 秒
	DefaultVideoFPS         = 24
)

// applyDefaults 按 kind 填默认多模态参数。
//
//   - image: aspect_ratio 默认 1:1
//   - video: aspect_ratio 默认 16:9,resolution 默认 720p,duration 默认 5s,fps 默认 24
//
// 其余字段(N / Seed / Watermark / NegativePrompt / CameraMotion)不填默认值。
func applyDefaults(req *llm.GenerateRequest, kind string) {
	switch kind {
	case "image":
		if req.AspectRatio == "" {
			req.AspectRatio = DefaultImageAspectRatio
		}
	case "video":
		if req.AspectRatio == "" {
			req.AspectRatio = DefaultVideoAspectRatio
		}
		if req.Resolution == "" {
			req.Resolution = DefaultVideoResolution
		}
		if req.Duration == 0 {
			req.Duration = DefaultVideoDuration
		}
		if req.FPS == 0 {
			req.FPS = DefaultVideoFPS
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
