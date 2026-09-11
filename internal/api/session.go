package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleGetSession GET /v1/sessions/:id —— 历史回显。
func (h *Handler) handleGetSession(c *gin.Context) {
	id := c.Param("id")
	sess, err := h.svc.GetSession(c.Request.Context(), id)
	if err != nil {
		failErr(c, err)
		return
	}

	view := SessionView{
		ID:        sess.ID,
		CreatedAt: sess.CreatedAt.Unix(),
		UpdatedAt: sess.UpdatedAt.Unix(),
		Messages:  make([]MessageView, 0, len(sess.Messages)),
	}
	for _, m := range sess.Messages {
		view.Messages = append(view.Messages, fromEinoMessage(m))
	}
	c.JSON(http.StatusOK, view)
}

// handleDeleteSession DELETE /v1/sessions/:id —— 清除会话。
func (h *Handler) handleDeleteSession(c *gin.Context) {
	id := c.Param("id")
	if err := h.svc.DeleteSession(c.Request.Context(), id); err != nil {
		failErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleListAgents GET /v1/agents —— 已注册 agent 列表。
func (h *Handler) handleListAgents(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"agents": h.agentNames()})
}

// handleHealthz GET /healthz。
func (h *Handler) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
