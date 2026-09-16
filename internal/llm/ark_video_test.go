package llm

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildSeedanceParams_Empty(t *testing.T) {
	// 全部零值应返回 nil，不写入 parameters
	if got := buildSeedanceParams(GenerateRequest{}); got != nil {
		t.Errorf("empty req -> %+v, want nil", got)
	}
}

func TestBuildSeedanceParams_AspectRatioAndResolution(t *testing.T) {
	p := buildSeedanceParams(GenerateRequest{
		AspectRatio: "16:9",
		Resolution:  "720p",
		Duration:    5,
	})
	if p == nil {
		t.Fatal("expected non-nil params")
	}
	if p.Ratio != "16:9" {
		t.Errorf("ratio = %q", p.Ratio)
	}
	if p.Resolution != "720p" {
		t.Errorf("resolution = %q", p.Resolution)
	}
	if p.Duration != 5 {
		t.Errorf("duration = %d", p.Duration)
	}
}

func TestBuildSeedanceParams_SizeFallback(t *testing.T) {
	// size="16:9" 应被识别为 ratio
	p := buildSeedanceParams(GenerateRequest{Size: "16:9"})
	if p == nil || p.Ratio != "16:9" {
		t.Errorf("size as ratio: %+v", p)
	}
	// size="720p" 应被识别为 resolution
	p2 := buildSeedanceParams(GenerateRequest{Size: "720p"})
	if p2 == nil || p2.Resolution != "720p" {
		t.Errorf("size as resolution: %+v", p2)
	}
	// size="1024x1024" 既不是比例也不是分辨率，忽略
	if p3 := buildSeedanceParams(GenerateRequest{Size: "1024x1024"}); p3 != nil {
		t.Errorf("wh-size should be nil params: %+v", p3)
	}
}

func TestBuildSeedanceParams_AspectRatioBeatsSize(t *testing.T) {
	p := buildSeedanceParams(GenerateRequest{
		AspectRatio: "9:16",
		Size:        "16:9",
	})
	if p == nil || p.Ratio != "9:16" {
		t.Errorf("aspect_ratio should win over size: %+v", p)
	}
}

func TestBuildSeedanceParams_SeedRules(t *testing.T) {
	// 0 / 负数 seed 不传
	if p := buildSeedanceParams(GenerateRequest{Seed: 0}); p != nil {
		t.Errorf("seed=0 should drop: %+v", p)
	}
	if p := buildSeedanceParams(GenerateRequest{Seed: -1}); p != nil {
		t.Errorf("seed<0 should drop: %+v", p)
	}
	// 正整数 seed 写入
	p := buildSeedanceParams(GenerateRequest{Seed: 42})
	if p == nil || p.Seed != 42 {
		t.Errorf("seed=42 should write: %+v", p)
	}
}

func TestBuildSeedanceParams_CameraMotionStatic(t *testing.T) {
	p := buildSeedanceParams(GenerateRequest{CameraMotion: "static"})
	if p == nil || !p.CameraFixed {
		t.Errorf("camera_motion=static should set CameraFixed: %+v", p)
	}
	// 其他 camera_motion 不触发 camerafixed
	p2 := buildSeedanceParams(GenerateRequest{CameraMotion: "pan_left"})
	if p2 != nil {
		t.Errorf("camera_motion=pan_left should be nil params: %+v", p2)
	}
}

func TestBuildSeedanceParams_Watermark(t *testing.T) {
	tr := true
	fa := false
	pt := buildSeedanceParams(GenerateRequest{Watermark: &tr})
	if pt == nil || pt.Watermark == nil || !*pt.Watermark {
		t.Errorf("watermark=true should write: %+v", pt)
	}
	pf := buildSeedanceParams(GenerateRequest{Watermark: &fa})
	if pf == nil || pf.Watermark == nil || *pf.Watermark {
		t.Errorf("watermark=false should write false: %+v", pf)
	}
}

func TestSeedanceCreateReq_MarshalOmitsEmptyParams(t *testing.T) {
	// ImageURL 非空时 content 应包含 image_url
	req := seedanceCreateReq{
		Model: "Doubao-Seedance-2.5",
		Content: []seedanceCT{
			{Type: "text", Text: "wave"},
			{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/y.jpg"}},
		},
		Ratio:    buildSeedanceParams(GenerateRequest{AspectRatio: "16:9"}).Ratio,
		Duration: 5,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// 断言包含关键字段
	for _, want := range []string{
		`"model":"Doubao-Seedance-2.5"`,
		`"text":"wave"`,
		`"image_url":{"url":"https://x/y.jpg"}`,
		`"ratio":"16:9"`,
		`"duration":5`,
	} {
		if !contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestBuildSeedanceParams_AutoRatioMapsToAdaptive(t *testing.T) {
	p := buildSeedanceParams(GenerateRequest{AspectRatio: "auto", Duration: 4})
	if p == nil || p.Ratio != "adaptive" || p.Duration != 4 {
		t.Fatalf("auto ratio params = %+v", p)
	}
}

func TestSeedancePollDelay(t *testing.T) {
	tests := []struct {
		elapsed    time.Duration
		configured time.Duration
		want       time.Duration
	}{
		{elapsed: 0, configured: 2 * time.Second, want: 2 * time.Second},
		{elapsed: 29 * time.Second, configured: 2 * time.Second, want: 2 * time.Second},
		{elapsed: 31 * time.Second, configured: 2 * time.Second, want: 2 * time.Second},
		{elapsed: 31 * time.Second, configured: 10 * time.Second, want: 5 * time.Second},
		{elapsed: 2 * time.Minute, configured: 10 * time.Second, want: 8 * time.Second},
	}
	for _, tt := range tests {
		if got := pollDelay(tt.elapsed, tt.configured); got != tt.want {
			t.Fatalf("pollDelay(%s, %s) = %s, want %s", tt.elapsed, tt.configured, got, tt.want)
		}
	}
}

func TestSeedanceCreateReq_EmptyParamsOmitted(t *testing.T) {
	req := seedanceCreateReq{
		Model:   "Doubao-Seedance-2.5",
		Content: []seedanceCT{{Type: "text", Text: "x"}},
	}
	raw, _ := json.Marshal(req)
	if contains(string(raw), `"parameters"`) {
		t.Errorf("parameters should be omitted when nil: %s", raw)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestBuildSeedanceContent(t *testing.T) {
	tests := []struct {
		name string
		req  GenerateRequest
		want []seedanceCT
	}{
		{
			name: "prompt only",
			req:  GenerateRequest{Prompt: "hi"},
			want: []seedanceCT{{Type: "text", Text: "hi"}},
		},
		{
			name: "first_frame + last_frame + general ordering",
			req: GenerateRequest{
				Prompt: "hi",
				ReferenceImages: []ReferenceImage{
					{URL: "https://x/g1.jpg"},
					{URL: "https://x/first.jpg", Role: "first_frame"},
					{URL: "https://x/last.jpg", Role: "last_frame"},
					{URL: "https://x/g2.jpg"},
				},
			},
			want: []seedanceCT{
				{Type: "text", Text: "hi"},
				{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/first.jpg"}},
				{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/g1.jpg"}},
				{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/g2.jpg"}},
				{Type: "image_url_last", ImageURLLast: &seedanceImageURL{URL: "https://x/last.jpg"}},
			},
		},
		{
			name: "empty URL is skipped",
			req: GenerateRequest{
				Prompt: "hi",
				ReferenceImages: []ReferenceImage{
					{URL: "", Role: "first_frame"},
					{URL: "https://x/good.jpg"},
				},
			},
			want: []seedanceCT{
				{Type: "text", Text: "hi"},
				{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/good.jpg"}},
			},
		},
		{
			name: "duplicate first_frame keeps first",
			req: GenerateRequest{
				Prompt: "hi",
				ReferenceImages: []ReferenceImage{
					{URL: "https://x/a.jpg", Role: "first_frame"},
					{URL: "https://x/b.jpg", Role: "first_frame"},
				},
			},
			want: []seedanceCT{
				{Type: "text", Text: "hi"},
				{Type: "image_url", ImageURL: &seedanceImageURL{URL: "https://x/a.jpg"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSeedanceContent(tt.req)
			if !equalSeedanceCT(t, got, tt.want) {
				t.Logf("got:  %+v", got)
				t.Logf("want: %+v", tt.want)
				t.Fail()
			}
		})
	}
}

// equalSeedanceCT 顺序敏感的 deep equal,容错 nil 指针。
func equalSeedanceCT(t *testing.T, got, want []seedanceCT) bool {
	t.Helper()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		g := got[i]
		w := want[i]
		if g.Type != w.Type || g.Text != w.Text {
			return false
		}
		if (g.ImageURL == nil) != (w.ImageURL == nil) {
			return false
		}
		if g.ImageURL != nil && g.ImageURL.URL != w.ImageURL.URL {
			return false
		}
		if (g.ImageURLLast == nil) != (w.ImageURLLast == nil) {
			return false
		}
		if g.ImageURLLast != nil && g.ImageURLLast.URL != w.ImageURLLast.URL {
			return false
		}
	}
	return true
}
