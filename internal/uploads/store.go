// Package uploads 文件元数据索引接口。
//
// 与 session.Store 模式一致:本包定义接口,实现放在子包(memory/...)里。
package uploads

import (
	"context"
	"errors"
)

// ErrNotFound upload 不存在。
var ErrNotFound = errors.New("upload not found")

// UploadStore upload 元数据索引:把 upload 归属到 session,支持级联删除。
//
// 实现要求:
//   - 并发安全
//   - Put 时 SessionID 空 → 不索引(ListBySession 不会返回)
//   - 进程重启数据丢失是 v1 已知限制(与 session.Store 行为一致);
//     真实 OSS 对象仍在,会成"孤儿对象",需要靠 TOS lifecycle 兜底清理
type UploadStore interface {
	// Put 写入/覆盖一条 upload 元数据。
	Put(ctx context.Context, u *Upload) error
	// Get 按 ID 取 upload。ID 不存在返回 ErrNotFound。
	Get(ctx context.Context, id string) (*Upload, error)
	// ListBySession 列某 session 下所有 upload。sessionID 空返回 nil。
	ListBySession(ctx context.Context, sessionID string) ([]*Upload, error)
	// Delete 删单条 upload 元数据。ID 不存在返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
	// DeleteBySession 删某 session 下所有 upload 元数据,返回被删的 ID 列表。
	DeleteBySession(ctx context.Context, sessionID string) ([]string, error)
}
