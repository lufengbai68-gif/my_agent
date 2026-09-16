package memory

import (
	"context"
	"testing"

	"github.com/lufengbai68-gif/my_agent/internal/uploads"
)

func mk(id, session string) *uploads.Upload {
	return &uploads.Upload{
		ID:        id,
		URL:       "https://x/" + id,
		Filename:  id + ".png",
		MimeType:  "image/png",
		SessionID: session,
	}
}

func TestStore_PutAndGet(t *testing.T) {
	s := New()
	u := mk("a", "s1")
	if err := s.Put(context.Background(), u); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(context.Background(), "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SessionID != "s1" || got.Filename != "a.png" {
		t.Errorf("got %+v", got)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := New()
	_, err := s.Get(context.Background(), "missing")
	if err != uploads.ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestStore_AnonymousUploadNotIndexed(t *testing.T) {
	s := New()
	_ = s.Put(context.Background(), mk("a", "")) // 匿名上传
	// Get 能拿到
	if _, err := s.Get(context.Background(), "a"); err != nil {
		t.Errorf("anonymous upload should be gettable: %v", err)
	}
	// 但 ListBySession 不返回
	if list, _ := s.ListBySession(context.Background(), ""); list != nil {
		t.Errorf("anonymous should not be listed, got %v", list)
	}
	if list, _ := s.ListBySession(context.Background(), "any-session"); list != nil {
		t.Errorf("anonymous should not appear in any session list, got %v", list)
	}
}

func TestStore_ListBySession(t *testing.T) {
	s := New()
	_ = s.Put(context.Background(), mk("a", "s1"))
	_ = s.Put(context.Background(), mk("b", "s1"))
	_ = s.Put(context.Background(), mk("c", "s2"))
	_ = s.Put(context.Background(), mk("d", ""))

	list, err := s.ListBySession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("s1 should have 2 uploads, got %d", len(list))
	}
	for _, u := range list {
		if u.SessionID != "s1" {
			t.Errorf("list returned wrong session: %+v", u)
		}
	}

	if list, _ := s.ListBySession(context.Background(), "s2"); len(list) != 1 {
		t.Errorf("s2 should have 1 upload, got %d", len(list))
	}

	if list, _ := s.ListBySession(context.Background(), "missing"); list != nil {
		t.Errorf("missing session should return nil, got %v", list)
	}
}

func TestStore_PutMigrateSession(t *testing.T) {
	s := New()
	_ = s.Put(context.Background(), mk("a", "s1"))
	// 改 session
	_ = s.Put(context.Background(), mk("a", "s2"))

	if list, _ := s.ListBySession(context.Background(), "s1"); len(list) != 0 {
		t.Errorf("s1 should be empty after migrate, got %d", len(list))
	}
	if list, _ := s.ListBySession(context.Background(), "s2"); len(list) != 1 {
		t.Errorf("s2 should have the migrated upload, got %d", len(list))
	}
}

func TestStore_Delete(t *testing.T) {
	s := New()
	_ = s.Put(context.Background(), mk("a", "s1"))

	if err := s.Delete(context.Background(), "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(context.Background(), "a"); err != uploads.ErrNotFound {
		t.Errorf("should be not found, got %v", err)
	}
	// 反向索引也应清掉
	if list, _ := s.ListBySession(context.Background(), "s1"); len(list) != 0 {
		t.Errorf("session index should be cleaned, got %d", len(list))
	}

	// 二次删返回 NotFound
	if err := s.Delete(context.Background(), "a"); err != uploads.ErrNotFound {
		t.Errorf("second delete should return ErrNotFound, got %v", err)
	}
}

func TestStore_DeleteBySession(t *testing.T) {
	s := New()
	_ = s.Put(context.Background(), mk("a", "s1"))
	_ = s.Put(context.Background(), mk("b", "s1"))
	_ = s.Put(context.Background(), mk("c", "s2"))

	deleted, err := s.DeleteBySession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("delete by session: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("should return 2 deleted IDs, got %d (%v)", len(deleted), deleted)
	}
	// s2 不应被影响
	if list, _ := s.ListBySession(context.Background(), "s2"); len(list) != 1 {
		t.Errorf("s2 should be untouched, got %d", len(list))
	}
	// s1 已空
	if list, _ := s.ListBySession(context.Background(), "s1"); list != nil {
		t.Errorf("s1 should be empty, got %v", list)
	}
}
