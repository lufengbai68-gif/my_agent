package memory

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/session"
)

func TestList_Empty(t *testing.T) {
	s := New()
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty, got %d", len(list))
	}
}

func TestList_OrderedByUpdatedDesc(t *testing.T) {
	s := New()
	ctx := context.Background()

	// 三个 session,更新顺序: a → b → c  (c 最新)
	sessA, _ := s.GetOrCreate(ctx, "a")
	time.Sleep(10 * time.Millisecond)
	sessB, _ := s.GetOrCreate(ctx, "b")
	time.Sleep(10 * time.Millisecond)
	sessC, _ := s.GetOrCreate(ctx, "c")

	// 让 a 重新变 "最新"
	sessA.UpdatedAt = time.Now().Add(time.Hour)
	_ = s.Save(ctx, sessA)
	_ = sessB
	_ = sessC

	list, _ := s.List(ctx)
	if len(list) != 3 {
		t.Fatalf("want 3, got %d", len(list))
	}
	if list[0].ID != "a" {
		t.Errorf("first should be most recently updated, got %q", list[0].ID)
	}
}

func TestList_ReturnsDeepCopy(t *testing.T) {
	s := New()
	ctx := context.Background()
	sess, _ := s.GetOrCreate(ctx, "s1")
	sess.Messages = []*schema.Message{schema.UserMessage("hi")}
	_ = s.Save(ctx, sess)

	list, _ := s.List(ctx)
	if len(list) == 0 {
		t.Fatal("no sessions returned")
	}
	// 改 list 不应影响内部状态
	list[0].Messages = append(list[0].Messages, schema.UserMessage("evil"))

	reloaded, _ := s.GetOrCreate(ctx, "s1")
	if len(reloaded.Messages) != 1 {
		t.Errorf("internal Messages mutated by external List caller: %d", len(reloaded.Messages))
	}
}

// 确保 Store 接口包含 List(编译期检查)
var _ session.Store = (*Store)(nil)


func TestGet_Existing(t *testing.T) {
	s := New()
	ctx := context.Background()
	created, _ := s.GetOrCreate(ctx, "s1")
	created.Messages = []*schema.Message{schema.UserMessage("hi")}
	_ = s.Save(ctx, created)

	got, err := s.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Errorf("want 1 message, got %d", len(got.Messages))
	}
}

func TestGet_NotFoundDoesNotCreate(t *testing.T) {
	s := New()
	ctx := context.Background()

	// 关键:读不存在的不该创建
	_, err := s.Get(ctx, "ghost")
	if err != session.ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// store 内部不应有任何记录
	s.mu.RLock()
	n := len(s.sessions)
	s.mu.RUnlock()
	if n != 0 {
		t.Errorf("Get should not create; sessions map has %d entries", n)
	}
}

func TestGet_DoesNotMutate(t *testing.T) {
	s := New()
	ctx := context.Background()
	created, _ := s.GetOrCreate(ctx, "s1")
	_ = s.Save(ctx, created)

	got, _ := s.Get(ctx, "s1")
	got.Messages = append(got.Messages, schema.UserMessage("evil"))

	reloaded, _ := s.Get(ctx, "s1")
	if len(reloaded.Messages) != 0 {
		t.Errorf("Get should return copy; store mutated to %d messages", len(reloaded.Messages))
	}
}
