package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lufengbai68-gif/my_agent/internal/session"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
	"github.com/lufengbai68-gif/my_agent/internal/uploads/mime"
)

// allowedPurposes purpose 字段合法值(空 = 不传,允许)。
// 不在白名单 → 400 bad_request(避免任意字符串进元数据)。
var allowedPurposes = map[string]bool{
	"":            true,
	"reference":   true,
	"first_frame": true,
	"last_frame":  true,
	"style":       true,
}

// uploadHandler POST /v1/uploads (multipart/form-data)
//
// 表单字段:
//   - file: 图片文件(必填)
//   - purpose: reference | first_frame | last_frame | style (可选)
func (h *Handler) uploadHandler(c *gin.Context) {
	ctx := c.Request.Context()
	if h.uploads == nil {
		fail(c, http.StatusServiceUnavailable, "uploads_disabled", "uploads not configured")
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "missing_file", "file form field is required")
		return
	}
	purpose := c.PostForm("purpose")
	if !allowedPurposes[purpose] {
		slog.WarnContext(ctx, "upload_bad_purpose", "purpose", purpose)
		fail(c, http.StatusBadRequest, "bad_request",
			"purpose must be one of: reference, first_frame, last_frame, style (or empty)")
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		slog.ErrorContext(ctx, "upload_failed", "stage", "open", "filename", fileHeader.Filename, "err", err.Error())
		fail(c, http.StatusInternalServerError, "open_failed", err.Error())
		return
	}
	defer file.Close()

	// 用 magic bytes 检测 MIME（不能信扩展名）。先 peek 再 seek 回去。
	sniff := make([]byte, 32)
	n, _ := io.ReadFull(file, sniff)
	if seeker, ok := file.(io.Seeker); ok {
		_, _ = seeker.Seek(0, io.SeekStart)
	} else {
		// 兜底: 重新打开
		file.Close()
		file, _ = fileHeader.Open()
		_, _ = io.CopyN(io.Discard, file, int64(n))
	}
	mimeType := mime.Detect(bytes.NewReader(sniff[:n]))
	if mimeType == "" {
		mimeType = mime.DetectByFilename(fileHeader.Filename)
	}

	sessionID := c.GetHeader("X-Session-Id")

	slog.InfoContext(ctx, "upload_received",
		"filename", fileHeader.Filename,
		"size", fileHeader.Size,
		"purpose", purpose,
		"mime", mimeType,
		"has_session_id", sessionID != "",
	)

	if sessionID != "" {
		if _, err := h.svc.GetSession(ctx, sessionID); err != nil {
			if errors.Is(err, session.ErrNotFound) {
				slog.WarnContext(ctx, "upload_session_not_found", "session_id", sessionID)
				fail(c, http.StatusNotFound, "session_not_found",
					"X-Session-Id session does not exist; call POST /v1/chat/completions first to create")
				return
			}
			slog.ErrorContext(ctx, "upload_session_check_failed", "session_id", sessionID, "err", err.Error())
			failErr(c, err)
			return
		}
	}

	up, err := h.uploads.Save(ctx, uploads.SaveInput{
		Filename:  fileHeader.Filename,
		MimeType:  mimeType,
		Size:      fileHeader.Size,
		Reader:    file,
		Purpose:   purpose,
		SessionID: sessionID,
	})
	if err != nil {
		var code string
		switch {
		case errors.Is(err, uploads.ErrTooLarge):
			code = "too_large"
		case errors.Is(err, uploads.ErrUnsupportedMime):
			code = "unsupported_mime"
		default:
			code = "upload_failed"
		}
		slog.ErrorContext(ctx, "upload_failed",
			"stage", "save",
			"filename", fileHeader.Filename,
			"size", fileHeader.Size,
			"error_code", code,
			"err", err.Error(),
		)
		fail(c, http.StatusBadGateway, code, err.Error())
		return
	}

	slog.InfoContext(ctx, "upload_saved",
		"upload_id", up.ID,
		"session_id", up.SessionID,
		"purpose", up.Purpose,
		"width", up.Width,
		"height", up.Height,
		"size", up.Size,
	)

	// 写元数据索引(Save 成功后才写,失败时回滚 OSS 对象)。
	// 匿名上传也写入索引,便于首次 chat/completions 创建 session 后回绑。
	if h.uploadStore != nil {
		if err := h.uploadStore.Put(ctx, up); err != nil {
			// 元数据写失败:尽力回滚 OSS 对象(避免孤儿文件),返回 502。
			_ = h.uploads.Delete(ctx, up.ID)
			slog.ErrorContext(ctx, "upload_meta_persist_failed",
				"upload_id", up.ID, "session_id", sessionID, "err", err.Error(),
			)
			fail(c, http.StatusBadGateway, "meta_persist_failed", err.Error())
			return
		}
	}
	c.JSON(http.StatusCreated, up)
}

// deleteUploadHandler DELETE /v1/uploads/:id
func (h *Handler) deleteUploadHandler(c *gin.Context) {
	ctx := c.Request.Context()
	if h.uploads == nil {
		fail(c, http.StatusServiceUnavailable, "uploads_disabled", "uploads not configured")
		return
	}
	id := c.Param("id")
	if err := h.uploads.Delete(ctx, id); err != nil {
		if errors.Is(err, uploads.ErrNotFound) {
			fail(c, http.StatusNotFound, "not_found", "upload not found")
			return
		}
		slog.ErrorContext(ctx, "upload_delete_failed", "upload_id", id, "err", err.Error())
		fail(c, http.StatusBadGateway, "delete_failed", err.Error())
		return
	}
	slog.InfoContext(ctx, "upload_deleted", "upload_id", id)
	c.Status(http.StatusNoContent)
}

// metaUploadHandler GET /v1/uploads/:id/meta
func (h *Handler) metaUploadHandler(c *gin.Context) {
	ctx := c.Request.Context()
	if h.uploads == nil {
		fail(c, http.StatusServiceUnavailable, "uploads_disabled", "uploads not configured")
		return
	}
	id := c.Param("id")
	up, err := h.uploads.Meta(ctx, id)
	if err != nil {
		if errors.Is(err, uploads.ErrNotFound) {
			fail(c, http.StatusNotFound, "not_found", "upload not found")
			return
		}
		slog.ErrorContext(ctx, "upload_meta_failed", "upload_id", id, "err", err.Error())
		fail(c, http.StatusBadGateway, "meta_failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, up)
}
