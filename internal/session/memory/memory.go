// Package memory 提供会话的内存存储实现（sync.RWMutex + map）。
// 仅适用于单实例部署；V4 换 Redis 实现时无需改动上层。
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/session"
)

// Store 内存会话存储。
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
}

var _ session.Store = (*Store)(nil)

// New 创建空的内存存储。
func New() *Store {
	return &Store{sessions: make(map[string]*session.Session)}
}

// GetOrCreate 返回会话副本；不存在则以当前时间创建。
func (s *Store) GetOrCreate(_ context.Context, id string) (*session.Session, error) {
	if id == "" {
		return nil, session.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		now := time.Now()
		sess = &session.Session{ID: id, CreatedAt: now, UpdatedAt: now}
		s.sessions[id] = sess
	}
	return cloneSession(sess), nil
}

// Save 全量覆盖保存（深拷贝入参，不持有调用方引用）。
func (s *Store) Save(_ context.Context, sess *session.Session) error {
	if sess == nil || sess.ID == "" {
		return session.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = cloneSession(sess)
	return nil
}

// Delete 删除会话，不存在返回 ErrNotFound。
func (s *Store) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return session.ErrNotFound
	}
	delete(s.sessions, id)
	return nil
}

// cloneSession 复制会话及其 Messages 切片。
// 消息对象本身在写入后视为不可变（chat.Service 的纪律），
// 因此浅拷贝消息指针即可安全隔离追加操作。
func cloneSession(sess *session.Session) *session.Session {
	cp := *sess
	cp.Messages = append([]*schema.Message(nil), sess.Messages...)
	return &cp
}
