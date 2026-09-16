package chat

import (
	"testing"

	"github.com/lufengbai68-gif/my_agent/internal/history"
	"github.com/lufengbai68-gif/my_agent/internal/session"
)

func TestSummarizeSession_PreviewFromFirstUserMessage(t *testing.T) {
	sess := &session.Session{
		ID: "s1",
		History: []history.Message{
			{Role: history.RoleUser, Text: "画一只在月球上喝咖啡的猫"},
			{Role: history.RoleAssistant, Text: "好的"},
		},
	}
	sum := summarizeSession(sess)
	if sum.ID != "s1" {
		t.Errorf("id: %q", sum.ID)
	}
	if sum.MessageCount != 2 {
		t.Errorf("msg count: %d", sum.MessageCount)
	}
	if sum.Preview != "画一只在月球上喝咖啡的猫" {
		t.Errorf("preview: %q", sum.Preview)
	}
}

func TestSummarizeSession_LongPreviewTruncated(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	sess := &session.Session{
		History: []history.Message{{Role: history.RoleUser, Text: long}},
	}
	sum := summarizeSession(sess)
	if len([]rune(sum.Preview)) > 81 { // 80 + …
		t.Errorf("preview should be truncated, got %d chars", len([]rune(sum.Preview)))
	}
}

func TestSummarizeSession_NoUserMessage(t *testing.T) {
	sess := &session.Session{
		History: []history.Message{{Role: history.RoleAssistant, Text: "hi"}},
	}
	sum := summarizeSession(sess)
	if sum.Preview != "" {
		t.Errorf("preview should be empty, got %q", sum.Preview)
	}
}
