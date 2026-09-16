// Package session 定义会话存储接口与会话类型。
// V1 提供内存实现（memory）；V4 可按同一接口换 Redis/DB 实现。
package session

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/history"
)

// ErrNotFound 会话不存在。
var ErrNotFound = errors.New("session not found")

// Session 一段多轮对话。Messages 保存完整 eino 消息
// （含多模态 part），供后续轮次原样回放给模型。
type Session struct {
	ID        string            `json:"id"`
	Messages  []*schema.Message `json:"-"` // 由 API 层转换为视图，不直接序列化
	History   []history.Message `json:"-"` // 独立渲染快照，不依赖 Eino Extra
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Store 会话存储接口。实现必须并发安全；
// 返回的 Session 与传入的 Session 都归调用方所有，实现不得持有引用。
type Store interface {
	// GetOrCreate 返回指定 id 的会话，不存在则创建。
	// id 必须非空。
	GetOrCreate(ctx context.Context, id string) (*Session, error)
	// Get 返回指定 id 的会话，不存在返回 ErrNotFound（不创建）。
	// 用于 GET 类查询接口,避免读路径产生副作用。
	Get(ctx context.Context, id string) (*Session, error)
	// Save 保存会话整体（全量覆盖 Messages）。
	Save(ctx context.Context, s *Session) error
	// Delete 删除会话；不存在时返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
	// List 返回所有会话(按 UpdatedAt 倒序,最新活跃的在前)。
	// 用于侧边栏 / 多 session 切换,实现必须返回深拷贝,不可让 caller 改内部状态。
	List(ctx context.Context) ([]*Session, error)
}
