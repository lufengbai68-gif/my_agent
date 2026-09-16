package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ModelInfoDTO 返回给前端的模型元信息。
type ModelInfoDTO struct {
	Key          string             `json:"key"`
	Label        string             `json:"label"`
	ModelType    string             `json:"model_type"` // image | video
	Capabilities map[string]bool    `json:"capabilities"`
}

// ModelsResponseDTO 顶层响应。
type ModelsResponseDTO struct {
	Models []ModelInfoDTO `json:"models"`
}

// handleListModels 返回所有 chat_only=false 的生成模型（前端可见列表）。
//
// 用户在前端只看到 image / video 生成能力，看不到对话模型。
func (h *Handler) handleListModels(c *gin.Context) {
	out := ModelsResponseDTO{Models: []ModelInfoDTO{}}
	for name, m := range h.cfg.Models {
		if m.ChatOnly {
			continue
		}
		// 只展示生成模型（chat_only=false 且 model_type != chat）
		if m.ModelType == "chat" {
			continue
		}
		caps := map[string]bool{
			"text":  m.Capabilities.Text,
			"image": m.Capabilities.Image,
			"audio": m.Capabilities.Audio,
			"video": m.Capabilities.Video,
			"file":  m.Capabilities.File,
		}
		label := m.Label
		if label == "" {
			label = name
		}
		out.Models = append(out.Models, ModelInfoDTO{
			Key:          name,
			Label:        label,
			ModelType:    m.ModelType,
			Capabilities: caps,
		})
	}
	c.JSON(http.StatusOK, out)
}
