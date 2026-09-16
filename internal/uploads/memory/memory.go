package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/lufengbai68-gif/my_agent/internal/uploads"
)

// Store 进程内 Upload 元数据索引(与 session/memory 行为一致,v1 单进程)。
//
// 索引结构:
//   byID      : ID → *Upload(快速点查 / 删)
//   bySession : sessionID → set[ID](快速列某 session 下所有 upload)
type Store struct {
	mu        sync.RWMutex
	byID      map[string]*uploads.Upload
	bySession map[string]map[string]struct{}
}

// New 返回空 Store。
func New() *Store {
	return &Store{
		byID:      make(map[string]*uploads.Upload),
		bySession: make(map[string]map[string]struct{}),
	}
}

// Put 写入一条 upload 元数据。
//
// SessionID 非空时:在 bySession 建反向索引;以后 ListBySession 才会返回它。
// SessionID 为空:只进 byID(匿名上传,不索引)。
// 同一 ID 二次 Put:全字段覆盖;若 SessionID 变了,反向索引会跟着迁移。
func (s *Store) Put(_ context.Context, u *uploads.Upload) error {
	if u == nil || u.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	old, existed := s.byID[u.ID]
	if existed && old.SessionID != u.SessionID {
		if old.SessionID != "" {
			delete(s.bySession[old.SessionID], u.ID)
			if len(s.bySession[old.SessionID]) == 0 {
				delete(s.bySession, old.SessionID)
			}
		}
	}
	s.byID[u.ID] = u
	if u.SessionID != "" {
		if s.bySession[u.SessionID] == nil {
			s.bySession[u.SessionID] = make(map[string]struct{})
		}
		s.bySession[u.SessionID][u.ID] = struct{}{}
	}
	return nil
}

// Get 按 ID 取 upload。
func (s *Store) Get(_ context.Context, id string) (*uploads.Upload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return nil, uploads.ErrNotFound
	}
	cp := *u
	return &cp, nil
}

// ListBySession 列某 session 下所有 upload。
// sessionID 空返回 nil(匿名上传不索引,避免"列出所有匿名上传"这类无意义查询)。
func (s *Store) ListBySession(_ context.Context, sessionID string) ([]*uploads.Upload, error) {
	if sessionID == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids, ok := s.bySession[sessionID]
	if !ok {
		return nil, nil
	}
	out := make([]*uploads.Upload, 0, len(ids))
	for id := range ids {
		if u, ok := s.byID[id]; ok {
			cp := *u
			out = append(out, &cp)
		}
	}
	// 按 ID 稳定排序,便于前端 diff / 调试
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Delete 删单条 upload 元数据。
func (s *Store) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[id]
	if !ok {
		return uploads.ErrNotFound
	}
	if u.SessionID != "" {
		if set, ok := s.bySession[u.SessionID]; ok {
			delete(set, id)
			if len(set) == 0 {
				delete(s.bySession, u.SessionID)
			}
		}
	}
	delete(s.byID, id)
	return nil
}

// DeleteBySession 删某 session 下所有 upload 元数据,返回被删的 ID 列表
// (供调用方去清 OSS 对象)。
func (s *Store) DeleteBySession(_ context.Context, sessionID string) ([]string, error) {
	if sessionID == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, ok := s.bySession[sessionID]
	if !ok {
		return nil, nil
	}
	deleted := make([]string, 0, len(ids))
	for id := range ids {
		if u, ok := s.byID[id]; ok {
			if u.SessionID == sessionID {
				delete(s.byID, id)
				deleted = append(deleted, id)
			}
		}
	}
	delete(s.bySession, sessionID)
	sort.Strings(deleted)
	return deleted, nil
}
