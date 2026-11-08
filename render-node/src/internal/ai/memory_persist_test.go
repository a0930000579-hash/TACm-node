package ai

import "testing"

func TestPersistentMemorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	store, err := NewPersistentMemoryStore(dir)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	store.Append("sess-1",
		ChatMessage{Role: "user", Content: "我叫小明"},
		ChatMessage{Role: "assistant", Content: "你好小明"},
	)

	// 模擬程序重啟：用同一個目錄建立全新存放。
	restarted, err := NewPersistentMemoryStore(dir)
	if err != nil {
		t.Fatalf("reinit: %v", err)
	}
	h := restarted.Load("sess-1")
	if len(h) != 2 {
		t.Fatalf("want 2 history messages after restart, got %d", len(h))
	}
	if h[0].Content != "我叫小明" || h[1].Content != "你好小明" {
		t.Fatalf("history mismatch after restart: %+v", h)
	}

	// 特殊字元 session id 應安全寫檔且可回讀。
	store.Append("a/b c", ChatMessage{Role: "user", Content: "特殊名"})
	if got := restarted.Load("a/b c"); len(got) != 1 || got[0].Content != "特殊名" {
		t.Fatalf("unsafe session id not persisted, got %+v", got)
	}
}

func TestPersistentMemoryEmptyDirReturnsEmpty(t *testing.T) {
	store, err := NewPersistentMemoryStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if h := store.Load("missing"); len(h) != 0 {
		t.Fatalf("want empty history, got %+v", h)
	}
}

func TestInMemoryStoreStillWorks(t *testing.T) {
	m := NewMemoryStore()
	m.Append("x", ChatMessage{Role: "user", Content: "hi"})
	if h := m.Load("x"); len(h) != 1 || h[0].Content != "hi" {
		t.Fatalf("in-memory store broken: %+v", h)
	}
}
