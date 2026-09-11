package memory

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/lufengbai68-gif/my_agent/internal/session"
)

func TestStoreCRUD(t *testing.T) {
	s := New()
	ctx := context.Background()

	// GetOrCreate 幂等
	s1, err := s.GetOrCreate(ctx, "a")
	if err != nil || s1.ID != "a" || len(s1.Messages) != 0 {
		t.Fatalf("get: %+v err=%v", s1, err)
	}
	s2, _ := s.GetOrCreate(ctx, "a")
	if s2.ID != "a" {
		t.Fatalf("second get: %+v", s2)
	}

	// Save 全量覆盖
	m1 := &schema.Message{Role: schema.User, Content: "hi"}
	m2 := &schema.Message{Role: schema.Assistant, Content: "hello"}
	s1.Messages = []*schema.Message{m1, m2}
	if err := s.Save(ctx, s1); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _ := s.GetOrCreate(ctx, "a")
	if len(got.Messages) != 2 || got.Messages[0].Content != "hi" || got.Messages[1].Content != "hello" {
		t.Fatalf("after save: %+v", got.Messages)
	}

	// 调用方改副本不影响存储（切片隔离）
	got.Messages = got.Messages[:0]
	again, _ := s.GetOrCreate(ctx, "a")
	if len(again.Messages) != 2 {
		t.Fatalf("store was mutated via returned copy: %+v", again.Messages)
	}

	// Delete + ErrNotFound
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Delete(ctx, "a"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetOrCreate(ctx, ""); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("empty id: want ErrNotFound, got %v", err)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := New()
	ctx := context.Background()

	const workers, rounds = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				id := "sess-parallel"
				if w%2 == 0 { // 一半并发写不同会话
					id = "sess-" + string(rune('a'+w%26))
				}
				sess, err := s.GetOrCreate(ctx, id)
				if err != nil {
					t.Errorf("get: %v", err)
					return
				}
				sess.Messages = append(sess.Messages, &schema.Message{Role: schema.User, Content: "x"})
				if err := s.Save(ctx, sess); err != nil {
					t.Errorf("save: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
