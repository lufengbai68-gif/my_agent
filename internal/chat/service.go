// Package chat 是编排核心：组装会话历史 → 调用 agent Runner →
// 装配响应（同步 JSON 或 SSE 流）。对上层（api）只暴露 Service。
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/history"
	"github.com/lufengbai68-gif/my_agent/internal/llm"
	"github.com/lufengbai68-gif/my_agent/internal/session"
	"github.com/lufengbai68-gif/my_agent/internal/uploads"
	uploadmime "github.com/lufengbai68-gif/my_agent/internal/uploads/mime"
)

// ErrNoReply 模型未产生任何 assistant 回复（异常情况）。
var ErrNoReply = errors.New("agent produced no assistant reply")

// AgentResolver 解析 agent（空名 → 默认）并返回实际生效的名字与 runner。
// *agent.Registry 即实现。
type AgentResolver interface {
	Resolve(name string) (string, agent.Runner, error)
}

// GenerationParams / GenerateOptions / ctx helpers 都定义在 llm 包,
// 这里只使用别名方便阅读(同包内不需要)。
//
// 关键不变量: 它不进 LLM 提示词,通过 llm.WithGenerateOptions 注入 ctx,
// 工具层用 llm.GenerateOptionsFromContext 还原。
// 这样 dispatcher LLM 只看用户消息 + model_key,保持提示词干净。

// Usage token 用量统计（镜像 schema.TokenUsage 的核心字段）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result 同步调用的结果。
type Result struct {
	SessionID string
	AgentName string
	Message   *schema.Message // 最终 assistant 消息（Content + ResponseMeta）
	Media     []MediaItem     // 工具调用产生的媒体 URL（图片/视频/音频）
	CreatedAt int64           // unix 秒
}

// StreamEvent SSE 流事件。
//
// 事件类型与字段约定(前端按 type 分发,不要假设事件相对顺序):
//
//   - chunk       增量文本(LLM 文字输出,按 token 流式下发)
//     Agent = 输出该段的 agent 名
//     前端: append 到当前 assistant bubble
//
//   - tool_start  工具调用开始(主 agent 调了 generate_image / generate_video 等)
//     ToolName = 工具名(model_key 在 hint 里,前端自行解析)
//     ToolCallID = 同一工具调用的多次事件的关联键
//     前端: 展示 loading 状态
//
//   - media_ready 单条媒体产物到位(图片 / 视频 URL 已生成)
//     ToolCallID = 与 tool_start 配对
//     Media = 单条 MediaItem
//     前端: 立即渲染该条 media; 不再等 done
//     重要: 同一次回复可能有多次 media_ready,
//     每次的 ToolCallID 不同,前端按 ToolCallID 配对
//
//   - done        整轮收尾(assistant 文本流已结束 + 所有 tool 完成)
//     FinishReason / Usage 仅在此处出现
//     不再携带 Media 字段(媒体已通过 media_ready 流式下发)
//     前端: 把已收齐的 chunk + media_ready 渲染到会话,
//     关闭所有 loading
//
//   - error       出错(整轮中断)
//     Message = 错误描述
//
// 顺序保证:
//   - tool_start 一定先于其对应的 media_ready
//   - chunk 与 media_ready 的相对顺序与生成时机一致
//     (LLM 可能先调 tool 拿到图后,再写文字描述;
//     前端看到的顺序就是「先 media_ready 再 chunk」)
type StreamEvent struct {
	Type         string      `json:"type"`
	SessionID    string      `json:"session_id,omitempty"`
	Agent        string      `json:"agent,omitempty"`
	Delta        string      `json:"delta,omitempty"`         // chunk
	ToolName     string      `json:"tool_name,omitempty"`     // tool_start
	ToolCallID   string      `json:"tool_call_id,omitempty"`  // tool_start / media_ready
	Media        []MediaItem `json:"media,omitempty"`         // media_ready: 单条
	FinishReason string      `json:"finish_reason,omitempty"` // done
	Usage        *Usage      `json:"usage,omitempty"`         // done
	Message      string      `json:"message,omitempty"`       // error
}

// Service 聊天服务。
type Service struct {
	store          session.Store
	agents         AgentResolver
	maxMsgs        int      // 每会话历史上限；0 = 不限
	allowedDomains []string // reference_images URL 白名单(空 slice = 拒绝)

	// 上传器相关(可选,nil 表示 uploads 未配置)。
	// uploadsMeta: 元数据索引,删除 session 时级联清理
	// uploader:    OSS 删除能力,级联清理时调 Delete 删对象
	uploadsMeta uploads.UploadStore
	uploader    uploads.Uploader
}

// New 创建服务。maxMessages <= 0 表示不限历史长度。
// allowedDomains 传入 reference_images[].url 允许的 host 后缀列表。
// uploadsMeta / uploader 可传 nil(nil 时 DeleteSession 不会级联清上传)。
func New(store session.Store, agents AgentResolver, maxMessages int, allowedDomains []string, uploadsMeta uploads.UploadStore, uploader uploads.Uploader) *Service {
	return &Service{
		store:          store,
		agents:         agents,
		maxMsgs:        maxMessages,
		allowedDomains: allowedDomains,
		uploadsMeta:    uploadsMeta,
		uploader:       uploader,
	}
}

// DeleteSession 删除会话及其级联资源(upload 文件 + OSS 对象)。
//
// 顺序: 列 uploads → 删 OSS 对象 → 清元数据 → 删 session
//
// 任一阶段失败不阻塞后续(best-effort), 所有错误聚合返回。
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	var errs []string

	// 1) 列 & 删 OSS 对象(如果配了 uploader)
	if s.uploadsMeta != nil {
		list, err := s.uploadsMeta.ListBySession(ctx, sessionID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("list uploads: %v", err))
		} else if s.uploader != nil {
			for _, up := range list {
				if err := s.uploader.Delete(ctx, up.ID); err != nil {
					// 单文件删失败不阻塞其他文件删除
					errs = append(errs, fmt.Sprintf("delete upload %s: %v", up.ID, err))
				}
			}
		}
		// 2) 清元数据(无论 OSS 删除是否成功,元数据一定要清掉)
		if _, err := s.uploadsMeta.DeleteBySession(ctx, sessionID); err != nil {
			errs = append(errs, fmt.Sprintf("delete uploads meta: %v", err))
		}
	}

	// 3) 删 session 本身
	if err := s.store.Delete(ctx, sessionID); err != nil {
		errs = append(errs, fmt.Sprintf("delete session: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("delete session %q: %s", sessionID, strings.Join(errs, "; "))
	}
	return nil
}

// Complete 同步完成一轮对话：装配历史 → 运行默认 agent → 拼接最终回复 → 持久化。
//
// userModelKey 是用户在请求里选的"生成模型"（如 seedance_pro）；
// 若非空，会作为前缀注入到 user message 第一条 text part，
// 提示主 agent dispatcher 路由到该生成工具。
func (s *Service) Complete(ctx context.Context, sessionID string, opts llm.GenerateOptions, userMsg *schema.Message) (result *Result, err error) {
	start := time.Now()
	slog.InfoContext(ctx, "chat_request",
		"session_id", sessionID,
		"model_key", opts.ModelKey,
		"stream", false,
		"msg_role", userMsg.Role,
	)
	defer func() {
		mediaCount := 0
		if result != nil {
			mediaCount = len(result.Media)
		}
		slog.InfoContext(ctx, "chat_request_done",
			"session_id", sessionID,
			"model_key", opts.ModelKey,
			"stream", false,
			"dur", time.Since(start),
			"media_count", mediaCount,
			"err", errString(err),
		)
	}()

	sess, err := s.prepare(ctx, &sessionID)
	if err != nil {
		return nil, err
	}
	var media []MediaItem
	persistFailure := func(partialText string, runErr error) error {
		if persistErr := s.persistFailure(ctx, sess, userMsg, partialText, runErr, media, opts); persistErr != nil {
			slog.ErrorContext(ctx, "failed_turn_persist_failed",
				"session_id", sess.ID,
				"run_err", errString(runErr),
				"persist_err", persistErr.Error(),
			)
		}
		return runErr
	}
	if err := s.bindUploadsToSession(ctx, sess.ID, userMsg); err != nil {
		return nil, persistFailure("", err)
	}

	runnerName, runner, err := s.agents.Resolve("")
	if err != nil {
		return nil, persistFailure("", err)
	}

	// opts 通过 ctx 透传给工具层(generation_params 不进 LLM 提示词)
	ctx = llm.WithGenerateOptions(ctx, opts)

	input := s.buildInput(sess, userMsg, opts)

	// 工具调用产生的媒体 URL — 从 Tool 角色消息的 JSON Content 里提取
	collectMedia := func(content string) {
		var out struct {
			URLs  []string `json:"urls"`
			Kind  string   `json:"kind"`
			Model string   `json:"model"`
		}
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			return
		}
		for _, u := range out.URLs {
			media = append(media, MediaItem{
				Type:        out.Kind,
				URL:         u,
				Description: "Generated by " + out.Model,
			})
		}
	}

	var final *schema.Message
	iter := runner.Run(ctx, input)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			return nil, persistFailure("", ev.Err)
		}
		// GetMessage 内部对流式事件做 Copy(2)+拼接，同步路径无需关心流
		msg, _, err := adk.GetMessage(ev)
		if err != nil {
			return nil, persistFailure("", err)
		}
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.Assistant:
			final = msg
		case schema.Tool:
			collectMedia(msg.Content)
		}
	}
	if final == nil {
		return nil, persistFailure("", ErrNoReply)
	}

	media = s.persistGeneratedMedia(ctx, sess.ID, media)
	if err := s.persist(ctx, sess, userMsg, final, media, opts); err != nil {
		return nil, err
	}
	return &Result{
		SessionID: sess.ID,
		AgentName: runnerName,
		Message:   final,
		Media:     media,
		CreatedAt: time.Now().Unix(),
	}, nil
}

// Stream 流式完成一轮对话：增量文本通过 sink 下发（SSE），
// 同时在内部拼接完整消息以便持久化历史。sink 返回 error 视为客户端断开，
// 立即停止并返回；已收到的部分不落库（半截回复对多轮上下文无意义）。
func (s *Service) Stream(ctx context.Context, sessionID string, opts llm.GenerateOptions, userMsg *schema.Message, sink func(StreamEvent) error) (err error) {
	start := time.Now()
	var media []MediaItem
	var streamedText strings.Builder
	slog.InfoContext(ctx, "chat_request",
		"session_id", sessionID,
		"model_key", opts.ModelKey,
		"stream", true,
		"msg_role", userMsg.Role,
	)
	defer func() {
		slog.InfoContext(ctx, "chat_request_done",
			"session_id", sessionID,
			"model_key", opts.ModelKey,
			"stream", true,
			"dur", time.Since(start),
			"media_count", len(media),
			"err", errString(err),
		)
	}()

	sess, err := s.prepare(ctx, &sessionID)
	if err != nil {
		return err
	}
	sendError := func(runErr error) error {
		_ = sink(StreamEvent{SessionID: sess.ID, Type: "error", Message: runErr.Error()})
		return runErr
	}
	persistFailure := func(partialText string, runErr error, media []MediaItem) error {
		if persistErr := s.persistFailure(ctx, sess, userMsg, partialText, runErr, media, opts); persistErr != nil {
			slog.ErrorContext(ctx, "failed_turn_persist_failed",
				"session_id", sess.ID,
				"run_err", errString(runErr),
				"persist_err", persistErr.Error(),
			)
		}
		return runErr
	}
	if err := s.bindUploadsToSession(ctx, sess.ID, userMsg); err != nil {
		persistFailure("", err, nil)
		return sendError(err)
	}

	runnerName, runner, err := s.agents.Resolve("")
	if err != nil {
		persistFailure("", err, nil)
		return sendError(err)
	}

	// opts 通过 ctx 透传给工具层(generation_params 不进 LLM 提示词)
	ctx = llm.WithGenerateOptions(ctx, opts)

	input := s.buildInput(sess, userMsg, opts)

	// 工具输出解析 + 立即下发 media_ready
	// 不再攒到 done —— 改流式下发,前端按事件顺序拼装,
	// 避免"LLM 文字在前 / tool 产物在后"变成"用户先看到文字再看到图"的反直觉体验
	handleToolMessage := func(toolCallID, content string) error {
		var out struct {
			URLs  []string `json:"urls"`
			Kind  string   `json:"kind"`
			Model string   `json:"model"`
		}
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			return nil
		}
		toolName := "generate_" + out.Kind
		if err := sink(StreamEvent{
			Type:       "tool_start",
			ToolName:   toolName,
			ToolCallID: toolCallID,
		}); err != nil {
			return err
		}
		for _, u := range out.URLs {
			item := MediaItem{
				Type:        out.Kind,
				URL:         u,
				Description: "Generated by " + out.Model,
			}
			media = append(media, item)
			if err := sink(StreamEvent{
				Type:       "media_ready",
				ToolName:   toolName,
				ToolCallID: toolCallID,
				Media:      []MediaItem{item},
			}); err != nil {
				return err
			}
		}
		return nil
	}

	var final *schema.Message
	iter := runner.Run(ctx, input)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			persistFailure(streamedText.String(), ev.Err, media)
			return sendError(ev.Err)
		}
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		mv := ev.Output.MessageOutput

		switch {
		case mv.IsStreaming && mv.MessageStream != nil:
			// 一路下发 SSE，一路拼接完整消息存历史
			ss := mv.MessageStream.Copy(2)
			var streamErr error
			for {
				chunk, err := ss[0].Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					streamErr = err
					break
				}
				if chunk.Content != "" {
					streamedText.WriteString(chunk.Content)
					if err := sink(StreamEvent{Type: "chunk", Agent: ev.AgentName, Delta: chunk.Content}); err != nil {
						ss[0].Close()
						ss[1].Close()
						return err
					}
				}
			}
			if streamErr != nil {
				ss[1].Close()
				persistFailure(streamedText.String(), streamErr, media)
				return sendError(streamErr)
			}
			merged, err := schema.ConcatMessageStream(ss[1])
			if err != nil {
				persistFailure(streamedText.String(), err, media)
				return sendError(err)
			}
			if merged.Role == schema.Assistant {
				final = merged
			}

		case mv.Role == schema.Assistant && mv.Message != nil:
			final = mv.Message
			// 非流式事件也以整体文本补发一次，保证客户端拿到完整内容
			if mv.Message.Content != "" {
				streamedText.WriteString(mv.Message.Content)
				if err := sink(StreamEvent{Type: "chunk", Agent: ev.AgentName, Delta: mv.Message.Content}); err != nil {
					return err
				}
			}

		case mv.Role == schema.Tool && mv.Message != nil:
			// 工具输出消息：解析产物, 立即以 media_ready 事件下发(不等 done)
			if err := handleToolMessage(mv.Message.ToolCallID, mv.Message.Content); err != nil {
				return err
			}
		}
		// 其余事件忽略
	}
	if final == nil {
		err := ErrNoReply
		persistFailure(streamedText.String(), err, media)
		return sendError(err)
	}

	media = s.persistGeneratedMedia(ctx, sess.ID, media)
	if err := s.persist(ctx, sess, userMsg, final, media, opts); err != nil {
		return sendError(err)
	}

	meta := final.ResponseMeta
	ev := StreamEvent{
		Type:      "done",
		SessionID: sess.ID,
		Agent:     runnerName,
		// Media 不再携带 —— 已通过 media_ready 流式下发过
	}
	if meta != nil {
		ev.FinishReason = meta.FinishReason
		if meta.Usage != nil {
			ev.Usage = &Usage{
				PromptTokens:     meta.Usage.PromptTokens,
				CompletionTokens: meta.Usage.CompletionTokens,
				TotalTokens:      meta.Usage.TotalTokens,
			}
		}
	}
	return sink(ev)
}

// GetSession 查询会话（不存在返回 session.ErrNotFound）。
//
// 读路径不允许副作用:不会自动创建空 session,避免 GET 类接口
// 污染 session 列表。客户端要新建会话应调 POST /v1/chat/completions
// (Complete/Stream 走 prepare 自动创建)。
func (s *Service) GetSession(ctx context.Context, id string) (*session.Session, error) {
	sess, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// SessionSummary session 摘要(给列表/侧边栏用,不含完整消息)。
type SessionSummary struct {
	ID           string
	CreatedAt    int64
	UpdatedAt    int64
	MessageCount int
	Preview      string // 第一条 user 消息文本(最长 80 字)
}

// ListSessions 返回所有 session 摘要,按 UpdatedAt 倒序。
//
// 每条摘要只含轻量元数据(不含 messages),适合侧边栏 / 多 session 切换场景。
// 上层如需完整内容,再调 GetSession / GetSessionComplete。
func (s *Service) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(all))
	for _, sess := range all {
		out = append(out, summarizeSession(sess))
	}
	return out, nil
}

// summarizeSession 提取 session 摘要字段。
//
// Preview 优先取第一条 user 消息纯文本;多模态首 part 也是文本则同样取。
// 截断 80 字符防止侧边栏爆框。
func summarizeSession(sess *session.Session) SessionSummary {
	sum := SessionSummary{
		ID:           sess.ID,
		CreatedAt:    sess.CreatedAt.Unix(),
		UpdatedAt:    sess.UpdatedAt.Unix(),
		MessageCount: len(sess.History),
	}
	for _, message := range sess.History {
		if message.Role != history.RoleUser {
			continue
		}
		text := message.Text
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > 80 {
			text = string(r[:80]) + "…"
		}
		sum.Preview = text
		break
	}
	return sum
}

// prepare 解析会话：空 ID 生成新 UUID，并确保会话存在。
func (s *Service) prepare(ctx context.Context, sessionID *string) (*session.Session, error) {
	if *sessionID == "" {
		*sessionID = uuid.NewString()
	}
	return s.store.GetOrCreate(ctx, *sessionID)
}

// buildInput 构造 agent 输入：复制历史 → 改写 user message（注入 hint）→返回。
//
// 关键设计：model_key / generation_params 通过 **user message 内容前缀** 注入，
// 而不是 system hint。
// 原因：LLM 倾向遵循历史模式（甚至错误模式），可能忽略 system hint。
// 把硬性指令写在 user message 内容前，确保 100% 可见、可遵循。
//
// 支持两种 user message 形态：
//   - 单 Content（纯文本）：前缀加到 Content 字符串前
//   - 多模态 Parts：在第一条 text part 的 text 前注入前缀（不动其他 part）
//
// 副作用：返回的 input 里 user message 是改写后的副本，但 persist 用的是原 userMsg，
// 所以 session 历史保持干净（不会污染后续轮的对话）。
func (s *Service) buildInput(sess *session.Session, userMsg *schema.Message, opts llm.GenerateOptions) []adk.Message {
	input := make([]adk.Message, 0, len(sess.Messages)+2)
	for _, m := range sess.Messages {
		input = append(input, m)
	}
	if hint := buildHintPrefix(opts); hint != "" {
		input = append(input, injectHint(userMsg, hint))
	} else {
		input = append(input, userMsg)
	}
	return input
}

// buildHintPrefix 把 GenerateOptions 拼成一段前缀字符串（只给 LLM 看 model_key）。
//
// 输出格式（每行一条 hint，换行分隔）：
//
//	[user selected model_key=xxx]
//
// generation_params 不注入 LLM 提示词：避免污染对话上下文，
// 改为通过 ctx 透传到工具层。LLM 只负责"调哪个工具 + 用什么 prompt"，
// generation_params 由前端 UI 选择直接喂到实际生成调用。
func buildHintPrefix(opts llm.GenerateOptions) string {
	var sb strings.Builder
	if opts.ModelKey != "" {
		fmt.Fprintf(&sb, "[user selected model_key=%s]\n", opts.ModelKey)
	}
	return sb.String()
}

// injectHint 构造带 hint 前缀的 user message 副本。
//
//   - 纯文本（无 parts）：前缀拼到 Content 前
//   - 多模态：在第一条 text part 的 text 前注入，原 parts 数组复制以避免污染 userMsg
//
// 返回的是新 Message，不修改入参。
func injectHint(userMsg *schema.Message, hint string) *schema.Message {
	if len(userMsg.UserInputMultiContent) == 0 {
		return &schema.Message{
			Role:    userMsg.Role,
			Content: hint + userMsg.Content,
			Extra:   userMsg.Extra,
		}
	}
	parts := make([]schema.MessageInputPart, len(userMsg.UserInputMultiContent))
	copy(parts, userMsg.UserInputMultiContent)
	for i := range parts {
		if parts[i].Type == schema.ChatMessagePartTypeText {
			parts[i].Text = hint + parts[i].Text
			return &schema.Message{
				Role:                  userMsg.Role,
				Content:               userMsg.Content,
				UserInputMultiContent: parts,
				Extra:                 userMsg.Extra,
			}
		}
	}
	// 没有 text part：兜底 prepend
	parts = append([]schema.MessageInputPart{{
		Type: schema.ChatMessagePartTypeText,
		Text: hint,
	}}, parts...)
	return &schema.Message{
		Role:                  userMsg.Role,
		Content:               userMsg.Content,
		UserInputMultiContent: parts,
		Extra:                 userMsg.Extra,
	}
}

// persist 追加用户消息与 assistant 回复并按上限裁剪后保存。
// 消息对象在落库后视为不可变（memory 实现按此纪律做浅拷贝隔离）。
func (s *Service) persist(ctx context.Context, sess *session.Session, userMsg, assistantMsg *schema.Message, media []MediaItem, opts llm.GenerateOptions) error {
	now := time.Now()
	turnID := history.NewID()
	request := buildRequestSnapshot(opts)
	userAttachments := s.buildUserAttachments(ctx, userMsg, opts)
	assistantAttachments := buildGeneratedAttachments(media)

	seq := len(sess.History) + 1
	user := history.Message{
		ID:          history.NewID(),
		TurnID:      turnID,
		RequestID:   turnID,
		Seq:         seq,
		Role:        history.RoleUser,
		Status:      history.StatusSuccess,
		Text:        messageText(userMsg),
		CreatedAt:   now.Unix(),
		Request:     request,
		Attachments: userAttachments,
	}
	assistant := history.Message{
		ID:          history.NewID(),
		TurnID:      turnID,
		RequestID:   turnID,
		Seq:         seq + 1,
		Role:        history.RoleAssistant,
		Status:      history.StatusSuccess,
		Text:        messageText(assistantMsg),
		CreatedAt:   now.Unix(),
		Request:     request,
		Attachments: assistantAttachments,
	}

	sess.History = append(sess.History, user, assistant)
	userMsg.Extra = setMessageExtra(userMsg.Extra, "created_at", now.Unix())
	assistantMsg.Extra = setMessageExtra(assistantMsg.Extra, "created_at", now.Unix())
	if len(media) > 0 {
		assistantMsg.Extra = setMessageExtra(assistantMsg.Extra, "media", media)
	}
	sess.Messages = append(sess.Messages, userMsg, assistantMsg)
	if s.maxMsgs > 0 && len(sess.Messages) > s.maxMsgs {
		sess.Messages = sess.Messages[len(sess.Messages)-s.maxMsgs:]
	}
	sess.UpdatedAt = now
	return s.store.Save(ctx, sess)
}

func (s *Service) persistFailure(
	ctx context.Context,
	sess *session.Session,
	userMsg *schema.Message,
	partialText string,
	runErr error,
	media []MediaItem,
	opts llm.GenerateOptions,
) error {
	media = s.persistGeneratedMedia(ctx, sess.ID, media)
	now := time.Now()
	turnID := history.NewID()
	request := buildRequestSnapshot(opts)
	errorText := runErr.Error()
	if errorText == "" {
		errorText = "generation failed"
	}

	sess.History = append(
		sess.History,
		history.Message{
			ID:          history.NewID(),
			TurnID:      turnID,
			RequestID:   turnID,
			Seq:         len(sess.History) + 1,
			Role:        history.RoleUser,
			Status:      history.StatusSuccess,
			Text:        messageText(userMsg),
			CreatedAt:   now.Unix(),
			Request:     request,
			Attachments: s.buildUserAttachments(ctx, userMsg, opts),
		},
		history.Message{
			ID:          history.NewID(),
			TurnID:      turnID,
			RequestID:   turnID,
			Seq:         len(sess.History) + 2,
			Role:        history.RoleAssistant,
			Status:      history.StatusFailed,
			Text:        partialText,
			Error:       errorText,
			CreatedAt:   now.Unix(),
			Request:     request,
			Attachments: buildGeneratedAttachments(media),
		},
	)
	sess.UpdatedAt = now
	return s.store.Save(ctx, sess)
}

func buildRequestSnapshot(opts llm.GenerateOptions) *history.Request {
	if opts.GenerationParams == nil {
		if opts.ModelKey == "" {
			return nil
		}
		return &history.Request{Model: opts.ModelKey}
	}

	params := *opts.GenerationParams
	mode := "image"
	if params.Duration > 0 {
		mode = "video"
	}
	generationType := params.GenerationType
	if generationType == "" && mode == "video" {
		for _, reference := range params.ReferenceImages {
			if reference.Role == "first_frame" || reference.Role == "last_frame" {
				generationType = "first_last_frame"
				break
			}
		}
		if generationType == "" {
			generationType = "all_in_one"
		}
	}
	return &history.Request{
		Model:          opts.ModelKey,
		Mode:           mode,
		GenerationType: generationType,
		Params:         &params,
	}
}

func messageText(message *schema.Message) string {
	if message == nil {
		return ""
	}
	if message.Content != "" {
		return message.Content
	}
	for _, part := range message.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			return part.Text
		}
	}
	return ""
}

func (s *Service) buildUserAttachments(ctx context.Context, userMsg *schema.Message, opts llm.GenerateOptions) []history.Attachment {
	roles := make(map[string]string)
	if opts.GenerationParams != nil {
		for _, reference := range opts.GenerationParams.ReferenceImages {
			if reference.UploadID != "" {
				roles[reference.UploadID] = reference.Role
			}
		}
	}

	attachments := make([]history.Attachment, 0)
	for index, part := range userMsg.UserInputMultiContent {
		if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil {
			continue
		}

		uploadID, _ := part.Extra["upload_id"].(string)
		fileName, _ := part.Extra["filename"].(string)
		attachment := history.Attachment{
			ID:        history.NewID(),
			Kind:      "reference",
			Type:      "image",
			URL:       strDerefService(part.Image.URL),
			FileName:  fileName,
			Purpose:   attachmentPurpose(roles[uploadID]),
			UploadID:  uploadID,
			SortOrder: index,
		}

		if uploadID != "" && s.uploadsMeta != nil {
			if upload, err := s.uploadsMeta.Get(ctx, uploadID); err == nil {
				attachment.URL = upload.URL
				if upload.Filename != "" {
					attachment.FileName = upload.Filename
				}
				if upload.MimeType != "" {
					attachment.MIMEType = upload.MimeType
				}
				if upload.Width > 0 {
					attachment.Width = upload.Width
				}
				if upload.Height > 0 {
					attachment.Height = upload.Height
				}
				attachment.ExpiresAt = upload.ExpiresAt
			}
		}
		attachments = append(attachments, attachment)
	}
	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

func buildGeneratedAttachments(media []MediaItem) []history.Attachment {
	if len(media) == 0 {
		return nil
	}
	attachments := make([]history.Attachment, 0, len(media))
	for index, item := range media {
		attachments = append(attachments, history.Attachment{
			ID:          history.NewID(),
			Kind:        "generated",
			Type:        item.Type,
			URL:         item.URL,
			Description: item.Description,
			Purpose:     "output",
			SortOrder:   index,
		})
	}
	return attachments
}

func attachmentPurpose(role string) string {
	switch role {
	case "first_frame", "last_frame", "style", "subject":
		return role
	default:
		return "reference"
	}
}

func strDerefService(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *Service) bindUploadsToSession(ctx context.Context, sessionID string, userMsg *schema.Message) error {
	if s.uploadsMeta == nil {
		return nil
	}

	for _, part := range userMsg.UserInputMultiContent {
		if part.Type != schema.ChatMessagePartTypeImageURL {
			continue
		}
		uploadID, _ := part.Extra["upload_id"].(string)
		if uploadID == "" {
			continue
		}
		upload, err := s.uploadsMeta.Get(ctx, uploadID)
		if errors.Is(err, uploads.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load upload %s: %w", uploadID, err)
		}
		if upload.SessionID == sessionID {
			continue
		}
		if upload.SessionID != "" && upload.SessionID != sessionID {
			return fmt.Errorf("upload %s already belongs to another session", uploadID)
		}
		upload.SessionID = sessionID
		if err := s.uploadsMeta.Put(ctx, upload); err != nil {
			return fmt.Errorf("bind upload %s to session: %w", uploadID, err)
		}
	}
	return nil
}

func (s *Service) persistGeneratedMedia(ctx context.Context, sessionID string, media []MediaItem) []MediaItem {
	if s.uploader == nil || len(media) == 0 {
		return media
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	for index, item := range media {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
		if err != nil {
			slog.WarnContext(ctx, "generated_media_copy_request_failed", "url", item.URL, "err", err.Error())
			continue
		}
		response, err := client.Do(request)
		if err != nil {
			slog.WarnContext(ctx, "generated_media_copy_failed", "url", item.URL, "err", err.Error())
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_ = response.Body.Close()
			slog.WarnContext(ctx, "generated_media_copy_status_failed", "url", item.URL, "status", response.StatusCode)
			continue
		}

		mimeType := response.Header.Get("Content-Type")
		if mimeType != "" {
			mimeType, _, _ = mime.ParseMediaType(mimeType)
		}
		if mimeType == "" {
			if item.Type == "video" {
				mimeType = "video/mp4"
			} else {
				mimeType = "image/png"
			}
		}

		extension := uploadmime.ExtFromMime(mimeType)
		if extension == "" {
			switch mimeType {
			case "video/mp4":
				extension = ".mp4"
			case "video/quicktime":
				extension = ".mov"
			case "video/webm":
				extension = ".webm"
			}
		}
		size := response.ContentLength
		if size < 0 {
			size = 0
		}
		upload, err := s.uploader.Save(ctx, uploads.SaveInput{
			Filename:  "generated-" + uuid.NewString() + extension,
			MimeType:  mimeType,
			Size:      size,
			Reader:    response.Body,
			Purpose:   "generated",
			SessionID: sessionID,
		})
		_ = response.Body.Close()
		if err != nil {
			slog.WarnContext(ctx, "generated_media_store_failed", "url", item.URL, "err", err.Error())
			continue
		}
		if s.uploadsMeta != nil {
			if err := s.uploadsMeta.Put(ctx, upload); err != nil {
				slog.WarnContext(ctx, "generated_media_meta_failed", "upload_id", upload.ID, "err", err.Error())
			}
		}
		item.URL = upload.URL
		item.UploadID = upload.ID
		media[index] = item
	}
	return media
}

func setMessageExtra(extra map[string]any, key string, value any) map[string]any {
	if extra == nil {
		extra = make(map[string]any)
	}
	extra[key] = value
	return extra
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
