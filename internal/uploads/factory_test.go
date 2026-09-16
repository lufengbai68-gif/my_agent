package uploads

import (
	"context"
	"strings"
	"testing"

	"github.com/lufengbai68-gif/my_agent/internal/config"
)

func TestNewFromConfig_UnknownStore(t *testing.T) {
	cfg := &config.UploadsConfig{Store: "nope"}
	_, err := NewFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unknown store")
	}
	if !strings.Contains(err.Error(), "unknown store") {
		t.Errorf("error should mention 'unknown store', got: %v", err)
	}
}

func TestNewFromConfig_NilConfig(t *testing.T) {
	_, err := NewFromConfig(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

// ark_oss 路径需要真实 bucket,在无集成测试环境下不应跑（HeadBucket 会失败）。
// 这里只验证它确实进入了 ark_oss 分支(返回的是 TOS 客户端初始化错误,不是 store unknown 错误)。
func TestNewFromConfig_ArkOSSMissingCreds(t *testing.T) {
	cfg := &config.UploadsConfig{
		Store:  "ark_oss",
		ArkOSS: config.ArkOSSConfig{Bucket: "x", Region: "cn-beijing", Endpoint: "https://x", AccessKey: "", SecretKey: ""},
	}
	_, err := NewFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when ark_oss creds missing")
	}
	if strings.Contains(err.Error(), "unknown store") {
		t.Errorf("should reach ark_oss branch, got: %v", err)
	}
}

func TestNewFromConfig_ArkOSSExplicitButNoBucket(t *testing.T) {
	// 用户显式 store=ark_oss 但缺 bucket,应返回明确错误(供 main.go 捕获并 log.Fatalf)。
	cfg := &config.UploadsConfig{Store: "ark_oss"}
	_, err := NewFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when ark_oss creds missing")
	}
	if strings.Contains(err.Error(), "unknown store") {
		t.Errorf("应进入 ark_oss 分支报错, 不该是 unknown store: %v", err)
	}
}

func TestNewFromConfig_EmptyStoreGoesArkOSS(t *testing.T) {
	// 工厂层对空 store 仍默认 ark_oss(便于纯工厂测试);
	// 但实际装配由 main.go 守卫:空 store == 未启用,不会调到这里。
	// 这里验证"调到了 ark_oss 分支",而不是 unknown store。
	cfg := &config.UploadsConfig{} // Store == ""
	_, err := NewFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error when no creds")
	}
	if strings.Contains(err.Error(), "unknown store") {
		t.Errorf("empty store should default to ark_oss, got: %v", err)
	}
}
