package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/uploads"
)

// handleListSessions GET /v1/sessions —— 所有 session 摘要(侧边栏用)。
//
// 查询参数:
//   - limit: 返回数量上限,默认 50,最大 200
//   - offset: 跳过前 N 条,默认 0
//     响应额外带 total(总数,前端用来判断是否还有更多)。
func (h *Handler) handleListSessions(c *gin.Context) {
	limit, offset := parsePagination(c.Query("limit"), c.Query("offset"))
	all, err := h.svc.ListSessions(c.Request.Context())
	if err != nil {
		failErr(c, err)
		return
	}
	total := len(all)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	page := all[start:end]

	out := make([]gin.H, 0, len(page))
	for _, s := range page {
		out = append(out, gin.H{
			"id":            s.ID,
			"created_at":    s.CreatedAt,
			"updated_at":    s.UpdatedAt,
			"message_count": s.MessageCount,
			"preview":       s.Preview,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"sessions": out,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

// parsePagination 解析 limit / offset 查询参数,带默认值和上限。
func parsePagination(limitStr, offsetStr string) (limit, offset int) {
	limit = 50
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}
	if limit > 200 {
		limit = 200
	}
	offset = 0
	if v, err := strconv.Atoi(offsetStr); err == nil && v > 0 {
		offset = v
	}
	return
}

func refreshAttachmentViews(attachments []MessageAttachmentView, uploadMeta map[string]uploads.Upload) {
	for index := range attachments {
		uploadID := attachments[index].UploadID
		if uploadID == "" && attachments[index].Kind == "reference" {
			uploadID = attachments[index].ID
		}
		if uploadID == "" {
			continue
		}
		upload, ok := uploadMeta[uploadID]
		if !ok {
			continue
		}
		if upload.URL != "" {
			attachments[index].URL = upload.URL
		}
		if upload.Filename != "" {
			attachments[index].FileName = upload.Filename
		}
		if upload.MimeType != "" {
			attachments[index].MIMEType = upload.MimeType
		}
		if upload.Width > 0 {
			attachments[index].Width = upload.Width
		}
		if upload.Height > 0 {
			attachments[index].Height = upload.Height
		}
		attachments[index].ExpiresAt = upload.ExpiresAt
	}
}

// handleGetSession GET /v1/sessions/:id —— 单 session 完整历史。
// 上传参考图和模型生成产物都内嵌到对应 message.attachments，前端不需要二次关联。
//
// 查询参数:
//   - fresh=false: 跳过 upload 的 fresh URL 重新生成(返回 UploadStore 里存的旧 URL,
//     通常已过期;减少 N+1 TOS round-trip)
//     默认 fresh=true —— 每个 upload 调一次 Meta() (HeadObject + PreSignedURL)。
func (h *Handler) handleGetSession(c *gin.Context) {
	id := c.Param("id")
	sess, err := h.svc.GetSession(c.Request.Context(), id)
	if err != nil {
		failErr(c, err)
		return
	}

	freshURL := c.Query("fresh") != "false"

	// uploads: 默认重新生成 fresh URL;fresh=false 时跳过 Meta() (返回缓存 URL,可能过期)
	uploadMeta := make(map[string]uploads.Upload)
	if h.uploadStore != nil {
		list, _ := h.uploadStore.ListBySession(c.Request.Context(), id)
		for _, up := range list {
			payload := up
			if freshURL && h.uploads != nil {
				if meta, err := h.uploads.Meta(c.Request.Context(), up.ID); err == nil {
					payload = meta
				}
			}
			uploadMeta[payload.ID] = *payload
		}
	}

	view := SessionView{
		ID:        sess.ID,
		CreatedAt: sess.CreatedAt.Unix(),
		UpdatedAt: sess.UpdatedAt.Unix(),
		Messages:  make([]MessageView, 0, len(sess.History)),
	}
	if len(sess.History) > 0 {
		for _, message := range sess.History {
			viewMessage := fromHistoryMessage(message)
			refreshAttachmentViews(viewMessage.Attachments, uploadMeta)
			view.Messages = append(view.Messages, viewMessage)
		}
	} else {
		view.Messages = make([]MessageView, 0, len(sess.Messages))
		for index, m := range sess.Messages {
			messageID := fmt.Sprintf("%s:message:%d", sess.ID, index)
			viewMessage := fromEinoMessage(m, messageID, uploadMeta)
			refreshAttachmentViews(viewMessage.Attachments, uploadMeta)
			view.Messages = append(view.Messages, viewMessage)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"session": view,
	})
}

// handleDeleteSession DELETE /v1/sessions/:id —— 清除会话 + 级联删除该 session 的 upload。
func (h *Handler) handleDeleteSession(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.DeleteSession(c.Request.Context(), id); err != nil {
		failErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleListSessionUploads GET /v1/sessions/:id/uploads —— 列出某 session 的 upload。
//
// uploadStore 未配置(untracked 模式)时返回空数组,前端无感知。
// 返回的 URL 是 UploadStore 里存的(可能过期),前端要 fresh 时调:
//
//	GET /v1/uploads/:id/meta
func (h *Handler) handleListSessionUploads(c *gin.Context) {
	if h.uploadStore == nil {
		c.JSON(http.StatusOK, gin.H{"uploads": []any{}})
		return
	}
	id := c.Param("id")
	list, err := h.uploadStore.ListBySession(c.Request.Context(), id)
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]gin.H, 0, len(list))
	for _, u := range list {
		out = append(out, gin.H{
			"id":         u.ID,
			"url":        u.URL,
			"filename":   u.Filename,
			"mime_type":  u.MimeType,
			"size":       u.Size,
			"width":      u.Width,
			"height":     u.Height,
			"purpose":    u.Purpose,
			"session_id": u.SessionID,
			"expires_at": u.ExpiresAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"uploads": out})
}

// handleHealthz GET /healthz。
func (h *Handler) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
