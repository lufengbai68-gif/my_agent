package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// seedanceVideo 实现 GenerationModel 接口，封装火山方舟 Seedance 视频生成。
//
// 协议：
//
//	POST {base_url}/api/v3/contents/generations/tasks → task_id
//	GET  {base_url}/api/v3/contents/generations/tasks/{id} → status + video_url
type seedanceVideo struct {
	cfg seedanceConfig
}

// seedanceConfig 配置。
type seedanceConfig struct {
	APIKey       string
	BaseURL      string
	Model        string
	PollInterval time.Duration
	MaxWait      time.Duration
	HTTPClient   *http.Client
	now          func() time.Time
	sleep        func(context.Context, time.Duration) error
}

func (c *seedanceConfig) validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("api_key is required")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.MaxWait <= 0 {
		c.MaxWait = 600 * time.Second
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.sleep == nil {
		c.sleep = defaultSleep
	}
	return nil
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// newSeedanceVideo GenerationFactory 入口。
func newSeedanceVideo(ctx context.Context, cfg *config.ModelConfig) (GenerationModel, error) {
	c := seedanceConfig{
		APIKey:       cfg.APIKey,
		BaseURL:      cfg.BaseURL,
		Model:        cfg.Model,
		PollInterval: cfg.PollInterval,
		MaxWait:      cfg.MaxWait,
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &seedanceVideo{cfg: c}, nil
}

// Kind 实现 GenerationModel。
func (s *seedanceVideo) Kind() string { return "video" }

// Generate 实现 GenerationModel。
func (s *seedanceVideo) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	taskID, err := s.createTask(ctx, req)
	if err != nil {
		return nil, err
	}
	res, err := s.pollUntilDone(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if res.Status != "succeeded" {
		msg := "unknown"
		code := "unknown"
		if res.Error != nil {
			code = res.Error.Code
			msg = res.Error.Message
		}
		return nil, fmt.Errorf("video generation %s: %s: %s", res.Status, code, msg)
	}

	result := &GenerateResult{
		URLs: nil,
		Raw:  map[string]any{"task_id": res.ID, "status": res.Status},
	}
	if res.Content != nil && res.Content.VideoURL != "" {
		result.URLs = []string{res.Content.VideoURL}
	}
	if res.Usage != nil {
		result.Usage = &GenerateUsage{
			Frames:   res.Usage.GeneratedFrames,
			Duration: res.Usage.Duration,
			Ratio:    res.Usage.VideoRatio,
		}
	}
	return result, nil
}

// --- HTTP / 方舟协议 ---

// seedanceCreateReq 方舟 Seedance 创建任务请求体。
//
// 字段说明：
//   - Model: 模型名（如 "Doubao-Seedance-2.5"）
//   - Content: 多模态内容，至少包含一条 text；
//     ReferenceImages 按 role 拆成 image_url (first_frame/general) 和 image_url_last (last_frame)
//   - Parameters: 生成参数；只有非零值才写入，避免污染上游默认值
type seedanceCreateReq struct {
	Model       string       `json:"model"`
	Content     []seedanceCT `json:"content"`
	Resolution  string       `json:"resolution,omitempty"`
	Ratio       string       `json:"ratio,omitempty"`
	Duration    int          `json:"duration,omitempty"`
	FPS         int          `json:"fps,omitempty"`
	Seed        int64        `json:"seed,omitempty"`
	Watermark   *bool        `json:"watermark,omitempty"`
	CameraFixed bool         `json:"camera_fixed,omitempty"`
}

type seedanceCT struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// ImageURL: 内容项(方舟 Seedance content[] 元素,首帧 / 通用参考图)
	ImageURL *seedanceImageURL `json:"image_url,omitempty"`
	// ImageURLLast: 末帧参考(Seedance 2.5+ 专用)
	ImageURLLast *seedanceImageURL `json:"image_url_last,omitempty"`
}

type seedanceImageURL struct {
	URL string `json:"url"`
}

// seedanceParams Seedance 支持的生成参数（部分）。
// 零值字段不会被 omitempty 序列化掉——只有非零才写出。
type seedanceParams struct {
	Ratio       string `json:"ratio,omitempty"`       // 16:9 / 9:16 / 1:1 / 4:3 / 3:4
	Duration    int    `json:"duration,omitempty"`    // 5 / 10
	Resolution  string `json:"resolution,omitempty"`  // 480p / 720p / 1080p
	FPS         int    `json:"fps,omitempty"`         // 24
	Seed        int64  `json:"seed,omitempty"`        // 任意整数
	Watermark   *bool  `json:"watermark,omitempty"`   // 显式 nil 不传；非 nil 透传（含 false）
	CameraFixed bool   `json:"camerafixed,omitempty"` // true 相机固定
}

type seedanceCreateResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type seedanceTaskResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content *struct {
		VideoURL string `json:"video_url"`
	} `json:"content,omitempty"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Usage *struct {
		GeneratedFrames int    `json:"generated_video_frames"`
		VideoRatio      string `json:"video_ratio"`
		Duration        string `json:"video_duration"`
	} `json:"usage,omitempty"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

var seedanceTerminal = map[string]bool{
	"succeeded": true,
	"failed":    true,
	"cancelled": true,
}

func (s *seedanceVideo) createTask(ctx context.Context, req GenerateRequest) (string, error) {
	content := buildSeedanceContent(req)
	if len(content) == 0 {
		return "", fmt.Errorf("create task: empty content")
	}
	params := buildSeedanceParams(req)
	body, _ := json.Marshal(seedanceCreateReq{
		Model:       s.cfg.Model,
		Content:     content,
		Resolution:  params.Resolution,
		Ratio:       params.Ratio,
		Duration:    params.Duration,
		FPS:         params.FPS,
		Seed:        params.Seed,
		Watermark:   params.Watermark,
		CameraFixed: params.CameraFixed,
	})

	url := strings.TrimRight(s.cfg.BaseURL, "/") + "/contents/generations/tasks"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("post create task: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("create task: status %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}

	var parsed seedanceCreateResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("create task: empty id: %s", truncate(string(raw), 256))
	}
	if parsed.Error != nil {
		return "", fmt.Errorf("create task: upstream error %s: %s", parsed.Error.Code, parsed.Error.Message)
	}
	return parsed.ID, nil
}

func (s *seedanceVideo) pollUntilDone(ctx context.Context, taskID string) (*seedanceTaskResp, error) {
	deadline := s.cfg.now().Add(s.cfg.MaxWait)
	url := strings.TrimRight(s.cfg.BaseURL, "/") + "/contents/generations/tasks/" + taskID

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		elapsed := s.cfg.now().Sub(deadline.Add(-s.cfg.MaxWait))
		if s.cfg.now().After(deadline) {
			return nil, fmt.Errorf("poll timeout after %s", s.cfg.MaxWait)
		}

		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		httpReq.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
		resp, err := s.cfg.HTTPClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("poll task: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("poll task: status %d: %s", resp.StatusCode, truncate(string(raw), 512))
		}

		var status seedanceTaskResp
		if err := json.Unmarshal(raw, &status); err != nil {
			return nil, fmt.Errorf("decode poll response: %w", err)
		}
		if seedanceTerminal[status.Status] {
			return &status, nil
		}
		if err := s.cfg.sleep(ctx, pollDelay(elapsed, s.cfg.PollInterval)); err != nil {
			return nil, err
		}
	}
}

func pollDelay(elapsed, configured time.Duration) time.Duration {
	if configured <= 0 {
		configured = 2 * time.Second
	}
	delay := 2 * time.Second
	if elapsed >= 30*time.Second {
		delay = 5 * time.Second
	}
	if elapsed >= 2*time.Minute {
		delay = 8 * time.Second
	}
	if configured < delay {
		return configured
	}
	return delay
}

// buildSeedanceContent 装配 content[]:一条 text + 多张 image_url(按 role 排序)。
//
// role 排序:
//   - first_frame: 首帧(作为 image_url,排在通用参考之前)
//   - last_frame:  末帧(作为 image_url_last,永远在最后)
//   - 其他 / 空 role: 通用参考图(走 image_url,排在 first_frame 之后、last_frame 之前)
func buildSeedanceContent(req GenerateRequest) []seedanceCT {
	content := []seedanceCT{{Type: "text", Text: req.Prompt}}

	var firstFrame string
	var lastFrame string
	var general []string

	for _, ref := range req.ReferenceImages {
		if ref.URL == "" {
			continue
		}
		switch strings.ToLower(ref.Role) {
		case "first_frame":
			if firstFrame == "" {
				firstFrame = ref.URL
			}
		case "last_frame":
			if lastFrame == "" {
				lastFrame = ref.URL
			}
		default:
			general = append(general, ref.URL)
		}
	}

	// 追加顺序:首帧 → 通用参考 → 末帧
	if firstFrame != "" {
		content = append(content, seedanceCT{
			Type:     "image_url",
			ImageURL: &seedanceImageURL{URL: firstFrame},
		})
	}
	for _, u := range general {
		content = append(content, seedanceCT{
			Type:     "image_url",
			ImageURL: &seedanceImageURL{URL: u},
		})
	}
	if lastFrame != "" {
		content = append(content, seedanceCT{
			Type:         "image_url_last",
			ImageURLLast: &seedanceImageURL{URL: lastFrame},
		})
	}
	return content
}

// buildSeedanceParams 把 GenerateRequest 映射为方舟 Seedance parameters 块。
//
// 优先级：
//   - ratio: 显式 AspectRatio > Size（若带 ":" 视作比例）
//   - resolution: 显式 Resolution > Size（若是 "720p" / "1080p" 等）
//   - 其余字段零值不传（omitempty）
//   - Seed 0 / 负数不传（让上游随机）
//   - Watermark nil → 不传（用上游默认），非 nil → 透传 bool
//   - CameraMotion == "static" → CameraFixed=true
func buildSeedanceParams(req GenerateRequest) *seedanceParams {
	p := &seedanceParams{}

	// ratio：aspect_ratio 优先；若 size 形如 "16:9" 也认
	switch {
	case req.AspectRatio != "":
		if req.AspectRatio == "auto" {
			p.Ratio = "adaptive"
		} else {
			p.Ratio = req.AspectRatio
		}
	case looksLikeRatio(req.Size):
		p.Ratio = req.Size
	}

	// resolution：显式 Resolution 优先；size 若带 "p" 也认
	switch {
	case req.Resolution != "":
		p.Resolution = req.Resolution
	case looksLikeResolution(req.Size):
		p.Resolution = req.Size
	}

	if req.Duration > 0 {
		p.Duration = req.Duration
	}
	if req.FPS > 0 {
		p.FPS = req.FPS
	}
	if req.Seed > 0 {
		p.Seed = int64(req.Seed)
	}
	if req.Watermark != nil {
		wm := *req.Watermark
		p.Watermark = &wm
	}
	if req.CameraMotion == "static" {
		p.CameraFixed = true
	}

	// 空对象不应发送——如果所有字段都是零值，返回 nil 走 omitempty
	if p.Ratio == "" && p.Resolution == "" && p.Duration == 0 && p.FPS == 0 &&
		p.Seed == 0 && p.Watermark == nil && !p.CameraFixed {
		return nil
	}
	return p
}

func looksLikeRatio(s string) bool {
	if s == "" {
		return false
	}
	// 形如 "16:9"
	for i, r := range s {
		if r == ':' && i > 0 && i < len(s)-1 {
			return true
		}
	}
	return false
}

func looksLikeResolution(s string) bool {
	// 形如 "720p" / "1080p"
	if len(s) < 3 {
		return false
	}
	last := s[len(s)-1]
	if last != 'p' && last != 'P' {
		return false
	}
	for i := 0; i < len(s)-1; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
