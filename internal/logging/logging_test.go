package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestInit_LevelFilter(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, Config{Level: "info", Format: "json"})

	slog.Debug("should be filtered", "k", "v")
	slog.Info("should appear", "k", "v")

	out := buf.String()
	if strings.Contains(out, "should be filtered") {
		t.Errorf("debug log should be filtered at info level: %s", out)
	}
	if !strings.Contains(out, "should appear") {
		t.Errorf("info log should appear: %s", out)
	}
}

func TestRedactReplacer(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, Config{Level: "info", Format: "json", Redact: []string{"api_key", "authorization"}})

	slog.Info("auth attempt",
		"api_key", "sk-secret-12345",
		"authorization", "Bearer xyz",
		"username", "alice",
	)

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes())[:bytes.IndexByte(buf.Bytes(), '\n')], &rec); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if rec["api_key"] != "***REDACTED***" {
		t.Errorf("api_key not redacted: %v", rec["api_key"])
	}
	if rec["authorization"] != "***REDACTED***" {
		t.Errorf("authorization not redacted: %v", rec["authorization"])
	}
	if rec["username"] != "alice" {
		t.Errorf("non-redacted field changed: %v", rec["username"])
	}
}

func TestCtxHandler_AttachesRequestID(t *testing.T) {
	var buf bytes.Buffer
	InitTo(&buf, Config{Level: "info", Format: "json"})

	ctx := WithRequestID(t.Context(), "rid-test-123")
	slog.InfoContext(ctx, "hello")

	if !strings.Contains(buf.String(), `"request_id":"rid-test-123"`) {
		t.Errorf("request_id missing from log: %s", buf.String())
	}
}

func TestRequestID_Missing(t *testing.T) {
	if got := RequestID(t.Context()); got != "" {
		t.Errorf("expected empty rid, got %q", got)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		" warn ": slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"??":    slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
