package uploads

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/volcengine/ve-tos-golang-sdk/tos"

	"github.com/lufengbai68-gif/my_agent/internal/uploads/mime"
)

// ArkOSSUploader 火山引擎对象存储（TOS）上传器。
//
// 调用流程:
//   Save -> PutObject 到 bucket -> PreSignedURL(GET, ttl) -> 拼成 Upload
//
// 文件元数据(mime/width/height/purpose)写进 OSS 对象自定义 meta header
// (x-tos-meta-*)。Meta() 调 HeadObject 直接读, 不需要额外 DB。
type ArkOSSUploader struct {
	cfg    Config
	client *tos.Client
	bucket *tos.Bucket
}

// NewArkOSSUploader 创建 TOS 客户端并连通一次（HeadBucket）。
//
// 失败立刻返回: 启动时连不上不应让进程继续跑（缺依赖就不能工作）。
func NewArkOSSUploader(ctx context.Context, cfg Config) (*ArkOSSUploader, error) {
	if cfg.Bucket == "" || cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("ark_oss: bucket/endpoint/ak/sk are required")
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = time.Hour
	}
	if cfg.Region == "" {
		cfg.Region = "cn-beijing"
	}

	cred := tos.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey)
	client, err := tos.NewClient(cfg.Endpoint,
		tos.WithCredentials(cred),
		tos.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("ark_oss: new client: %w", err)
	}

	if _, err := client.HeadBucket(ctx, cfg.Bucket); err != nil {
		return nil, fmt.Errorf("ark_oss: head bucket %q: %w", cfg.Bucket, err)
	}
	slog.InfoContext(ctx, "ark_oss_init_ok",
		"bucket", cfg.Bucket,
		"region", cfg.Region,
		"endpoint", cfg.Endpoint,
		"dir_prefix", cfg.DirPrefix,
	)

	bkt, _ := client.Bucket(cfg.Bucket)
	return &ArkOSSUploader{
		cfg:    cfg,
		client: client,
		bucket: bkt,
	}, nil
}

// Save 上传文件,返回预签名 URL。
func (u *ArkOSSUploader) Save(ctx context.Context, in SaveInput) (*Upload, error) {
	if err := u.cfg.CheckSize(in.Size); err != nil {
		return nil, err
	}
	if in.MimeType == "" {
		in.MimeType = mime.DetectByFilename(in.Filename)
	}
	if !u.cfg.Whitelist(in.MimeType) {
		return nil, ErrUnsupportedMime
	}

	id := uuid.NewString()
	ext := filepath.Ext(in.Filename)
	if ext == "" {
		ext = mime.ExtFromMime(in.MimeType)
	}
	key := u.cfg.BuildKey(id, ext)

	opts := []tos.Option{tos.WithContentType(in.MimeType)}
	for k, v := range metaHeaders(id, in.MimeType, in.Filename, in.Purpose, in.SessionID, 0, 0) {
		opts = append(opts, tos.WithMeta(k, v))
	}

	if _, err := u.bucket.PutObject(ctx, key, in.Reader, opts...); err != nil {
		slog.ErrorContext(ctx, "oss_put_failed",
			"key", key,
			"filename", in.Filename,
			"size", in.Size,
			"err", err.Error(),
		)
		return nil, fmt.Errorf("ark_oss: put object: %w", err)
	}
	slog.InfoContext(ctx, "oss_put_ok",
		"key", key,
		"upload_id", id,
		"size", in.Size,
		"mime", in.MimeType,
	)

	// 探测图片宽高 (PNG/JPEG/WebP)。非图或失败不阻塞
	w, h := mime.Dimensions(in.Reader, in.MimeType)
	if w > 0 && h > 0 {
		_, _ = u.bucket.SetObjectMeta(ctx, key,
			tos.WithMeta("width", fmt.Sprintf("%d", w)),
			tos.WithMeta("height", fmt.Sprintf("%d", h)),
		)
	}

	url, err := u.client.PreSignedURL("GET", u.cfg.Bucket, key, u.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("ark_oss: presign url: %w", err)
	}

	return &Upload{
		ID:        id,
		URL:       url,
		Filename:  in.Filename,
		MimeType:  in.MimeType,
		Size:      in.Size,
		Width:     w,
		Height:    h,
		Purpose:   in.Purpose,
		SessionID: in.SessionID,
		ExpiresAt: time.Now().Add(u.cfg.PresignTTL).Unix(),
	}, nil
}

// Delete 删对象（按 ID 反查 prefix + DeleteObject）。
func (u *ArkOSSUploader) Delete(ctx context.Context, id string) error {
	key, err := u.resolveKey(ctx, id)
	if err != nil {
		return err
	}
	if _, err := u.bucket.DeleteObject(ctx, key); err != nil {
		slog.WarnContext(ctx, "oss_delete_failed", "upload_id", id, "key", key, "err", err.Error())
		return fmt.Errorf("ark_oss: delete: %w", err)
	}
	return nil
}

// Meta 读对象元数据 (不下载内容)。
func (u *ArkOSSUploader) Meta(ctx context.Context, id string) (*Upload, error) {
	key, err := u.resolveKey(ctx, id)
	if err != nil {
		return nil, err
	}
	head, err := u.bucket.HeadObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("ark_oss: head: %w", err)
	}

	mimeType := head.Metadata["mime"]
	if mimeType == "" {
		mimeType = head.ContentType
	}
	w, _ := parseInt(head.Metadata["width"])
	h, _ := parseInt(head.Metadata["height"])
	sessionID := head.Metadata["session-id"]

	url, err := u.client.PreSignedURL("GET", u.cfg.Bucket, key, u.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("ark_oss: presign: %w", err)
	}

	return &Upload{
		ID:        id,
		URL:       url,
		Filename:  head.Metadata["original-fn"],
		MimeType:  mimeType,
		Size:      head.ContentLength,
		Width:     w,
		Height:    h,
		Purpose:   head.Metadata["purpose"],
		SessionID: sessionID,
		ExpiresAt: time.Now().Add(u.cfg.PresignTTL).Unix(),
	}, nil
}

// resolveKey 用 ID 反查 OSS key (ListObjects prefix 匹配)。
func (u *ArkOSSUploader) resolveKey(ctx context.Context, id string) (string, error) {
	prefix := u.cfg.DirPrefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	out, err := u.bucket.ListObjects(ctx, &tos.ListObjectsInput{
		Prefix:  prefix + id,
		MaxKeys: 1,
	})
	if err != nil {
		return "", fmt.Errorf("ark_oss: list: %w", err)
	}
	if len(out.Contents) == 0 {
		return "", ErrNotFound
	}
	return out.Contents[0].Key, nil
}

func parseInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid int: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// metaHeaders 把上传元数据转成 OSS meta header (x-tos-meta-*)。
func metaHeaders(id, mimeType, filename, purpose, sessionID string, w, h int) map[string]string {
	m := map[string]string{
		"upload-id":   id,
		"mime":        mimeType,
		"original-fn": sanitizeFilename(filename),
	}
	if purpose != "" {
		m["purpose"] = purpose
	}
	if sessionID != "" {
		m["session-id"] = sessionID
	}
	if w > 0 {
		m["width"] = fmt.Sprintf("%d", w)
	}
	if h > 0 {
		m["height"] = fmt.Sprintf("%d", h)
	}
	return m
}

// sanitizeFilename OSS meta header 不允许换行/控制字符
func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
