// Package chat 是编排核心：组装会话历史 → 调用 agent Runner →
// 装配响应（同步 JSON 或 SSE 流）。对上层（api）只暴露 Service。
package chat

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/lufengbai68-gif/my_agent/internal/agent"
	"github.com/lufengbai68-gif/my_agent/internal/session"
)

// ErrNoReply 模型未产生任何 assistant 回复（异常情况）。
var ErrNoReply = errors.New("agent produced no assistant reply")

// AgentResolver 解析 agent（空名 → 默认）并返回实际生效的名字与 runner。
// *agent.Registry 即实现。
type AgentResolver interface {
	Resolve(name string) (string, agent.Runner, error)
}

// Usage token 用量统计（镜像 schema.TokenUsage 的核心字段）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result 同步调用的结果。
type Result struct {
	SessionID  string
	AgentName  string
	Message    *schema.Message // 最终 assistant 消息（Content + ResponseMeta）
	CreatedAt  int64           // unix 秒
}

// StreamEvent SSE 流事件。Type: chunk（增量文本）| done（收尾）| error（失败）。
type StreamEvent struct {
	Type         string `json:"type"`
	SessionID    string `json:"session_id,omitempty"`
	Agent        string `json:"agent,omitempty"`
	Delta        string `json:"delta,omitempty"`          // chunk
	FinishReason string `json:"finish_reason,omitempty"`  // done
	Usage        *Usage `json:"usage,omitempty"`           // done
	Message      string `json:"message,omitempty"`        // error
}

// Service 聊天服务。
type Service struct {
	store   session.Store
	agents  AgentResolver
	maxMsgs int // 每会话历史上限；0 = 不限
}

// New 创建服务。maxMessages <= 0 表示不限历史长度。
func New(store session.Store, agents AgentResolver, maxMessages int) *Service {
	return &Service{store: store, agents: agents, maxMsgs: maxMessages}
}

// Complete 同步完成一轮对话：装配历史 → 运行 agent → 拼接最终回复 → 持久化。
func (s *Service) Complete(ctx context.Context, sessionID, agentName string, userMsg *schema.Message) (*Result, error) {
	sess, err := s.prepare(ctx, &sessionID)
	if err != nil {
		return nil, err
	}

	runnerName, runner, err := s.agents.Resolve(agentName)
	if err != nil {
		return nil, err
	}

	// 复制历史切片，绝不原地修改存储中的会话
	input := make([]adk.Message, 0, len(sess.Messages)+1)
	for _, m := range sess.Messages {
		input = append(input, m)
	}
	input = append(input, userMsg)

	var final *schema.Message
	iter := runner.Run(ctx, input)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			return nil, ev.Err
		}
		// GetMessage 内部对流式事件做 Copy(2)+拼接，同步路径无需关心流
		msg, _, err := adk.GetMessage(ev)
		if err != nil {
			return nil, err
		}
		if msg != nil && msg.Role == schema.Assistant {
			final = msg
		}
	}
	if final == nil {
		return nil, ErrNoReply
	}

	s.persist(sess, userMsg, final)
	return &Result{
		SessionID: sess.ID,
		AgentName: runnerName,
		Message:   final,
		CreatedAt: time.Now().Unix(),
	}, nil
}

// Stream 流式完成一轮对话：增量文本通过 sink 下发（SSE），
// 同时在内部拼接完整消息以便持久化历史。sink 返回 error 视为客户端断开，
// 立即停止并返回；已收到的部分不落库（半截回复对多轮上下文无意义）。
func (s *Service) Stream(ctx context.Context, sessionID, agentName string, userMsg *schema.Message, sink func(StreamEvent) error) error {
	sess, err := s.prepare(ctx, &sessionID)
	if err != nil {
		return err
	}

	runnerName, runner, err := s.agents.Resolve(agentName)
	if err != nil {
		return err
	}

	input := make([]adk.Message, 0, len(sess.Messages)+1)
	for _, m := range sess.Messages {
		input = append(input, m)
	}
	input = append(input, userMsg)

	var final *schema.Message
	iter := runner.Run(ctx, input)
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			_ = sink(StreamEvent{Type: "error", Message: ev.Err.Error()})
			return ev.Err
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
					if err := sink(StreamEvent{Type: "chunk", Agent: ev.AgentName, Delta: chunk.Content}); err != nil {
						ss[0].Close()
						ss[1].Close()
						return err
					}
				}
			}
			if streamErr != nil {
				ss[1].Close()
				_ = sink(StreamEvent{Type: "error", Message: streamErr.Error()})
				return streamErr
			}
			merged, err := schema.ConcatMessageStream(ss[1])
			if err != nil {
				return err
			}
			if merged.Role == schema.Assistant {
				final = merged
			}

		case mv.Role == schema.Assistant && mv.Message != nil:
			final = mv.Message
			// 非流式事件也以整体文本补发一次，保证客户端拿到完整内容
			if mv.Message.Content != "" {
				if err := sink(StreamEvent{Type: "chunk", Agent: ev.AgentName, Delta: mv.Message.Content}); err != nil {
					return err
				}
			}
		}
		// 其余事件（如 Tool 角色，V2 引入工具后出现）目前静默跳过
	}
	if final == nil {
		err := ErrNoReply
		_ = sink(StreamEvent{Type: "error", Message: err.Error()})
		return err
	}

	s.persist(sess, userMsg, final)

	meta := final.ResponseMeta
	ev := StreamEvent{Type: "done", SessionID: sess.ID, Agent: runnerName}
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
func (s *Service) GetSession(ctx context.Context, id string) (*session.Session, error) {
	sess, err := s.store.GetOrCreate(ctx, id)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// DeleteSession 删除会话。
func (s *Service) DeleteSession(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// prepare 解析会话：空 ID 生成新 UUID，并确保会话存在。
func (s *Service) prepare(ctx context.Context, sessionID *string) (*session.Session, error) {
	if *sessionID == "" {
		*sessionID = uuid.NewString()
	}
	return s.store.GetOrCreate(ctx, *sessionID)
}

// persist 追加用户消息与 assistant 回复并按上限裁剪后保存。
// 消息对象在落库后视为不可变（memory 实现按此纪律做浅拷贝隔离）。
func (s *Service) persist(sess *session.Session, userMsg, assistantMsg *schema.Message) {
	sess.Messages = append(sess.Messages, userMsg, assistantMsg)
	if s.maxMsgs > 0 && len(sess.Messages) > s.maxMsgs {
		sess.Messages = sess.Messages[len(sess.Messages)-s.maxMsgs:]
	}
	sess.UpdatedAt = time.Now()
	_ = s.store.Save(context.Background(), sess)
}
