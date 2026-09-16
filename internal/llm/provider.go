// Package llm 提供模型工厂的注册与机制。
//
// 拆分成两条独立的工厂线：
//   - ChatFactory：构建对话模型（返回 eino ChatModel 接口）
//   - GenerationFactory：构建生成模型（图片/视频，返回 GenerationModel 接口）
//
// 新增供应商 = 在 builtin 包加一个工厂 + RegisterBuiltins 注册一行。
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

// ChatModel 本系统统一使用的对话模型接口（eino 基础接口）。
type ChatModel = model.BaseModel[*schema.Message]

// GenerationModel 统一抽象的图片/视频生成模型接口。
//
// 与 ChatModel 不同：返回的是结构化结果（URL、usage），不是消息。
type GenerationModel interface {
	// Generate 提交生成请求（必要时包含异步轮询），返回最终结果。
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error)

	// Kind 返回模型类型（image | video | audio），用于工具路由。
	Kind() string
}

// GenerateRequest 多模态生成请求的统一结构。
//
// 字段按"用户意图"维度组织（与上游 API 字段脱钩），由各 factory 自行映射。
//
//   - Prompt / NegativePrompt：正反向提示词
//   - Size：自由格式尺寸（图片 "1024x1024"/"2K"、视频 "720p"/"1080p"），最优先
//   - AspectRatio：画面比例 "1:1" / "16:9" / "9:16" / "4:3" / "3:4" / "3:2" / "2:3" / "21:9"
//   - Width/Height：派生宽高，兜底用
//   - Resolution：视频专属（"720p" / "1080p"）
//   - FPS：视频帧率
//   - Duration：视频时长（秒）
//   - N：图片生成数量（默认 1）
//   - Seed：随机种子（0/负数 = 不传）
//   - CameraMotion：相机运动偏好（"static"/"pan_left"/"zoom_in"...）
//   - Watermark：是否带水印（nil = 用模型默认）
//   - Extra：上游私有参数透传通道
//
// 图片字段优先级：Size > AspectRatio > Width/Height > 默认
// 视频字段优先级：Size / Resolution / AspectRatio 任选其一；Duration/FPS 可选
type GenerateRequest struct {
	Prompt          string
	NegativePrompt  string
	Size            string
	AspectRatio     string
	Width           int
	Height          int
	Resolution      string
	FPS             int
	Duration        int
	N               int
	Seed            int
	CameraMotion    string
	ReferenceImages []ReferenceImage    // 多图参考(优先生效)
	Watermark       *bool
	Extra           map[string]any
}

// GenerateResult 生成结果。
type GenerateResult struct {
	URLs   []string         // 生成产物 URL（图片通常 1 张，视频 1 个）
	LocalPaths []string      // 若下载到本地，本地路径
	Usage  *GenerateUsage   // 用量信息
	Raw    map[string]any   // 透传原始响应，便于调试
}

// GenerateUsage 用量。
type GenerateUsage struct {
	Frames   int    // 视频总帧数
	Duration string // 视频时长描述
	Ratio    string // 画面比例
	Cost     float64
}

// ChatFactory 按配置构建一个 ChatModel 实例。
type ChatFactory func(ctx context.Context, cfg *config.ModelConfig) (ChatModel, error)

// GenerationFactory 按配置构建一个 GenerationModel 实例。
type GenerationFactory func(ctx context.Context, cfg *config.ModelConfig) (GenerationModel, error)

// Registry 模型工厂与命名实例的注册表。
//
// 工厂按 (type, provider) 嵌套注册：type 表能力（chat/image/video/audio），
// provider 表协议实现（openai / ark / ...）。
type Registry struct {
	mu sync.RWMutex

	// 双层 map：[type][provider] → Factory
	chatFactories       map[string]map[string]ChatFactory
	generationFactories map[string]map[string]GenerationFactory

	chatBuilt           map[string]ChatModel
	generationBuilt     map[string]GenerationModel
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{
		chatFactories:       make(map[string]map[string]ChatFactory),
		generationFactories: make(map[string]map[string]GenerationFactory),
		chatBuilt:           make(map[string]ChatModel),
		generationBuilt:     make(map[string]GenerationModel),
	}
}

// RegisterChatFactory 注册对话模型工厂。
//
//   - kind: 能力类别，必须是 "chat"
//   - provider: 协议实现（openai / ark / ...）
func (r *Registry) RegisterChatFactory(kind, provider string, f ChatFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chatFactories[kind] == nil {
		r.chatFactories[kind] = make(map[string]ChatFactory)
	}
	r.chatFactories[kind][provider] = f
}

// RegisterGenerationFactory 注册生成模型工厂。
//
//   - kind: 能力类别（image / video / audio）
//   - provider: 协议实现（openai / ark / ...）
func (r *Registry) RegisterGenerationFactory(kind, provider string, f GenerationFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.generationFactories[kind] == nil {
		r.generationFactories[kind] = make(map[string]GenerationFactory)
	}
	r.generationFactories[kind][provider] = f
}

// BuildAll 按配置构建全部命名模型实例。
//
// 每个 ModelConfig 按 ModelType 路由到对应工厂：
//   - "chat"   → ChatFactory
//   - 其他      → GenerationFactory
func (r *Registry) BuildAll(ctx context.Context, cfgs map[string]*config.ModelConfig) error {
	names := make([]string, 0, len(cfgs))
	for name := range cfgs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := cfgs[name]
		kind := cfg.Type
		if kind == "" {
			kind = cfg.ModelType // 兼容旧字段
		}
		switch kind {
		case "chat":
			providers, ok := r.chatFactories[kind]
			if !ok {
				return fmt.Errorf("model %q: unknown chat kind %q", name, kind)
			}
			f, ok := providers[cfg.Provider]
			if !ok {
				return fmt.Errorf("model %q: unknown chat provider %q for kind %q", name, cfg.Provider, kind)
			}
			m, err := f(ctx, cfg)
			if err != nil {
				return fmt.Errorf("build chat model %q: %w", name, err)
			}
			r.mu.Lock()
			r.chatBuilt[name] = m
			r.mu.Unlock()
		case "image", "video", "audio":
			providers, ok := r.generationFactories[kind]
			if !ok {
				return fmt.Errorf("model %q: unknown generation kind %q", name, kind)
			}
			f, ok := providers[cfg.Provider]
			if !ok {
				return fmt.Errorf("model %q: unknown generation provider %q for kind %q", name, cfg.Provider, kind)
			}
			m, err := f(ctx, cfg)
			if err != nil {
				return fmt.Errorf("build generation model %q: %w", name, err)
			}
			r.mu.Lock()
			r.generationBuilt[name] = m
			r.mu.Unlock()
		default:
			return fmt.Errorf("model %q: invalid kind %q (supported: chat|image|video|audio)", name, kind)
		}
	}
	return nil
}

// GetChat 取命名 chat 模型实例。
func (r *Registry) GetChat(name string) (ChatModel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.chatBuilt[name]
	if !ok {
		return nil, fmt.Errorf("chat model %q not found", name)
	}
	return m, nil
}

// GetGeneration 取命名 generation 模型实例。
func (r *Registry) GetGeneration(name string) (GenerationModel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.generationBuilt[name]
	if !ok {
		return nil, fmt.Errorf("generation model %q not found", name)
	}
	return m, nil
}

// ChatNames 已构建的 chat 模型实例名（有序）。
func (r *Registry) ChatNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.chatBuilt))
	for name := range r.chatBuilt {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GenerationNames 已构建的 generation 模型实例名（有序）。
func (r *Registry) GenerationNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.generationBuilt))
	for name := range r.generationBuilt {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
