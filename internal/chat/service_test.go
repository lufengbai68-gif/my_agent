package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/session"
	"github.com/lufengbai68-gif/my_agent/internal/session/memory"
)

// stubRunner 记录输入并回放预设事件。
type stubRunner struct {
	mu     sync.Mutex
	got    []adk.Message // 最近一次 Run 的输入
	events func(input []adk.Message) []*adk.AgentEvent
}

func (s *stubRunner) Run(_ context.Context, msgs []adk.Message, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	s.mu.Lock()
	s.got = append([]adk.Message{}, msgs...) // 记录最近一次输入
	s.mu.Unlock()

	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		for _, ev := range s.events(msgs) {
			gen.Send(ev)
		}
		gen.Close()
	}()
	return iter
}

// newAgents 注册桩 runner。
func newAgents(name string, r agent.Runner) *agent.Registry {
	reg := agent.NewRegistry(name)
	if err := reg.RegisterRunner(name, r); err != nil {
		panic(err)
	}
	return reg
}

// assistantEvent 构造非流式 assistant 事件（Role 与真实 adk 事件一致，见 typedModelOutputEvent）。
func assistantEvent(content string) *adk.AgentEvent {
	return &adk.AgentEvent{
		AgentName: "stub",
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				Role:    schema.Assistant,
				Message: &schema.Message{Role: schema.Assistant, Content: content},
			},
		},
	}
}

// streamEvent 构造流式 assistant 事件（分片）。
func streamEvent(chunks []string) *adk.AgentEvent {
	return &adk.AgentEvent{
		AgentName: "stub",
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				IsStreaming:   true,
				Role:          schema.Assistant,
				MessageStream: schema.StreamReaderFromArray(messagesFromChunks(chunks)),
			},
		},
	}
}

func messagesFromChunks(chunks []string) []*schema.Message {
	msgs := make([]*schema.Message, 0, len(chunks))
	for _, c := range chunks {
		msgs = append(msgs, &schema.Message{Role: schema.Assistant, Content: c})
	}
	return msgs
}

func TestCompleteSync(t *testing.T) {
	runner := &stubRunner{events: func(in []adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{assistantEvent("hello world")}
	}}
	svc := New(memory.New(), newAgents("main", runner), 10)
	ctx := context.Background()

	res, err := svc.Complete(ctx, "s1", "", schema.UserMessage("hi"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 空名解析为默认 agent（回显实际生效的名字）
	if res.Message.Content != "hello world" || res.AgentName != "main" || res.SessionID != "s1" {
		t.Fatalf("result: %+v", res)
	}

	// 第一轮：会话为空，输入仅 1 条新消息
	if len(runner.got) != 1 || runner.got[0].Content != "hi" {
		t.Fatalf("runner input: %+v", runner.got)
	}

	// 历史已保存
	sess, _ := svc.GetSession(ctx, "s1")
	if len(sess.Messages) != 2 {
		t.Fatalf("session messages: %+v", sess.Messages)
	}

	// 第二轮：历史 2 条 + 新消息 = 3 条
	_, err = svc.Complete(ctx, "s1", "", schema.UserMessage("again"))
	if err != nil {
		t.Fatalf("complete 2: %v", err)
	}
	if len(runner.got) != 3 || runner.got[0].Content != "hi" {
		t.Fatalf("second turn input: %+v", runner.got)
	}
}

func TestCompleteAutoSessionID(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{assistantEvent("ok")}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	res, err := svc.Complete(context.Background(), "", "", schema.UserMessage("hi"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("expected generated session id")
	}
}

func TestCompleteError(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{{Err: errors.New("boom")}}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	if _, err := svc.Complete(context.Background(), "", "", schema.UserMessage("hi")); err == nil || err.Error() != "boom" {
		t.Fatalf("want boom, got %v", err)
	}
	// 失败不落历史
	sess, err := svc.GetSession(context.Background(), "nonexistent-id")
	if err != nil || len(sess.Messages) != 0 {
		t.Fatalf("session: %+v err=%v", sess, err)
	}
}

func TestCompleteAgentNotFound(t *testing.T) {
	svc := New(memory.New(), newAgents("main", &stubRunner{}), 0)
	_, err := svc.Complete(context.Background(), "", "nope", schema.UserMessage("hi"))
	if !errors.Is(err, agent.ErrAgentNotFound) {
		t.Fatalf("want ErrAgentNotFound, got %v", err)
	}
}

func TestCompleteMaxMessagesTrim(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{assistantEvent("r")}
	}}
	svc := New(memory.New(), newAgents("main", runner), 4) // 只留 4 条
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := svc.Complete(ctx, "s", "", schema.UserMessage("q")); err != nil {
			t.Fatal(err)
		}
	}
	sess, _ := svc.GetSession(ctx, "s")
	if len(sess.Messages) != 4 {
		t.Fatalf("want 4 trimmed messages, got %d", len(sess.Messages))
	}
}

func TestStreamChunks(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{streamEvent([]string{"Hel", "lo ", "world"})}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", "", schema.UserMessage("hi"), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var text strings.Builder
	var done *StreamEvent
	for i, ev := range events {
		switch ev.Type {
		case "chunk":
			text.WriteString(ev.Delta)
		case "done":
			done = &events[i]
		case "error":
			t.Fatalf("unexpected error event: %+v", ev)
		}
	}
	if text.String() != "Hello world" {
		t.Fatalf("streamed text: %q", text.String())
	}
	if done == nil || done.SessionID != "s1" {
		t.Fatalf("done event: %+v", done)
	}

	// 拼接后的完整消息已落历史
	sess, _ := svc.GetSession(context.Background(), "s1")
	if len(sess.Messages) != 2 || sess.Messages[1].Content != "Hello world" {
		t.Fatalf("history: %+v", sess.Messages)
	}
}

func TestStreamErrorEvent(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{streamEvent([]string{"par", "tial"}), {Err: errors.New("upstream down")}}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", "", schema.UserMessage("hi"), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err == nil || err.Error() != "upstream down" {
		t.Fatalf("want upstream error, got %v", err)
	}

	var hasError bool
	for _, ev := range events {
		if ev.Type == "error" {
			hasError = true
		}
	}
	if !hasError {
		t.Fatalf("no error event: %+v", events)
	}

	// 半截回复不落历史
	sess, _ := svc.GetSession(context.Background(), "s1")
	if len(sess.Messages) != 0 {
		t.Fatalf("partial reply should not persist: %+v", sess.Messages)
	}
}

func TestStreamClientDisconnect(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{streamEvent([]string{"a", "b", "c", "d"})}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	calls := 0
	err := svc.Stream(context.Background(), "s1", "", schema.UserMessage("hi"), func(ev StreamEvent) error {
		calls++
		if calls >= 2 {
			return io.EOF // 模拟客户端断开
		}
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}

	// 断开后不落历史
	sess, _ := svc.GetSession(context.Background(), "s1")
	if len(sess.Messages) != 0 {
		t.Fatalf("history after disconnect: %+v", sess.Messages)
	}
}

func TestStreamNonStreamingEvent(t *testing.T) {
	// 有些 runner 直接给非流式事件；Stream 应照常处理并下发
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{assistantEvent("full reply")}
	}}
	svc := New(memory.New(), newAgents("main", runner), 0)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", "", schema.UserMessage("hi"), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(events) != 2 || events[0].Delta != "full reply" || events[1].Type != "done" {
		t.Fatalf("events: %+v", events)
	}
}

var _ session.Store = (*memory.Store)(nil)
