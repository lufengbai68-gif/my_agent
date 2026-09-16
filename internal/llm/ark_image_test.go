package llm

import "testing"

func TestPickArkSize_Priority(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		ratio    string
		w, h     int
		want     string
	}{
		{"explicit size wins over ratio", "2048x1024", "16:9", 0, 0, "2048x1024"},
		{"explicit size wins over wh", "2048x2048", "", 800, 600, "2048x2048"},
		{"ratio derives when no explicit", "", "16:9", 0, 0, "2560x1440"},
		{"ratio wins over wh", "", "1:1", 800, 600, "2048x2048"},
		{"wh when no explicit/ratio", "", "", 800, 600, "800x600"},
		{"default 2048x2048 when nothing", "", "", 0, 0, "2048x2048"},
		{"unknown ratio passes through", "", "5:7", 0, 0, "5:7"},
		{"1:1 ratio", "", "1:1", 0, 0, "2048x2048"},
		{"9:16 vertical", "", "9:16", 0, 0, "1440x2560"},
		{"21:9 ultrawide", "", "21:9", 0, 0, "3360x1440"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickArkSize(tt.explicit, tt.ratio, tt.w, tt.h)
			if got != tt.want {
				t.Errorf("pickArkSize(%q,%q,%d,%d)=%q want %q",
					tt.explicit, tt.ratio, tt.w, tt.h, got, tt.want)
			}
		})
	}
}

func TestArkImageGenerate_AspectRatioMapping(t *testing.T) {
	// 验证 size 字段在 marshal 出去的请求体里正确。
	tests := []struct {
		ratio string
		want  string
	}{
		{"1:1", "2048x2048"},
		{"16:9", "2560x1440"},
		{"9:16", "1440x2560"},
	}
	for _, tt := range tests {
		t.Run(tt.ratio, func(t *testing.T) {
			req := arkImagesRequest{
				Model:  "ep-test",
				Prompt: "x",
				Size:   pickArkSize("", tt.ratio, 0, 0),
			}
			if req.Size != tt.want {
				t.Errorf("ratio %s -> size %q, want %q", tt.ratio, req.Size, tt.want)
			}
		})
	}
}

func TestPickArkSize_Minimum2K(t *testing.T) {
	// 所有 aspect_ratio 派生尺寸都必须 >= MinArkImagePixels (3,686,400 px)
	for ratio, sz := range aspectToSize {
		parts := splitWxH(sz)
		if len(parts) != 2 {
			t.Errorf("ratio %s -> size %q: not WxH format", ratio, sz)
			continue
		}
		w := atoiOrZero(parts[0])
		h := atoiOrZero(parts[1])
		if w*h < MinArkImagePixels {
			t.Errorf("ratio %s -> %s = %d px, below minimum %d", ratio, sz, w*h, MinArkImagePixels)
		}
	}
}

func TestPickArkSize_DefaultIs2K(t *testing.T) {
	got := pickArkSize("", "", 0, 0)
	if got != DefaultArkImageSize {
		t.Errorf("default = %q, want %q", got, DefaultArkImageSize)
	}
}

func TestValidateArkSize(t *testing.T) {
	tests := []struct {
		name    string
		size    string
		wantErr bool
	}{
		{"empty pass", "", false},
		{"valid 2048", "2048x2048", false},
		{"valid 2560x1440 (16:9 exact min)", "2560x1440", false},
		{"valid 4K", "4096x2160", false},
		{"reject 1024x1024", "1024x1024", true},
		{"reject 1920x1080 (below min)", "1920x1080", true},
		{"reject 1280x720", "1280x720", true},
		// k 格式 — 只接受 {2k, 3k, 4k} 大小写不限
		{"2k pass", "2k", false},
		{"3K pass (case insensitive)", "3K", false},
		{"4k pass", "4k", false},
		// 格式校验:之前透传给上游被拒的几种
		{"reject 720p (video resolution)", "720p", true},
		{"reject 16:9 (ratio belongs in aspect_ratio)", "16:9", true},
		{"reject 5k (not in {2,3,4}k)", "5k", true},
		{"reject abc", "abc", true},
		{"reject 2048 (no separator)", "2048", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArkSize(tt.size)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateArkSize(%q) err=%v, wantErr=%v", tt.size, err, tt.wantErr)
			}
			if err != nil {
				msg := err.Error()
				// WxH 像素不足 → 提示 Ark minimum
				// 格式错误 → 提示合法集合
				if !contains(msg, "Ark minimum") && !contains(msg, "must be one of") {
					t.Errorf("error missing actionable hint: %q", msg)
				}
			}
		})
	}
}

func TestValidateArkSize_ErrorMessageHelpful(t *testing.T) {
	err := validateArkSize("1024x1024")
	if err == nil {
		t.Fatal("expected error for 1024x1024")
	}
	// 必须告诉用户:
	//   1. 当前 size
	//   2. 当前像素数
	//   3. 最小要求
	//   4. 怎么修(用 aspect_ratio)
	for _, want := range []string{"1024x1024", "1048576", "3686400", "aspect_ratio"} {
		if !contains(err.Error(), want) {
			t.Errorf("error message missing %q: %q", want, err.Error())
		}
	}
}

func splitWxH(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == 'x' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return nil
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestNormalizeArkSize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"2K", "2k"},
		{"3K", "3k"},
		{"4K", "4k"},
		{"2k", "2k"}, // 已小写,原样
		{"2048x2048", "2048x2048"}, // WxH 不动
		{"2560x1440", "2560x1440"},
		{"5K", "5k"}, // 不在上游白名单也照样小写,由上游拒绝
		{"abc", "abc"}, // 末尾非 K,不动
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeArkSize(tt.in); got != tt.want {
				t.Errorf("normalizeArkSize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPickArkSize_NormalizesUppercaseK(t *testing.T) {
	// 用户传 "2K" 应自动规范为 "2k"
	got := pickArkSize("2K", "", 0, 0)
	if got != "2k" {
		t.Errorf("pickArkSize(%q) = %q, want %q", "2K", got, "2k")
	}
	// WxH 不会被改
	got = pickArkSize("2048x2048", "", 0, 0)
	if got != "2048x2048" {
		t.Errorf("pickArkSize(2048x2048) = %q, want unchanged", got)
	}
	// aspect_ratio 派生后也会被规范化（虽然派生结果都已是 WxH）
	got = pickArkSize("", "1:1", 0, 0)
	if got != "2048x2048" {
		t.Errorf("ratio 1:1 -> %q, want 2048x2048", got)
	}
}

func TestPartitionArkImageRefs(t *testing.T) {
	tests := []struct {
		name           string
		refs           []ReferenceImage
		wantSubject    []string
		wantStyle      []string
		wantGeneral    []string
	}{
		{
			name: "by role: subject/style/general",
			refs: []ReferenceImage{
				{URL: "u1", Role: "subject"},
				{URL: "u2", Role: "style"},
				{URL: "u3"},
				{URL: "u4", Role: "first_frame"}, // video role 退化到 general
				{URL: "u5", Role: "unknown"},
			},
			wantSubject: []string{"u1"},
			wantStyle:   []string{"u2"},
			wantGeneral: []string{"u3", "u4", "u5"},
		},
		{
			name: "case insensitive role matching",
			refs: []ReferenceImage{
				{URL: "u1", Role: "SUBJECT"},
				{URL: "u2", Role: "Style"},
			},
			wantSubject: []string{"u1"},
			wantStyle:   []string{"u2"},
		},
		{
			name: "empty URL is dropped",
			refs: []ReferenceImage{
				{URL: "", Role: "subject"},
				{URL: "u1", Role: "style"},
			},
			wantSubject: nil,
			wantStyle:   []string{"u1"},
		},
		{
			name:        "no refs",
			refs:        nil,
			wantSubject: nil,
			wantStyle:   nil,
			wantGeneral: nil,
		},
		{
			name: "multi subject / style only first kept (caller should pre-filter)",
			refs: []ReferenceImage{
				{URL: "s1", Role: "subject"},
				{URL: "s2", Role: "subject"},
				{URL: "y1", Role: "style"},
				{URL: "y2", Role: "style"},
			},
			wantSubject: []string{"s1", "s2"}, // 分桶阶段全保留;在 firstURL 取首条
			wantStyle:   []string{"y1", "y2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := partitionArkImageRefs(tt.refs)
			assertStringSlice(t, "subject", got.subject, tt.wantSubject)
			assertStringSlice(t, "style", got.style, tt.wantStyle)
			assertStringSlice(t, "general", got.general, tt.wantGeneral)
		})
	}
}

func TestFirstURL(t *testing.T) {
	if firstURL(nil) != "" {
		t.Errorf("nil slice should return empty string")
	}
	if firstURL([]string{}) != "" {
		t.Errorf("empty slice should return empty string")
	}
	if firstURL([]string{"a", "b"}) != "a" {
		t.Errorf("firstURL should return first element")
	}
}

func assertStringSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %d elements, want %d (%v)", label, len(got), len(want), got)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %q, want %q", label, i, got[i], want[i])
		}
	}
}
