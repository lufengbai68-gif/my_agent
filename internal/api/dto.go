// Package api 定义 HTTP 层：DTO、handlers、路由与错误约定。
package api

import (
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// ContentPart 多模态内容片段（扁平化结构，HTTP 友好）。
type ContentPart struct {
	Type       string `json:"type"`                  // text|image_url|audio_url|video_url|file_url
	Text       string `json:"text,omitempty"`        // type=text 必填
	URL        string `json:"url,omitempty"`         // 与 base64_data 二选一
	Base64Data string `json:"base64_data,omitempty"` // 裸 base64（无 data: 前缀）
	MIMEType   string `json:"mime_type,omitempty"`   // base64_data 时必填
	Detail     string `json:"detail,omitempty"`      // 仅图片: high|low|auto
	Name       string `json:"name,omitempty"`        // 仅文件: 原始文件名
}

// Part 类型常量，与 eino schema 的 ChatMessagePartType 值一致。
const (
	partTypeText     = "text"
	partTypeImageURL = "image_url"
	partTypeAudioURL = "audio_url"
	partTypeVideoURL = "video_url"
	partTypeFileURL  = "file_url"
)

// ChatRequest POST /v1/chat/completions 请求体。
type ChatRequest struct {
	SessionID string        `json:"session_id,omitempty"` // 空 → 服务端生成 UUID
	Agent     string        `json:"agent,omitempty"`      // 空 → 配置默认 agent
	Message   string        `json:"message,omitempty"`    // 纯文本便捷字段
	Parts     []ContentPart `json:"parts,omitempty"`      // 多模态；与 message 同时给出时优先
	Stream    bool          `json:"stream,omitempty"`     // true → SSE 响应
}

// Usage token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse 非流式响应体。
type ChatResponse struct {
	SessionID    string `json:"session_id"`
	Agent        string `json:"agent"`
	Role         string `json:"role"`
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// MessageView 会话历史中的单条消息视图。
type MessageView struct {
	Role    string        `json:"role"`
	Content string        `json:"content"`
	Parts   []ContentPart `json:"parts,omitempty"` // 仅多模态用户消息非空
}

// SessionView 会话查询响应。
type SessionView struct {
	ID        string        `json:"id"`
	CreatedAt int64         `json:"created_at"`
	UpdatedAt int64         `json:"updated_at"`
	Messages  []MessageView `json:"messages"`
}

// Validate 校验请求合法性。
func (r *ChatRequest) Validate() error {
	if r.Message == "" && len(r.Parts) == 0 {
		return errors.New("either message or parts must be provided")
	}
	for i, p := range r.Parts {
		if err := p.validate(i); err != nil {
			return err
		}
	}
	return nil
}

func (p ContentPart) validate(idx int) error {
	at := fmt.Sprintf("parts[%d]", idx)
	switch p.Type {
	case partTypeText:
		if p.Text == "" {
			return fmt.Errorf("%s: text is required for type %q", at, partTypeText)
		}
	case partTypeImageURL, partTypeAudioURL, partTypeVideoURL, partTypeFileURL:
		if (p.URL == "") == (p.Base64Data == "") {
			return fmt.Errorf("%s: exactly one of url or base64_data is required for type %q", at, p.Type)
		}
		if p.Base64Data != "" && p.MIMEType == "" {
			return fmt.Errorf("%s: mime_type is required with base64_data", at)
		}
		if p.Type == partTypeImageURL && p.Detail != "" {
			switch p.Detail {
			case "high", "low", "auto":
			default:
				return fmt.Errorf("%s: detail must be high|low|auto, got %q", at, p.Detail)
			}
		}
	default:
		return fmt.Errorf("%s: unknown part type %q", at, p.Type)
	}
	return nil
}

// ToUserMessage 转为 eino 用户消息。Parts 非空时走多模态路径。
func (r *ChatRequest) ToUserMessage() *schema.Message {
	if len(r.Parts) == 0 {
		return &schema.Message{Role: schema.User, Content: r.Message}
	}

	parts := make([]schema.MessageInputPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		switch p.Type {
		case partTypeText:
			parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: p.Text})

		case partTypeImageURL:
			img := &schema.MessageInputImage{
				MessagePartCommon: commonPart(p.URL, p.Base64Data, p.MIMEType),
			}
			switch p.Detail {
			case "high":
				img.Detail = schema.ImageURLDetailHigh
			case "low":
				img.Detail = schema.ImageURLDetailLow
			case "auto":
				img.Detail = schema.ImageURLDetailAuto
			}
			parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: img})

		case partTypeAudioURL:
			parts = append(parts, schema.MessageInputPart{
				Type:  schema.ChatMessagePartTypeAudioURL,
				Audio: &schema.MessageInputAudio{MessagePartCommon: commonPart(p.URL, p.Base64Data, p.MIMEType)},
			})

		case partTypeVideoURL:
			parts = append(parts, schema.MessageInputPart{
				Type:  schema.ChatMessagePartTypeVideoURL,
				Video: &schema.MessageInputVideo{MessagePartCommon: commonPart(p.URL, p.Base64Data, p.MIMEType)},
			})

		case partTypeFileURL:
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeFileURL,
				File: &schema.MessageInputFile{
					MessagePartCommon: commonPart(p.URL, p.Base64Data, p.MIMEType),
					Name:              p.Name,
				},
			})
		}
	}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

// commonPart 构造 URL/Base64 二选一的公共字段（指针语义由 eino 定义：
// nil 表示未提供，空串表示显式空值，这里只在非空时设置）。
func commonPart(url, base64, mime string) schema.MessagePartCommon {
	c := schema.MessagePartCommon{MIMEType: mime}
	if url != "" {
		u := url
		c.URL = &u
	}
	if base64 != "" {
		b := base64
		c.Base64Data = &b
	}
	return c
}

// fromEinoMessage 将存储的 eino 消息转为历史视图。
func fromEinoMessage(m *schema.Message) MessageView {
	v := MessageView{Role: string(m.Role), Content: m.Content}
	if len(m.UserInputMultiContent) > 0 {
		v.Parts = make([]ContentPart, 0, len(m.UserInputMultiContent))
		for _, ip := range m.UserInputMultiContent {
			if cp, ok := fromEinoPart(ip); ok {
				v.Parts = append(v.Parts, cp)
			}
		}
	}
	return v
}

// fromEinoPart 将 eino 多模态 part 转回 HTTP ContentPart（历史回显用）。
// 回显时省略 base64 数据（体积大且客户端本来就有），URL 保留。
func fromEinoPart(p schema.MessageInputPart) (ContentPart, bool) {
	cp := ContentPart{Type: string(p.Type)}
	switch p.Type {
	case schema.ChatMessagePartTypeText:
		cp.Text = p.Text
	case schema.ChatMessagePartTypeImageURL:
		if p.Image == nil {
			return cp, false
		}
		cp.URL = strDeref(p.Image.URL)
		cp.MIMEType = p.Image.MIMEType
		cp.Detail = string(p.Image.Detail)
	case schema.ChatMessagePartTypeAudioURL:
		if p.Audio == nil {
			return cp, false
		}
		cp.URL = strDeref(p.Audio.URL)
		cp.MIMEType = p.Audio.MIMEType
	case schema.ChatMessagePartTypeVideoURL:
		if p.Video == nil {
			return cp, false
		}
		cp.URL = strDeref(p.Video.URL)
		cp.MIMEType = p.Video.MIMEType
	case schema.ChatMessagePartTypeFileURL:
		if p.File == nil {
			return cp, false
		}
		cp.URL = strDeref(p.File.URL)
		cp.MIMEType = p.File.MIMEType
		cp.Name = p.File.Name
	default:
		return cp, false
	}
	return cp, true
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
