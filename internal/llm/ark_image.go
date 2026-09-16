package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// arkImage 实现 GenerationModel 接口，调用火山方舟图片生成原生 API。
//
// 与 OpenAI Images API 的关键差异：
//   - model 字段用接入点 ID（ep-xxx），不是模型名
//   - size 支持 "2K" 等自定义字符串（不限于 1024x1024）
//   - 特有字段：sequential_image_generation、watermark
//
// POST {base_url}/images/generations {model, prompt, size, watermark, ...}
//
//	→ {created, data: [{url|b64_json}]}
type arkImage struct {
	cfg arkImageConfig
}

type arkImageConfig struct {
	APIKey     string
	BaseURL    string
	Model      string // 接入点 ID（ep-xxx）
	HTTPClient *http.Client

	// 可选方舟特有参数（nil 时使用默认值）
	SequentialImageGeneration string // "auto" | "disabled"
	Watermark                 *bool
}

func (c *arkImageConfig) validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("api_key is required")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if c.SequentialImageGeneration == "" {
		c.SequentialImageGeneration = "disabled"
	}
	return nil
}

// newArkImage GenerationFactory 入口。
func newArkImage(ctx context.Context, cfg *config.ModelConfig) (GenerationModel, error) {
	c := arkImageConfig{
		APIKey:                    cfg.APIKey,
		BaseURL:                   cfg.BaseURL,
		Model:                     cfg.Model,
		SequentialImageGeneration: paramString(cfg, "sequential_image_generation"),
		Watermark:                 paramBool(cfg, "watermark"),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &arkImage{cfg: c}, nil
}

// Kind 实现 GenerationModel。
func (a *arkImage) Kind() string { return "image" }

// arkImagesRequest 方舟图片生成请求体。
//
// 参考图字段语义:
//
//	image              单图编辑(image 字段,单图)
//	images[]           多图参考(走通用 i2i,无角色区分)
//	subject_reference  主体参考(角色/物体一致性)
//	style_reference    风格参考(画风/色调)
//
// 字段优先级: subject_reference / style_reference 命中时优先使用对应专用字段;
// 其余无 role 的 reference 走 images[]。
type arkImagesRequest struct {
	Model                     string   `json:"model"`
	Prompt                    string   `json:"prompt"`
	Image                     string   `json:"image,omitempty"`             // 单图编辑(image-edit 用)
	Images                    []string `json:"images,omitempty"`            // 多图参考(无 role 的 ReferenceImages)
	SubjectReference          string   `json:"subject_reference,omitempty"` // 主体参考(取第一条)
	StyleReference            string   `json:"style_reference,omitempty"`   // 风格参考(取第一条)
	SequentialImageGeneration string   `json:"sequential_image_generation,omitempty"`
	ResponseFormat            string   `json:"response_format,omitempty"`
	Size                      string   `json:"size,omitempty"`
	Stream                    bool     `json:"stream,omitempty"`
	Watermark                 bool     `json:"watermark,omitempty"`
}

type arkImagesResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL     string `json:"url,omitempty"`
		B64JSON string `json:"b64_json,omitempty"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Generate 实现 GenerationModel。
func (a *arkImage) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	size := pickArkSize(req.Size, req.AspectRatio, req.Width, req.Height)
	if err := validateArkSize(size); err != nil {
		return nil, err
	}
	imageRefs := partitionArkImageRefs(req.ReferenceImages)
	arkReq := arkImagesRequest{
		Model:                     a.cfg.Model,
		Prompt:                    req.Prompt,
		Image:                     firstOrEmpty(imageRefs.general),
		Images:                    imageRefs.general,
		SubjectReference:          firstURL(imageRefs.subject),
		StyleReference:            firstURL(imageRefs.style),
		SequentialImageGeneration: a.cfg.SequentialImageGeneration,
		ResponseFormat:            "url",
		Size:                      size,
		Stream:                    false,
		Watermark:                 pickArkWatermark(a.cfg.Watermark),
	}

	body, err := json.Marshal(arkReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	url := strings.TrimRight(a.cfg.BaseURL, "/") + "/images/generations"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post images: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("images: status %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}

	var parsed arkImagesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("upstream error %s: %s", parsed.Error.Code, parsed.Error.Message)
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("empty response data")
	}

	result := &GenerateResult{
		URLs: make([]string, 0, len(parsed.Data)),
		Raw:  map[string]any{"created": parsed.Created},
	}
	for _, d := range parsed.Data {
		if d.URL != "" {
			result.URLs = append(result.URLs, d.URL)
		} else if d.B64JSON != "" {
			result.URLs = append(result.URLs, "data:image/png;base64,"+d.B64JSON)
		}
	}
	return result, nil
}

// MinArkImagePixels 方舟 doubao-seedream 系列最低像素数（≈2K）。
// 来源：API 文档硬性约束，小于该值会返回 InvalidParameter。
const MinArkImagePixels = 3686400

// DefaultArkImageSize 当用户没传 size / aspect_ratio 时的兜底尺寸。
// 选 2048x2048（4 MP）以满足方舟最小像素要求。
const DefaultArkImageSize = "2048x2048"

// aspectToSize 方舟 Seedream 系列支持的画面比例 → 像素尺寸。
// 所有尺寸 ≥ MinArkImagePixels（≈2K）。
// 优先级最高的是用户显式 Size；size 未给时按 aspect_ratio 派生；
// 都没给就 fallback 到 DefaultArkImageSize。
var aspectToSize = map[string]string{
	// 所有尺寸 ≥ MinArkImagePixels (3,686,400 px, ≈2K) 以满足方舟 doubao-seedream 硬性约束。
	"1:1":  "2048x2048", // 4,194,304 px
	"16:9": "2560x1440", // 3,686,400 px (16:9 最小合规)
	"9:16": "1440x2560", // 3,686,400 px (9:16 最小合规)
	"4:3":  "2304x1728", // 3,981,312 px
	"3:4":  "1728x2304", // 3,981,312 px
	"3:2":  "2496x1664", // 4,153,344 px
	"2:3":  "1664x2496", // 4,153,344 px
	"21:9": "3360x1440", // 4,838,400 px
}

// pickArkSize 决定 size 字段优先级：
//
//	用户显式 Size > AspectRatio 派生 > 宽高组合 > 默认 2048x2048
//
// 输出会经 normalizeArkSize 规范化（"2K" → "2k" 等）。
func pickArkSize(explicit, ratio string, w, h int) string {
	var s string
	switch {
	case explicit != "":
		// 1K 是前端历史逻辑档位，但 Ark 只接受 WxH/2k/3k/4k。
		// 这里按比例映射到最低合规像素尺寸，避免丢失用户选择的画幅。
		if strings.EqualFold(explicit, "1k") {
			if sz, ok := aspectToSize[ratio]; ok {
				s = sz
			} else {
				s = DefaultArkImageSize
			}
		} else {
			s = explicit
		}
	case ratio != "":
		if sz, ok := aspectToSize[ratio]; ok {
			s = sz
		} else {
			// 未知比例：交给上游拒绝（不要静默改成默认值，行为可预测）
			s = ratio
		}
	case w > 0 && h > 0:
		s = fmt.Sprintf("%dx%d", w, h)
	default:
		s = DefaultArkImageSize
	}
	return normalizeArkSize(s)
}

// normalizeArkSize 把 size 字符串规范成方舟 API 接受的形式。
//
//   - 自由格式 "2K" / "2k" / "3K" / "4K" → 统一小写（方舟只接受 2k/3k/4k）
//   - "WxH" 格式（如 "2048x2048"）原样返回（已合规）
//   - 其他（如 "5k"）原样返回，由上游拒绝
//
// 只处理尾部单个字符大小写，不试图做其它格式转换。
func normalizeArkSize(s string) string {
	if s == "" {
		return s
	}
	if s[len(s)-1] == 'K' {
		return s[:len(s)-1] + "k"
	}
	return s
}

// pickArkWatermark 解引用可选 watermark 配置；nil 时默认 false。
func pickArkWatermark(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// validateArkSize 校验 size 字符串同时满足:
//  1. 格式合法: 必须是 "WxH" (如 "2048x2048") 或 "2k"/"3k"/"4k"
//  2. WxH 像素数 ≥ MinArkImagePixels (≈2K)
//
// 方舟 doubao-seedream API 只接受这三类格式("WIDTHxHEIGHT", "2k", "3k", "4k")。
// 其它("5k"、"720p"、"16:9"、随机字符串)直接被上游拒 → 400 InvalidParameter。
// 这里前置校验是为了在前端传错时给明确错误,而不是去撞上游黑盒。
//
// 错误信息显式列出合法集合 + 最小像素要求,便于前端提示用户改用
// aspect_ratio(自动派生出合规 WxH)或更大的尺寸。
func validateArkSize(size string) error {
	if size == "" {
		return nil
	}
	if !isValidArkSizeFormat(size) {
		return fmt.Errorf("size %q is invalid for Ark image generation; must be one of: WIDTHxHEIGHT (e.g. 2048x2048), 2k, 3k, or 4k", size)
	}
	// WxH: 检查像素数; k 格式已被方方 isValidArkSizeFormat 限制在 {2,3,4},无需再验
	if !strings.Contains(strings.ToLower(size), "k") {
		parts := strings.SplitN(size, "x", 2)
		w, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		h, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		if w*h < MinArkImagePixels {
			return fmt.Errorf("size %q has %d pixels, below Ark minimum %d (~≈2K). Use %dx%d or larger, or specify aspect_ratio (e.g. `16:9`) to auto-pick a valid size",
				size, w*h, MinArkImagePixels,
				intSqrt(MinArkImagePixels), intSqrt(MinArkImagePixels))
		}
	}
	return nil
}

// isValidArkSizeFormat 判断 size 是否为方舟 doubao-seedream 接受的格式之一:
//   - "WxH": 正整数 × 正整数,如 "2048x2048"
//   - "Nk": N ∈ {2, 3, 4},如 "2k"/"3k"/"4k"(大小写都接受)
//
// 不接受: "5k"、"720p"、"16:9"、非整数像素、非 {2,3,4}k 的自由分辨率。
func isValidArkSizeFormat(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K") {
		body := s[:len(s)-1]
		n, err := strconv.Atoi(body)
		if err != nil {
			return false
		}
		return n >= 2 && n <= 4
	}
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return false
	}
	return true
}

// intSqrt 整数平方根（向上取整），用于生成最小合规的正方形尺寸提示。
func intSqrt(n int) int {
	x := 1
	for x*x < n {
		x++
	}
	return x
}

// arkImageRefs 按 role 把参考图分桶,喂给方舟对应字段。
//
//	subject: 主体参考,走 subject_reference(方舟 API 仅单图)
//	style:   风格参考,走 style_reference(方舟 API 仅单图)
//	general: 无 role 或其他(包含 first_frame/last_frame 等视频专用 role 在 image 里的退化),
//	         走 images[] 多图参考
type arkImageRefs struct {
	subject []string
	style   []string
	general []string
}

// partitionArkImageRefs 按 ReferenceImage.Role 分桶。
//
// 多 subject / style 只取第一条(方舟 API 限制);后续丢弃,避免
// 静默吞数据 — 调用方应提前筛选。
func partitionArkImageRefs(refs []ReferenceImage) arkImageRefs {
	var out arkImageRefs
	for _, r := range refs {
		if r.URL == "" {
			continue
		}
		switch strings.ToLower(r.Role) {
		case "subject":
			out.subject = append(out.subject, r.URL)
		case "style":
			out.style = append(out.style, r.URL)
		default:
			// role="" 或 first_frame / last_frame / 其他未知值 — 都进 general
			out.general = append(out.general, r.URL)
		}
	}
	return out
}

// firstURL 取 slice 第一条;空返回 ""。
func firstURL(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// firstOrEmpty 同 firstURL(简化命名,语义一致)。
func firstOrEmpty(s []string) string {
	return firstURL(s)
}

// paramString / paramBool：从 ModelConfig inline 字段取值的辅助函数。
//
// 当前 ModelConfig 是结构化字段（无 Params map），保留接口以便未来扩展。
// ark_image 工厂目前不使用这些函数调用 yaml 私有参数，而是通过 cfg.SequentialImageGeneration
// 等结构化字段或默认配置读取。如需在 yaml 里追加 inline 自定义字段，可改造 ModelConfig 加 Params map。
func paramString(_ *config.ModelConfig, _ string) string { return "" }

func paramBool(_ *config.ModelConfig, _ string) *bool { return nil }
