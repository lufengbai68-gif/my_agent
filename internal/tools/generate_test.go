package tools

import (
	"testing"

	"github.com/lufengbai68-gif/my_agent/internal/llm"
)

func TestApplyDefaults_ImageAspectRatio(t *testing.T) {
	req := &llm.GenerateRequest{}
	applyDefaults(req, "image")
	if req.AspectRatio != DefaultImageAspectRatio {
		t.Errorf("image aspect = %q, want %q", req.AspectRatio, DefaultImageAspectRatio)
	}
	// 其他字段不填
	if req.Resolution != "" || req.Duration != 0 || req.FPS != 0 {
		t.Errorf("image leaked video defaults: %+v", req)
	}
}

func TestApplyDefaults_VideoAllFields(t *testing.T) {
	req := &llm.GenerateRequest{}
	applyDefaults(req, "video")
	if req.AspectRatio != DefaultVideoAspectRatio {
		t.Errorf("video aspect = %q, want %q", req.AspectRatio, DefaultVideoAspectRatio)
	}
	if req.Resolution != DefaultVideoResolution {
		t.Errorf("video resolution = %q, want %q", req.Resolution, DefaultVideoResolution)
	}
	if req.Duration != DefaultVideoDuration {
		t.Errorf("video duration = %d, want %d", req.Duration, DefaultVideoDuration)
	}
	if req.FPS != DefaultVideoFPS {
		t.Errorf("video fps = %d, want %d", req.FPS, DefaultVideoFPS)
	}
}

func TestApplyDefaults_DoesNotOverrideUserValues(t *testing.T) {
	req := &llm.GenerateRequest{
		AspectRatio: "9:16",
		Resolution:  "1080p",
		Duration:    10,
		FPS:         30,
	}
	applyDefaults(req, "video")
	// 用户值原样保留
	if req.AspectRatio != "9:16" {
		t.Errorf("aspect overridden: %q", req.AspectRatio)
	}
	if req.Resolution != "1080p" {
		t.Errorf("resolution overridden: %q", req.Resolution)
	}
	if req.Duration != 10 {
		t.Errorf("duration overridden: %d", req.Duration)
	}
	if req.FPS != 30 {
		t.Errorf("fps overridden: %d", req.FPS)
	}
}

func TestApplyDefaults_PartialUserValues(t *testing.T) {
	// 用户只给了 aspect_ratio,其余走默认
	req := &llm.GenerateRequest{AspectRatio: "4:3"}
	applyDefaults(req, "video")
	if req.AspectRatio != "4:3" {
		t.Errorf("aspect overridden: %q", req.AspectRatio)
	}
	if req.Resolution != DefaultVideoResolution {
		t.Errorf("resolution not defaulted: %q", req.Resolution)
	}
	if req.Duration != DefaultVideoDuration {
		t.Errorf("duration not defaulted: %d", req.Duration)
	}
}

func TestApplyDefaults_DoesNotFillPreferenceFields(t *testing.T) {
	// seed / watermark / negative_prompt / image_url / camera_motion / n 这些
	// 不应该有"默认值"概念,留给上游或显式传入
	req := &llm.GenerateRequest{}
	applyDefaults(req, "video")
	if req.Seed != 0 {
		t.Errorf("seed should stay 0: %d", req.Seed)
	}
	if req.N != 0 {
		t.Errorf("n should stay 0: %d", req.N)
	}
	if req.Watermark != nil {
		t.Errorf("watermark should stay nil: %v", req.Watermark)
	}
	if req.NegativePrompt != "" {
		t.Errorf("negative_prompt should stay empty: %q", req.NegativePrompt)
	}
	if req.CameraMotion != "" {
		t.Errorf("camera_motion should stay empty: %q", req.CameraMotion)
	}
}

func TestApplyDefaults_UnknownKindDoesNothing(t *testing.T) {
	req := &llm.GenerateRequest{}
	applyDefaults(req, "audio")
	if req.AspectRatio != "" || req.Resolution != "" || req.Duration != 0 || req.FPS != 0 {
		t.Errorf("unknown kind leaked defaults: %+v", req)
	}
}

func TestApplyChatOpts_FillsAllFields(t *testing.T) {
	wm := false
	gp := &llm.GenerationParams{
		AspectRatio:    "16:9",
		NegativePrompt: "blur",
		Size:           "2048x2048",
		Resolution:     "720p",
		Width:          2048,
		Height:         2048,
		Duration:       5,
		FPS:            24,
		N:              2,
		Seed:           42,
		CameraMotion:   "static",
		Watermark:      &wm,
	}
	req := &llm.GenerateRequest{Prompt: "x"}
	applyChatOpts(req, gp)
	if req.AspectRatio != "16:9" || req.Size != "2048x2048" || req.Resolution != "720p" {
		t.Errorf("basic fields not filled: %+v", req)
	}
	if req.Duration != 5 || req.FPS != 24 || req.N != 2 || req.Seed != 42 {
		t.Errorf("numeric fields not filled: %+v", req)
	}
	if req.CameraMotion != "static" {
		t.Errorf("video fields not filled: %+v", req)
	}
	if req.NegativePrompt != "blur" {
		t.Errorf("negative_prompt not filled: %q", req.NegativePrompt)
	}
	if req.Watermark == nil || *req.Watermark != false {
		t.Errorf("watermark not filled (false explicit): %v", req.Watermark)
	}
}

func TestApplyChatOpts_NilDoesNothing(t *testing.T) {
	req := &llm.GenerateRequest{Prompt: "x"}
	applyChatOpts(req, nil)
	if req.AspectRatio != "" || req.Size != "" || req.Resolution != "" {
		t.Errorf("nil chatOpts should not modify req: %+v", req)
	}
}

func TestApplyChatOpts_DoesNotOverwriteNonZero(t *testing.T) {
	gp := &llm.GenerationParams{
		AspectRatio: "16:9",
		Duration:    5,
	}
	// req 已设置 AspectRatio / Duration
	req := &llm.GenerateRequest{
		Prompt:      "x",
		AspectRatio: "9:16", // LLM 已经填过(虽然不应,但兜底逻辑)
		Duration:    10,
	}
	applyChatOpts(req, gp)
	if req.AspectRatio != "9:16" {
		t.Errorf("non-zero aspect overwritten: %q", req.AspectRatio)
	}
	if req.Duration != 10 {
		t.Errorf("non-zero duration overwritten: %d", req.Duration)
	}
}
