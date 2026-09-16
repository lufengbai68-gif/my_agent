// Package history 定义面向前端回放的独立历史消息模型。
// 它不依赖 Eino 运行时消息结构，避免从 Extra 反推 UI 状态。
package history

import (
	"github.com/google/uuid"

	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusStreaming Status = "streaming"
	StatusSuccess   Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type Request struct {
	Model          string                `json:"model,omitempty"`
	Mode           string                `json:"mode,omitempty"`
	GenerationType string                `json:"generation_type,omitempty"`
	Params         *llm.GenerationParams `json:"params,omitempty"`
}

type Attachment struct {
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

type Message struct {
	ID          string       `json:"id"`
	TurnID      string       `json:"turn_id"`
	RequestID   string       `json:"request_id"`
	Seq         int          `json:"seq"`
	Role        Role         `json:"role"`
	Status      Status       `json:"status"`
	Text        string       `json:"text"`
	Error       string       `json:"error,omitempty"`
	CreatedAt   int64        `json:"created_at"`
	Request     *Request     `json:"request,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

func NewID() string {
	return uuid.NewString()
}

func (m Message) Clone() Message {
	cloned := m
	if m.Request != nil {
		request := *m.Request
		if m.Request.Params != nil {
			params := *m.Request.Params
			request.Params = &params
		}
		cloned.Request = &request
	}
	if len(m.Attachments) > 0 {
		cloned.Attachments = append([]Attachment(nil), m.Attachments...)
	}
	return cloned
}
