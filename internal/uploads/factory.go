package uploads

import (
	"context"
	"fmt"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

// NewFromConfig 根据 uploads 配置构造上传器。
//
// 当前只支持 ark_oss;store 为空时默认走 ark_oss（与 config 默认值一致）。
// 配置缺失或初始化失败返回 error,调用方决定如何降级（disable / 进程退出）。
func NewFromConfig(ctx context.Context, cfg *config.UploadsConfig) (Uploader, error) {
	if cfg == nil {
		return nil, fmt.Errorf("uploads: nil config")
	}
	store := cfg.Store
	if store == "" {
		store = "ark_oss"
	}
	oss := cfg.ArkOSS
	switch store {
	case "ark_oss":
		return NewArkOSSUploader(ctx, Config{
			Bucket:      oss.Bucket,
			Region:      oss.Region,
			Endpoint:    oss.Endpoint,
			AccessKey:   oss.AccessKey,
			SecretKey:   oss.SecretKey,
			DirPrefix:   oss.DirPrefix,
			MaxSize:     cfg.MaxSize,
			AllowedMime: cfg.AllowedMime,
			PresignTTL:  oss.PresignTTL,
		})
	default:
		return nil, fmt.Errorf("uploads: unknown store %q", store)
	}
}
