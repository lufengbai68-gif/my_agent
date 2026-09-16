// Package api 定义 HTTP 层：DTO、handlers、路由与错误约定。
package api

import (
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/chat"
	"github.com/lufengbai68-gif/my_agent/internal/history"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
)

// ContentPart 多模态内容片段（扁平化结构，HTTP 友好）。
type ContentPart struct {
	Type       string        `json:"type"`                  // text|image_url|audio_url|video_url|file_url
	Text       string        `json:"text,omitempty"`        // type=text 必填
	URL        string        `json:"url,omitempty"`         // 旧版兼容；image_url 新请求请使用 ImageURL
	ImageURL   *ImageURLPart `json:"image_url,omitempty"`   // 新版嵌套图片 URL
	Base64Data string        `json:"base64_data,omitempty"` // 裸 base64（无 data: 前缀）
	MIMEType   string        `json:"mime_type,omitempty"`   // base64_data 时必填
	Detail     string        `json:"detail,omitempty"`      // 仅图片: high|low|auto
	Name       string        `json:"name,omitempty"`        // 仅文件: 原始文件名
	UploadID   string        `json:"upload_id,omitempty"`   // 图片上传记录 ID，用于历史回显
}

// ImageURLPart 图片 Part 的嵌套 URL 结构。
type ImageURLPart struct {
	URL string `json:"url"`
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
//
// 字段说明：
//   - session_id：会话 ID；空 → 服务端生成 UUID；带值 → 复用历史上下文
//   - model：用户在前端选的"生成模型 key"（从 GET /v1/models 拿）。文本对话可不传，
//     此时 dispatcher 不会自动调生成工具
//   - generation_params：多模态生成参数（比例/分辨率/时长/数量/种子等）。
//     所有字段可选；前端只传用户显式选过的。这些值会作为硬性指令注入到 dispatcher，
//     强制生成工具使用，不会让 LLM 从 prompt 自然语言里推断
//   - parts：用户消息（统一多模态；至少一个 text part）
//   - stream：true → SSE；false → 一次性 JSON
type ChatRequest struct {
	SessionID        string                `json:"session_id,omitempty"`        // 空 → 服务端生成 UUID
	Model            string                `json:"model,omitempty"`             // 生成模型 key（决定 dispatcher 路由）
	GenerationParams *llm.GenerationParams `json:"generation_params,omitempty"` // 多模态生成参数
	Parts            []ContentPart         `json:"parts"`                       // 必填：多模态内容（包含至少一条 text）
	Stream           bool                  `json:"stream,omitempty"`            // true → SSE 响应
}

// Usage token 用量。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse 非流式响应体。
type ChatResponse struct {
	SessionID    string           `json:"session_id"`
	Agent        string           `json:"agent"`
	Role         string           `json:"role"`
	Content      string           `json:"content"`
	Media        []chat.MediaItem `json:"media,omitempty"` // 图片/视频等生成产物
	FinishReason string           `json:"finish_reason,omitempty"`
	Usage        *Usage           `json:"usage,omitempty"`
	CreatedAt    int64            `json:"created_at"`
}

// MessageView 会话历史中的单条消息视图。
type MessageView struct {
	ID          string                  `json:"id"`
	TurnID      string                  `json:"turn_id,omitempty"`
	RequestID   string                  `json:"request_id,omitempty"`
	Seq         int                     `json:"seq,omitempty"`
	Role        string                  `json:"role"`
	Status      string                  `json:"status,omitempty"`
	Text        string                  `json:"text"`
	Error       string                  `json:"error,omitempty"`
	CreatedAt   int64                   `json:"created_at,omitempty"`
	Request     *MessageRequestView     `json:"request,omitempty"`
	Attachments []MessageAttachmentView `json:"attachments,omitempty"`
}

// MessageRequestView 保存发起该轮生成时的完整上下文。
type MessageRequestView struct {
	Model            string                `json:"model,omitempty"`
	Mode             string                `json:"mode,omitempty"`
	GenerationType   string                `json:"generation_type,omitempty"`
	GenerationParams *llm.GenerationParams `json:"generation_params,omitempty"`
}

// MessageAttachmentView 消息内嵌的多模态附件。
// kind=reference 表示用户上传；kind=generated 表示模型生成。
type MessageAttachmentView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	FileName    string `json:"file_name,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Description string `json:"description,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	UploadID    string `json:"upload_id,omitempty"`
	SortOrder   int    `json:"sort_order"`
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
	if len(r.Parts) == 0 {
		return errors.New("parts is required (must include at least one text part)")
	}
	hasText := false
	for i, p := range r.Parts {
		if err := p.validate(i); err != nil {
			return err
		}
		if p.Type == partTypeText {
			hasText = true
		}
	}
	if !hasText {
		return errors.New("parts must contain at least one text part")
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
		url := p.imageURL()
		if (url == "") == (p.Base64Data == "") {
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

func (p ContentPart) imageURL() string {
	if p.ImageURL != nil {
		return p.ImageURL.URL
	}
	return p.URL
}

// ToUserMessage 转为 eino 用户消息（多模态，parts 必填）。
func (r *ChatRequest) ToUserMessage() *schema.Message {
	parts := make([]schema.MessageInputPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		switch p.Type {
		case partTypeText:
			parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: p.Text})

		case partTypeImageURL:
			imageExtra := map[string]any{}
			if p.UploadID != "" {
				imageExtra["upload_id"] = p.UploadID
			}
			if p.Name != "" {
				imageExtra["filename"] = p.Name
			}
			img := &schema.MessageInputImage{
				MessagePartCommon: commonPart(p.imageURL(), p.Base64Data, p.MIMEType),
			}
			switch p.Detail {
			case "high":
				img.Detail = schema.ImageURLDetailHigh
			case "low":
				img.Detail = schema.ImageURLDetailLow
			case "auto":
				img.Detail = schema.ImageURLDetailAuto
			}
			parts = append(parts, schema.MessageInputPart{
				Type:  schema.ChatMessagePartTypeImageURL,
				Image: img,
				Extra: imageExtra,
			})

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
	return &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
		Extra: map[string]any{
			"model":             r.Model,
			"generation_params": r.GenerationParams,
		},
	}
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
func fromEinoMessage(m *schema.Message, messageID string, uploadMeta map[string]uploads.Upload) MessageView {
	v := MessageView{ID: messageID, Role: string(m.Role), Text: m.Content}
	if v.Text == "" {
		for _, part := range m.UserInputMultiContent {
			if part.Type == schema.ChatMessagePartTypeText {
				v.Text = part.Text
				break
			}
		}
	}
	if createdAt, ok := m.Extra["created_at"].(int64); ok {
		v.CreatedAt = createdAt
	}
	var request *MessageRequestView
	if model, ok := m.Extra["model"].(string); ok {
		params, _ := m.Extra["generation_params"].(*llm.GenerationParams)
		if params != nil {
			copied := *params
			copied.ReferenceImages = make([]llm.ReferenceImage, 0, len(params.ReferenceImages))
			for _, reference := range params.ReferenceImages {
				copied.ReferenceImages = append(copied.ReferenceImages, llm.ReferenceImage{
					Role:     reference.Role,
					UploadID: reference.UploadID,
				})
			}
			params = &copied
		}
		mode := ""
		generationType := ""
		if params != nil {
			generationType = params.GenerationType
		}
		if params != nil && params.Duration > 0 {
			mode = "video"
		} else if params != nil {
			mode = "image"
		}
		if generationType == "" && mode == "video" {
			for _, reference := range params.ReferenceImages {
				if reference.Role == "first_frame" || reference.Role == "last_frame" {
					generationType = "first_last_frame"
					break
				}
			}
			if generationType == "" {
				generationType = "all_in_one"
			}
		}
		request = &MessageRequestView{
			Model:            model,
			Mode:             mode,
			GenerationType:   generationType,
			GenerationParams: params,
		}
	}
	v.Request = request

	if len(m.UserInputMultiContent) > 0 {
		for _, part := range m.UserInputMultiContent {
			if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil {
				continue
			}
			uploadID, _ := part.Extra["upload_id"].(string)
			fileName, _ := part.Extra["filename"].(string)
			attachment := MessageAttachmentView{
				ID:       uploadID,
				Kind:     "reference",
				Type:     "image",
				URL:      strDeref(part.Image.URL),
				FileName: fileName,
				MIMEType: part.Image.MIMEType,
				Purpose:  "reference",
				UploadID: uploadID,
			}
			if uploadID != "" {
				if upload, ok := uploadMeta[uploadID]; ok {
					attachment.URL = upload.URL
					attachment.FileName = upload.Filename
					attachment.MIMEType = upload.MimeType
					attachment.Width = upload.Width
					attachment.Height = upload.Height
					attachment.ExpiresAt = upload.ExpiresAt
				}
			}
			v.Attachments = append(v.Attachments, attachment)
		}
	}

	if media, ok := m.Extra["media"].([]chat.MediaItem); ok {
		for index, item := range media {
			v.Attachments = append(v.Attachments, MessageAttachmentView{
				ID:          fmt.Sprintf("%s:media:%d", messageID, index),
				Kind:        "generated",
				Type:        item.Type,
				URL:         item.URL,
				Description: item.Description,
				Purpose:     "output",
				UploadID:    item.UploadID,
			})
		}
	}
	return v
}

func fromHistoryMessage(message history.Message) MessageView {
	attachments := make([]MessageAttachmentView, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		attachments = append(attachments, MessageAttachmentView{
			ID:          attachment.ID,
			Kind:        attachment.Kind,
			Type:        attachment.Type,
			URL:         attachment.URL,
			FileName:    attachment.FileName,
			MIMEType:    attachment.MIMEType,
			Width:       attachment.Width,
			Height:      attachment.Height,
			ExpiresAt:   attachment.ExpiresAt,
			Description: attachment.Description,
			Purpose:     attachment.Purpose,
			UploadID:    attachment.UploadID,
			SortOrder:   attachment.SortOrder,
		})
	}

	var request *MessageRequestView
	if message.Request != nil {
		request = &MessageRequestView{
			Model:            message.Request.Model,
			Mode:             message.Request.Mode,
			GenerationType:   message.Request.GenerationType,
			GenerationParams: message.Request.Params,
		}
	}

	return MessageView{
		ID:          message.ID,
		TurnID:      message.TurnID,
		RequestID:   message.RequestID,
		Seq:         message.Seq,
		Role:        string(message.Role),
		Status:      string(message.Status),
		Text:        message.Text,
		Error:       message.Error,
		CreatedAt:   message.CreatedAt,
		Request:     request,
		Attachments: attachments,
	}
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
