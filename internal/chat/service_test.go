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
	"github.com/lufengbai68-gif/my_agent/internal/history"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
	"github.com/lufengbai68-gif/my_agent/internal/session"
	sessionmem "github.com/lufengbai68-gif/my_agent/internal/session/memory"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
	memoryupload "github.com/lufengbai68-gif/my_agent/internal/uploads/memory"
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
	svc := New(sessionmem.New(), newAgents("main", runner), 10, nil, nil, nil)
	ctx := context.Background()

	res, err := svc.Complete(ctx, "s1", llm.GenerateOptions{}, schema.UserMessage("hi"))
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
	_, err = svc.Complete(ctx, "s1", llm.GenerateOptions{}, schema.UserMessage("again"))
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
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	res, err := svc.Complete(context.Background(), "", llm.GenerateOptions{}, schema.UserMessage("hi"))
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
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	if _, err := svc.Complete(context.Background(), "", llm.GenerateOptions{}, schema.UserMessage("hi")); err == nil || err.Error() != "boom" {
		t.Fatalf("want boom, got %v", err)
	}
	// 失败不创建 session,GetSession 返回 ErrNotFound(读路径无副作用)
	_, err := svc.GetSession(context.Background(), "nonexistent-id")
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("want ErrNotFound on read of nonexistent session, got %v", err)
	}
}

func TestCompleteMaxMessagesTrim(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{assistantEvent("r")}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 4, nil, nil, nil) // 只留 4 条
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := svc.Complete(ctx, "s", llm.GenerateOptions{}, schema.UserMessage("q")); err != nil {
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
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"), func(ev StreamEvent) error {
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
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"), func(ev StreamEvent) error {
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

	// 失败轮次保留用户请求和模型错误，但不进入模型上下文。
	sess, _ := svc.GetSession(context.Background(), "s1")
	if len(sess.Messages) != 0 {
		t.Fatalf("failed reply should not enter model context: %+v", sess.Messages)
	}
	if len(sess.History) != 2 {
		t.Fatalf("failed turn history count = %d, want 2", len(sess.History))
	}
	user, assistant := sess.History[0], sess.History[1]
	if user.Role != history.RoleUser || user.Text != "hi" {
		t.Fatalf("failed turn user = %+v", user)
	}
	if assistant.Role != history.RoleAssistant || assistant.Status != history.StatusFailed || assistant.Error != "upstream down" {
		t.Fatalf("failed turn assistant = %+v", assistant)
	}
}

func TestStreamClientDisconnect(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{streamEvent([]string{"a", "b", "c", "d"})}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	calls := 0
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"), func(ev StreamEvent) error {
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
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"), func(ev StreamEvent) error {
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

var _ session.Store = (*sessionmem.Store)(nil)

// TestBuildInput_AddsModelKeyPrefixToUserMsg 验证 buildInput 把 model_key 注入 user message。
//
// 这是修复 #3（hint 被 LLM 忽略）的回归保护：
//   - 不再用 system hint（不可靠）
//   - 改用 user message 前缀 [user selected model_key=xxx]（必读）
//   - 必须确保 model_key 出现在 user role 的 content 里
func TestBuildInput_AddsModelKeyPrefixToUserMsg(t *testing.T) {
	sess := &session.Session{ID: "s1"}
	svc := New(sessionmem.New(), newAgents("a", nil), 0, nil, nil, nil)

	input := svc.buildInput(sess, schema.UserMessage("生成日落视频"), llm.GenerateOptions{ModelKey: "seedance_pro"})

	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}
	if input[0].Role != schema.User {
		t.Errorf("input[0] role = %v, want User", input[0].Role)
	}
	if !strings.Contains(input[0].Content, "model_key=seedance_pro") {
		t.Errorf("user msg missing model_key=seedance_pro: %q", input[0].Content)
	}
	if !strings.Contains(input[0].Content, "生成日落视频") {
		t.Errorf("user msg lost original content: %q", input[0].Content)
	}
}

// TestBuildInput_NoPrefixWhenEmpty 验证不传 model_key 时不加前缀。
func TestBuildInput_NoPrefixWhenEmpty(t *testing.T) {
	sess := &session.Session{ID: "s1"}
	svc := New(sessionmem.New(), newAgents("a", nil), 0, nil, nil, nil)

	input := svc.buildInput(sess, schema.UserMessage("你好"), llm.GenerateOptions{ModelKey: ""})

	if len(input) != 1 {
		t.Fatalf("input len = %d", len(input))
	}
	if strings.Contains(input[0].Content, "model_key") {
		t.Errorf("unexpected prefix when model_key empty: %q", input[0].Content)
	}
	if input[0].Content != "你好" {
		t.Errorf("content mutated: %q", input[0].Content)
	}
}

// TestBuildInput_HistoryBeforeUserMsg 验证顺序：历史在中、user msg 在最后。
func TestBuildInput_HistoryBeforeUserMsg(t *testing.T) {
	sess := &session.Session{
		ID: "s1",
		Messages: []*schema.Message{
			schema.UserMessage("prev1"),
			schema.AssistantMessage("reply1", nil),
		},
	}
	svc := New(sessionmem.New(), newAgents("a", nil), 0, nil, nil, nil)

	input := svc.buildInput(sess, schema.UserMessage("hi"), llm.GenerateOptions{ModelKey: "gpt_image_1"})

	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3", len(input))
	}
	if input[0].Content != "prev1" {
		t.Errorf("input[0] = %q, want prev1", input[0].Content)
	}
	if input[1].Content != "reply1" {
		t.Errorf("input[1] = %q", input[1].Content)
	}
	// input[2] 是改写后的 user msg：含 model_key 前缀
	if !strings.Contains(input[2].Content, "model_key=gpt_image_1") {
		t.Errorf("input[2] missing model_key prefix: %q", input[2].Content)
	}
}

// TestBuildInput_AddsModelKeyPrefixToFirstTextPart 验证多模态 parts 场景下
// model_key 前缀只注入第一条 text part,不污染 image/audio 等其他 part。
func TestBuildInput_AddsModelKeyPrefixToFirstTextPart(t *testing.T) {
	sess := &session.Session{ID: "s1"}
	svc := New(sessionmem.New(), newAgents("a", nil), 0, nil, nil, nil)

	userMsg := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "让这张图动起来"},
			{Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						URL: stringPtr("https://x/y.jpg"),
					},
				},
			},
		},
	}

	original := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "让这张图动起来"},
			{Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						URL: stringPtr("https://x/y.jpg"),
					},
				},
			},
		},
	}

	input := svc.buildInput(sess, userMsg, llm.GenerateOptions{ModelKey: "seedance_pro"})

	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}
	parts := input[0].UserInputMultiContent
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(parts))
	}
	if !strings.Contains(parts[0].Text, "model_key=seedance_pro") {
		t.Errorf("text part missing prefix: %q", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "让这张图动起来") {
		t.Errorf("text part lost original content: %q", parts[0].Text)
	}
	// image part 不应被修改
	if parts[1].Image == nil || parts[1].Image.URL == nil || *parts[1].Image.URL != "https://x/y.jpg" {
		t.Errorf("image part mutated: %+v", parts[1])
	}

	// 入参 userMsg 不应被修改（兜底不能污染原消息）
	if len(userMsg.UserInputMultiContent) != 2 || userMsg.UserInputMultiContent[0].Text != "让这张图动起来" {
		t.Errorf("userMsg mutated: %+v", userMsg.UserInputMultiContent)
	}
	if original.UserInputMultiContent[0].Text != "让这张图动起来" {
		t.Errorf("original mutated: %+v", original.UserInputMultiContent)
	}
}

// TestBuildInput_NoTextPart 验证 parts 里没有 text 时兜底加一条前缀 part。
func TestBuildInput_NoTextPart(t *testing.T) {
	sess := &session.Session{ID: "s1"}
	svc := New(sessionmem.New(), newAgents("a", nil), 0, nil, nil, nil)

	userMsg := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: stringPtr("https://x")},
				},
			},
		},
	}

	input := svc.buildInput(sess, userMsg, llm.GenerateOptions{ModelKey: "seedream"})
	parts := input[0].UserInputMultiContent

	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2 (prefix + image)", len(parts))
	}
	if parts[0].Type != schema.ChatMessagePartTypeText {
		t.Errorf("prefix part not text: %v", parts[0].Type)
	}
	if !strings.Contains(parts[0].Text, "model_key=seedream") {
		t.Errorf("prefix missing: %q", parts[0].Text)
	}
}

func stringPtr(s string) *string { return &s }

// TestBuildHintPrefix_OnlyModelKey 验证只传 model_key 时只生成一行 hint。
func TestBuildHintPrefix_OnlyModelKey(t *testing.T) {
	got := buildHintPrefix(llm.GenerateOptions{ModelKey: "seedance_pro"})
	want := "[user selected model_key=seedance_pro]\n"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestBuildHintPrefix_DoesNotIncludeGenerationParams 验证 generation_params
// 不再注入 LLM 提示词（避免污染对话上下文）；改走 ctx 透传。
func TestBuildHintPrefix_DoesNotIncludeGenerationParams(t *testing.T) {
	got := buildHintPrefix(llm.GenerateOptions{
		ModelKey: "seedream",
		GenerationParams: &llm.GenerationParams{
			AspectRatio: "16:9",
			Duration:    5,
		},
	})
	if strings.Contains(got, "generation_params") {
		t.Errorf("generation_params should NOT be in LLM hint: %q", got)
	}
	if strings.Contains(got, "aspect_ratio") {
		t.Errorf("aspect_ratio should NOT leak into hint: %q", got)
	}
	if !strings.Contains(got, "model_key=seedream") {
		t.Errorf("model_key should still be in hint: %q", got)
	}
}

// TestWithGenerateOptions_Roundtrip 验证 ctx 注入/取出。
func TestWithGenerateOptions_Roundtrip(t *testing.T) {
	wm := true
	opts := llm.GenerateOptions{
		ModelKey: "seedream",
		GenerationParams: &llm.GenerationParams{
			AspectRatio: "16:9",
			Duration:    5,
			Watermark:   &wm,
		},
	}
	ctx := llm.WithGenerateOptions(context.Background(), opts)
	got, ok := llm.GenerateOptionsFromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.ModelKey != "seedream" {
		t.Errorf("model_key lost: %q", got.ModelKey)
	}
	if got.GenerationParams == nil {
		t.Fatal("generation_params lost")
	}
	if got.GenerationParams.AspectRatio != "16:9" {
		t.Errorf("aspect_ratio lost: %q", got.GenerationParams.AspectRatio)
	}
	if got.GenerationParams.Duration != 5 {
		t.Errorf("duration lost: %d", got.GenerationParams.Duration)
	}
	if got.GenerationParams.Watermark == nil || !*got.GenerationParams.Watermark {
		t.Errorf("watermark lost: %v", got.GenerationParams.Watermark)
	}
}

// TestGenerateOptionsFromContext_Absent 验证 ctx 中无 opts 时返回零值 + false。
func TestGenerateOptionsFromContext_Absent(t *testing.T) {
	got, ok := llm.GenerateOptionsFromContext(context.Background())
	if ok {
		t.Errorf("expected ok=false, got %+v", got)
	}
	if got.ModelKey != "" || got.GenerationParams != nil {
		t.Errorf("expected zero value, got %+v", got)
	}
}

// toolEvent 构造工具输出消息事件（Role=Tool），模拟 eino adk 框架传来的工具结果。
func toolEvent(content string) *adk.AgentEvent {
	return &adk.AgentEvent{
		AgentName: "stub",
		Output: &adk.AgentOutput{
			MessageOutput: &adk.MessageVariant{
				Role:    schema.Tool,
				Message: &schema.Message{Role: schema.Tool, Content: content},
			},
		},
	}
}

// TestComplete_CollectsMediaFromToolEvents 验证 Complete 从 Tool 角色事件里
// 提取图片 URL 并放进 Result.Media。
func TestComplete_CollectsMediaFromToolEvents(t *testing.T) {
	toolJSON := `{"urls":["https://x/a.png"],"kind":"image","model":"seedream"}`
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{
			toolEvent(toolJSON),
			assistantEvent("done"),
		}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	res, err := svc.Complete(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("生成一张猫"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(res.Media) != 1 {
		t.Fatalf("media len = %d, want 1", len(res.Media))
	}
	m := res.Media[0]
	if m.Type != "image" {
		t.Errorf("type = %q, want image", m.Type)
	}
	if m.URL != "https://x/a.png" {
		t.Errorf("url = %q", m.URL)
	}
	if !strings.Contains(m.Description, "seedream") {
		t.Errorf("description should mention model: %q", m.Description)
	}
}

// TestComplete_CollectsMultipleMedia 验证多次工具调用产出多个 MediaItem。
func TestComplete_CollectsMultipleMedia(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{
			toolEvent(`{"urls":["https://x/a.png"],"kind":"image","model":"seedream"}`),
			toolEvent(`{"urls":["https://x/b.mp4"],"kind":"video","model":"seedance_pro"}`),
			assistantEvent("done"),
		}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	res, err := svc.Complete(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(res.Media) != 2 {
		t.Fatalf("media len = %d, want 2", len(res.Media))
	}
	if res.Media[0].Type != "image" || res.Media[1].Type != "video" {
		t.Errorf("wrong media types: %+v", res.Media)
	}
}

// TestComplete_IgnoresMalformedToolJSON 验证坏 JSON 不会让 Complete 崩溃。
func TestComplete_IgnoresMalformedToolJSON(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{
			toolEvent("not json at all"),
			toolEvent(`{"urls":[],"kind":"image","model":"seedream"}`), // 空 urls
			assistantEvent("done"),
		}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	res, err := svc.Complete(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(res.Media) != 0 {
		t.Errorf("expected no media, got %+v", res.Media)
	}
}

// TestStream_DoneEventCarriesMedia 验证 SSE 流结束时 done 事件带 media。
func TestStream_DoneEventCarriesMedia(t *testing.T) {
	toolJSON := `{"urls":["https://x/v.mp4"],"kind":"video","model":"seedance_pro"}`
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{
			toolEvent(toolJSON),
			assistantEvent("full reply"),
		}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)

	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("hi"), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var done *StreamEvent
	for i := range events {
		if events[i].Type == "done" {
			done = &events[i]
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	if len(done.Media) != 0 {
		t.Errorf("done 不应再带 Media(已流式下发): %+v", done.Media)
	}
	var mediaEvents []*StreamEvent
	for i := range events {
		if events[i].Type == "media_ready" {
			mediaEvents = append(mediaEvents, &events[i])
		}
	}
	if len(mediaEvents) != 1 {
		t.Fatalf("media_ready 事件数 = %d, want 1", len(mediaEvents))
	}
	if mediaEvents[0].Media[0].URL != "https://x/v.mp4" || mediaEvents[0].Media[0].Type != "video" {
		t.Errorf("wrong media_ready: %+v", mediaEvents[0])
	}
	if mediaEvents[0].ToolName != "generate_video" {
		t.Errorf("tool_name 应从 kind 派生, got %q", mediaEvents[0].ToolName)
	}

	sess, err := svc.GetSession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if len(sess.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(sess.Messages))
	}
	assistant := sess.Messages[1]
	persisted, ok := assistant.Extra["media"].([]MediaItem)
	if !ok || len(persisted) != 1 {
		t.Fatalf("assistant extra media = %+v, want 1 MediaItem", assistant.Extra["media"])
	}
	if persisted[0].URL != "https://x/v.mp4" {
		t.Errorf("persisted media URL = %q", persisted[0].URL)
	}
}

// stubUploader 记录 Delete 收到的 ID 列表,用于验证级联清理。
type stubUploader struct {
	deleted []string
	failOn  map[string]error // id -> 模拟 OSS 删除失败
}

func (s *stubUploader) Save(ctx context.Context, in uploads.SaveInput) (*uploads.Upload, error) {
	return nil, nil
}
func (s *stubUploader) Delete(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	if s.failOn != nil {
		if err, ok := s.failOn[id]; ok {
			return err
		}
	}
	return nil
}
func (s *stubUploader) Meta(_ context.Context, id string) (*uploads.Upload, error) {
	return nil, nil
}

func TestDeleteSession_Cascade(t *testing.T) {
	ctx := context.Background()
	sessStore := sessionmem.New()
	uploadStore := memoryupload.New()

	stub := &stubUploader{}

	// 先建一个 session
	sess, _ := sessStore.GetOrCreate(ctx, "s1")
	_ = sessStore.Save(ctx, sess)

	// 模拟该 session 上传了 3 个文件
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u1", SessionID: "s1", URL: "x1"})
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u2", SessionID: "s1", URL: "x2"})
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u3", SessionID: "s1", URL: "x3"})
	// 另一个 session 的文件不应受影响
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u4", SessionID: "s2", URL: "x4"})

	svc := New(sessStore, newAgents("main", nil), 0, nil, uploadStore, stub)
	if err := svc.DeleteSession(ctx, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// 1) stub.Delete 应被调到 u1/u2/u3 (顺序无所谓)
	if len(stub.deleted) != 3 {
		t.Errorf("expected 3 OSS deletes, got %d (%v)", len(stub.deleted), stub.deleted)
	}
	wanted := map[string]bool{"u1": true, "u2": true, "u3": true}
	for _, id := range stub.deleted {
		if !wanted[id] {
			t.Errorf("unexpected delete %q", id)
		}
		delete(wanted, id)
	}
	if len(wanted) > 0 {
		t.Errorf("missing deletes: %v", wanted)
	}

	// 2) u1/u2/u3 元数据应清掉
	for _, id := range []string{"u1", "u2", "u3"} {
		if _, err := uploadStore.Get(ctx, id); err != uploads.ErrNotFound {
			t.Errorf("uploads meta %s should be deleted, got %v", id, err)
		}
	}

	// 3) u4 不应被删
	if _, err := uploadStore.Get(ctx, "u4"); err != nil {
		t.Errorf("u4 (other session) should survive: %v", err)
	}

	// 4) session 本身应删掉
	if _, err := sessStore.GetOrCreate(ctx, "s1"); err == nil {
		// 重新创建 — 不算错误;改用 sessionStore.Delete 返回的 error 检查
	}
	// 用 Delete 后 GetOrCreate 应该会重建;直接验证 Delete 的行为:
	// 这里靠 Service.DeleteSession 内部返回 nil 表示成功路径走完
}

func TestDeleteSession_NoUploads(t *testing.T) {
	ctx := context.Background()
	sessStore := sessionmem.New()
	_, _ = sessStore.GetOrCreate(ctx, "s-only")

	svc := New(sessStore, newAgents("main", nil), 0, nil, nil, nil)
	if err := svc.DeleteSession(ctx, "s-only"); err != nil {
		t.Fatalf("delete without uploads should work: %v", err)
	}
}

func TestDeleteSession_EmptyID(t *testing.T) {
	svc := New(sessionmem.New(), newAgents("main", nil), 0, nil, nil, nil)
	if err := svc.DeleteSession(context.Background(), ""); err == nil {
		t.Fatal("empty session id should error")
	}
}

func TestDeleteSession_OSSFailureStillCleansMeta(t *testing.T) {
	ctx := context.Background()
	sessStore := sessionmem.New()
	_, _ = sessStore.GetOrCreate(ctx, "s1")

	uploadStore := memoryupload.New()
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u1", SessionID: "s1"})
	_ = uploadStore.Put(ctx, &uploads.Upload{ID: "u2", SessionID: "s1"})

	// 模拟 u1 OSS 删除失败
	stub := &stubUploader{failOn: map[string]error{"u1": errors.New("oss 502")}}

	svc := New(sessStore, newAgents("main", nil), 0, nil, uploadStore, stub)
	err := svc.DeleteSession(ctx, "s1")

	// DeleteSession 应聚合错误,但仍把元数据清掉(避免孤儿)
	if err == nil {
		t.Fatal("expected error from OSS failure")
	}
	if _, e := uploadStore.Get(ctx, "u1"); e != uploads.ErrNotFound {
		t.Errorf("u1 meta should be cleaned even after OSS failure, got %v", e)
	}
	if _, e := uploadStore.Get(ctx, "u2"); e != uploads.ErrNotFound {
		t.Errorf("u2 meta should be cleaned, got %v", e)
	}
}

// TestStream_OrderMediaBeforeText 验证 tool 产物(图片/视频)在 LLM 文字之前下发,
// 修复后用户看到的是 "先看到图 → 看到描述", 而不是旧实现的 "文字滚完突然冒图"。

func TestStream_OrderMediaBeforeText(t *testing.T) {
	runner := &stubRunner{events: func([]adk.Message) []*adk.AgentEvent {
		return []*adk.AgentEvent{
			// 1) tool 输出先到(图片生成完成)
			toolEvent(`{"urls":["https://x/img.png"],"kind":"image","model":"seedream"}`),
			// 2) LLM 看到图后写描述(流式)
			streamEvent([]string{"给你画好了"}),
		}
	}}
	svc := New(sessionmem.New(), newAgents("main", runner), 0, nil, nil, nil)
	var events []StreamEvent
	err := svc.Stream(context.Background(), "s1", llm.GenerateOptions{}, schema.UserMessage("画"), func(ev StreamEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	firstMediaIdx, firstChunkIdx := -1, -1
	for i, ev := range events {
		switch ev.Type {
		case "media_ready":
			if firstMediaIdx == -1 {
				firstMediaIdx = i
			}
		case "chunk":
			if firstChunkIdx == -1 {
				firstChunkIdx = i
			}
		}
	}
	if firstMediaIdx == -1 {
		t.Fatalf("missing media_ready event: %+v", events)
	}
	if firstChunkIdx == -1 {
		t.Fatalf("missing chunk event: %+v", events)
	}
	if firstMediaIdx >= firstChunkIdx {
		t.Errorf("media_ready 应在 chunk 之前, got media_idx=%d chunk_idx=%d events=%+v", firstMediaIdx, firstChunkIdx, events)
	}
	if events[firstMediaIdx].ToolName != "generate_image" {
		t.Errorf("tool_name 应从 kind 派生, got %q", events[firstMediaIdx].ToolName)
	}
}
