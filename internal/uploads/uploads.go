// Package uploads 把客户端文件存到对象存储（TOS），返回可被上游生成 API 直接 fetch 的 URL。
//
// 为什么不存本机:
//   - 上游 API（方舟 Seedance/Seedream）需要 fetch 这个 URL；本机 URL 需要公网暴露
//   - SSRF 风险（任意 URL 都能塞进 reference_images）
//   - 多实例部署时本机不共享
//
// 直接走方舟 OSS: 上传后拿到的是 ark-cdn 域名的预签名 URL, 同区域内网可达。
package uploads

import (
	"context"
	"errors"
	"io"
	"time"
)

// Upload 单次上传产物。
//
// SessionID 非空时表示归属到某个对话会话,级联删除时会一并清理;
// 为空表示不索引的匿名上传(不会随 session 删除而被清理)。
type Upload struct {
	// ID 服务端生成的稳定 ID（用作 OSS 对象 key 的一部分）
	ID string `json:"id"`
	// URL 预签名公网 URL，可直接喂给上游生成 API
	URL string `json:"url"`
	// Filename 原始文件名（仅作展示用）
	Filename string `json:"filename"`
	// MimeType 实际 MIME（基于 magic bytes 检测，不是扩展名）
	MimeType string `json:"mime_type"`
	// Size 字节数
	Size int64 `json:"size"`
	// Width / Height 像素数（图片类型才有）
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Purpose 标签（reference / first_frame / last_frame / style）
	Purpose string `json:"purpose,omitempty"`
	// SessionID 归属会话 ID,空 = 匿名上传不索引
	SessionID string `json:"session_id,omitempty"`
	// ExpiresAt 预签名 URL 过期时间（unix 秒）
	ExpiresAt int64 `json:"expires_at"`
}

// Uploader 上传器接口。LocalUploader / ArkOSSUploader 等都满足。
type Uploader interface {
	// Save 把 reader 内容存到对象存储，返回元数据。
	Save(ctx context.Context, in SaveInput) (*Upload, error)
	// Delete 主动删除对象。
	Delete(ctx context.Context, id string) error
	// Meta 仅读元数据，不下载内容（基于 OSS HeadObject）。
	Meta(ctx context.Context, id string) (*Upload, error)
}

// SaveInput 上传入参。
type SaveInput struct {
	Filename  string
	MimeType  string
	Size      int64
	Reader    io.Reader
	Purpose   string
	SessionID string // 可选;非空时记录到 Upload.SessionID,便于级联删除
}

// ErrTooLarge 文件超过 max_size。
var ErrTooLarge = errors.New("upload too large")

// ErrUnsupportedMime MIME 不在白名单。
var ErrUnsupportedMime = errors.New("unsupported mime type")


// Option 可配置。
type Option func(*Config)

// Config 上传器配置。
type Config struct {
	Bucket     string
	Region     string
	Endpoint   string
	AccessKey  string
	SecretKey  string
	DirPrefix  string
	MaxSize    int64         // 0 = 不限
	AllowedMime []string     // nil = 不限
	PresignTTL time.Duration // 预签名 URL 有效期
}

// Whitelist 校验 MIME（大小写不敏感）。
func (c *Config) Whitelist(mime string) bool {
	if len(c.AllowedMime) == 0 {
		return true
	}
	for _, m := range c.AllowedMime {
		if equalFold(m, mime) {
			return true
		}
	}
	return false
}

// CheckSize 校验大小。
func (c *Config) CheckSize(n int64) error {
	if c.MaxSize > 0 && n > c.MaxSize {
		return ErrTooLarge
	}
	return nil
}

// BuildKey 生成 OSS 对象 key,包含用户指定的 dir prefix。
//
// 格式: <dir_prefix><uuid>.<ext>
func (c *Config) BuildKey(id, ext string) string {
	prefix := c.DirPrefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	if ext == "" {
		ext = ".bin"
	}
	return prefix + id + ext
}
