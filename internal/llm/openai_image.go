package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// openaiImage 实现 GenerationModel 接口，调用 OpenAI Images API。
//
// POST {base_url}/images/generations {prompt, n, size} → {data: [{url|b64_json}]}
type openaiImage struct {
	cfg openaiImageConfig
}

type openaiImageConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func (c *openaiImageConfig) validate() error {
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
	return nil
}

// newOpenAIImage GenerationFactory 入口。
func newOpenAIImage(ctx context.Context, cfg *config.ModelConfig) (GenerationModel, error) {
	c := openaiImageConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &openaiImage{cfg: c}, nil
}

// Kind 实现 GenerationModel。
func (o *openaiImage) Kind() string { return "image" }

// openaiImagesRequest POST /images/generations 请求体。
type openaiImagesRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	N      int    `json:"n,omitempty"`
	Size   string `json:"size,omitempty"`
}

type openaiImagesResponse struct {
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
func (o *openaiImage) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	if req.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	body, _ := json.Marshal(openaiImagesRequest{
		Model:  o.cfg.Model,
		Prompt: req.Prompt,
		N:      pickN(req.N),
		Size:   pickSize(req.Width, req.Height),
	})

	url := strings.TrimRight(o.cfg.BaseURL, "/") + "/images/generations"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post images: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("images: status %d: %s", resp.StatusCode, truncate(string(raw), 512))
	}

	var parsed openaiImagesResponse
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
	for i, d := range parsed.Data {
		if d.URL != "" {
			result.URLs = append(result.URLs, d.URL)
		} else if d.B64JSON != "" {
			// b64_json 模式：data URI 化让前端可显示
			result.URLs = append(result.URLs, fmt.Sprintf("data:image/png;base64,%s", d.B64JSON))
			_ = i
		}
	}
	return result, nil
}

// pickN 取 N，缺省 1。
func pickN(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// pickSize 取尺寸，缺省 1024x1024。
func pickSize(w, h int) string {
	if w == 0 && h == 0 {
		return "1024x1024"
	}
	return fmt.Sprintf("%dx%d", w, h)
}

// 保留 base64 引入（用于扩展本地文件保存）
var _ = base64.StdEncoding
