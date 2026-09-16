package llm

import "context"

// GenerationParams 用户在前端选择的多模态生成参数。
//
// 所有字段可选；前端只传用户显式选过的。
// 通过 ctx 透传到工具层（不进 LLM 提示词），由工具层直接喂到生成 API。
//
// 字段语义与 GenerateRequest 一一对应。
type ReferenceImage struct {
	URL      string  `json:"url"`
	Role     string  `json:"role,omitempty"`   // first_frame/last_frame/style/subject
	Weight   float64 `json:"weight,omitempty"` // 0-1,img2img 用
	UploadID string  `json:"upload_id,omitempty"`
}

type GenerationParams struct {
	AspectRatio     string           `json:"aspect_ratio,omitempty"`
	GenerationType  string           `json:"generation_type,omitempty"`
	NegativePrompt  string           `json:"negative_prompt,omitempty"`
	Size            string           `json:"size,omitempty"`
	Resolution      string           `json:"resolution,omitempty"`
	Width           int              `json:"width,omitempty"`
	Height          int              `json:"height,omitempty"`
	Duration        int              `json:"duration,omitempty"`
	FPS             int              `json:"fps,omitempty"`
	N               int              `json:"n,omitempty"`
	Seed            int              `json:"seed,omitempty"`
	CameraMotion    string           `json:"camera_motion,omitempty"`
	ReferenceImages []ReferenceImage `json:"reference_images,omitempty"`
	Watermark       *bool            `json:"watermark,omitempty"`
}

// GenerateOptions 一次 chat 请求里的生成选项。
//
// ModelKey 进 LLM 提示词（让 dispatcher 知道用哪个模型）；
// GenerationParams 通过 ctx 透传到工具层（不进 LLM 提示词）。
type GenerateOptions struct {
	ModelKey         string
	GenerationParams *GenerationParams
}

// optionsCtxKey ctx key,避免与其他包冲突。
type optionsCtxKey struct{}

// WithGenerateOptions 把 GenerateOptions 注入 ctx,供工具层还原。
func WithGenerateOptions(ctx context.Context, opts GenerateOptions) context.Context {
	return context.WithValue(ctx, optionsCtxKey{}, opts)
}

// GenerateOptionsFromContext 从 ctx 还原 GenerateOptions;不存在返回零值。
func GenerateOptionsFromContext(ctx context.Context) (GenerateOptions, bool) {
	opts, ok := ctx.Value(optionsCtxKey{}).(GenerateOptions)
	return opts, ok
}
