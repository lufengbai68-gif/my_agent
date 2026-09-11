package api

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     ChatRequest
		wantErr string // 空 = 期望通过
	}{
		{
			name:    "message only",
			req:     ChatRequest{Message: "hi"},
			wantErr: "",
		},
		{
			name:    "empty request",
			req:     ChatRequest{},
			wantErr: "either message or parts",
		},
		{
			name: "text part",
			req:  ChatRequest{Parts: []ContentPart{{Type: "text", Text: "hello"}}},
		},
		{
			name: "image by url",
			req:  ChatRequest{Parts: []ContentPart{{Type: "image_url", URL: "https://x.com/a.jpg"}}},
		},
		{
			name: "image by url with detail",
			req:  ChatRequest{Parts: []ContentPart{{Type: "image_url", URL: "https://x.com/a.jpg", Detail: "high"}}},
		},
		{
			name: "image by base64 with mime",
			req:  ChatRequest{Parts: []ContentPart{{Type: "image_url", Base64Data: "AAAA", MIMEType: "image/png"}}},
		},
		{
			name:    "text part empty text",
			req:     ChatRequest{Parts: []ContentPart{{Type: "text"}}},
			wantErr: "text is required",
		},
		{
			name:    "image without url and base64",
			req:     ChatRequest{Parts: []ContentPart{{Type: "image_url"}}},
			wantErr: "exactly one of url or base64_data",
		},
		{
			name:    "image with both url and base64",
			req:     ChatRequest{Parts: []ContentPart{{Type: "image_url", URL: "https://x.com/a.jpg", Base64Data: "AAAA"}}},
			wantErr: "exactly one of url or base64_data",
		},
		{
			name:    "base64 without mime",
			req:     ChatRequest{Parts: []ContentPart{{Type: "audio_url", Base64Data: "AAAA"}}},
			wantErr: "mime_type is required",
		},
		{
			name:    "bad detail",
			req:     ChatRequest{Parts: []ContentPart{{Type: "image_url", URL: "https://x.com/a.jpg", Detail: "ultra"}}},
			wantErr: "detail must be",
		},
		{
			name:    "unknown type",
			req:     ChatRequest{Parts: []ContentPart{{Type: "smell_url", URL: "https://x.com"}}},
			wantErr: "unknown part type",
		},
		{
			name: "audio video file ok",
			req: ChatRequest{Parts: []ContentPart{
				{Type: "audio_url", URL: "https://x.com/a.wav"},
				{Type: "video_url", Base64Data: "AAAA", MIMEType: "video/mp4"},
				{Type: "file_url", URL: "https://x.com/doc.pdf", Name: "doc.pdf"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestToUserMessagePlainText(t *testing.T) {
	req := ChatRequest{Message: "hello"}
	msg := req.ToUserMessage()
	if msg.Role != schema.User || msg.Content != "hello" {
		t.Fatalf("got %+v", msg)
	}
	if len(msg.UserInputMultiContent) != 0 {
		t.Fatalf("expected no multimodal parts")
	}
}

func TestToUserMessageMultimodal(t *testing.T) {
	req := ChatRequest{Parts: []ContentPart{
		{Type: "text", Text: "what is this"},
		{Type: "image_url", URL: "https://x.com/a.jpg", Detail: "low"},
		{Type: "audio_url", Base64Data: "AAAA", MIMEType: "audio/wav"},
		{Type: "video_url", URL: "https://x.com/v.mp4"},
		{Type: "file_url", Base64Data: "BBBB", MIMEType: "application/pdf", Name: "doc.pdf"},
	}}
	msg := req.ToUserMessage()

	if msg.Role != schema.User {
		t.Fatalf("role = %s", msg.Role)
	}
	parts := msg.UserInputMultiContent
	if len(parts) != 5 {
		t.Fatalf("expected 5 parts, got %d", len(parts))
	}

	if parts[0].Type != schema.ChatMessagePartTypeText || parts[0].Text != "what is this" {
		t.Errorf("part0: %+v", parts[0])
	}
	if parts[1].Type != schema.ChatMessagePartTypeImageURL || parts[1].Image == nil ||
		parts[1].Image.URL == nil || *parts[1].Image.URL != "https://x.com/a.jpg" ||
		parts[1].Image.Detail != schema.ImageURLDetailLow {
		t.Errorf("part1: %+v", parts[1])
	}
	if parts[2].Type != schema.ChatMessagePartTypeAudioURL || parts[2].Audio == nil ||
		parts[2].Audio.Base64Data == nil || *parts[2].Audio.Base64Data != "AAAA" ||
		parts[2].Audio.MIMEType != "audio/wav" || parts[2].Audio.URL != nil {
		t.Errorf("part2: %+v", parts[2])
	}
	if parts[3].Type != schema.ChatMessagePartTypeVideoURL || parts[3].Video == nil ||
		parts[3].Video.URL == nil || *parts[3].Video.URL != "https://x.com/v.mp4" {
		t.Errorf("part3: %+v", parts[3])
	}
	if parts[4].Type != schema.ChatMessagePartTypeFileURL || parts[4].File == nil ||
		*parts[4].File.Base64Data != "BBBB" || parts[4].File.Name != "doc.pdf" {
		t.Errorf("part4: %+v", parts[4])
	}
}

func TestFromEinoMessage(t *testing.T) {
	// 多模态用户消息 → 视图（base64 不回显，url 保留）
	url := "https://x.com/a.jpg"
	msg := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "hi"},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/jpeg"},
			}},
		},
	}
	v := fromEinoMessage(msg)
	if v.Role != "user" || len(v.Parts) != 2 {
		t.Fatalf("got %+v", v)
	}
	if v.Parts[1].URL != url || v.Parts[1].Base64Data != "" || v.Parts[1].MIMEType != "image/jpeg" {
		t.Errorf("image part view: %+v", v.Parts[1])
	}

	// 纯文本 assistant 消息 → 无 Parts
	v2 := fromEinoMessage(&schema.Message{Role: schema.Assistant, Content: "ok"})
	if v2.Parts != nil || v2.Content != "ok" {
		t.Errorf("got %+v", v2)
	}
}
