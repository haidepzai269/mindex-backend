package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHashQueryDeterministic(t *testing.T) {
	h1 := HashQuery("weather", "hanoi")
	h2 := HashQuery("weather", "hanoi")
	if h1 != h2 {
		t.Errorf("HashQuery not deterministic: %q != %q", h1, h2)
	}
}

func TestHashQueryDifferentInputs(t *testing.T) {
	h1 := HashQuery("weather", "hanoi")
	h2 := HashQuery("weather", "saigon")
	if h1 == h2 {
		t.Errorf("HashQuery collision: %q == %q", h1, h2)
	}
}

func TestHashQueryLength(t *testing.T) {
	h := HashQuery("test")
	if len(h) != 16 {
		t.Errorf("HashQuery length = %d, want 16", len(h))
	}
}

func TestCacheKeyGlobalScope(t *testing.T) {
	key := cacheKey("weather", "abc123", CacheScopeGlobal, "user1")
	expected := "tools:cache:weather:abc123"
	if key != expected {
		t.Errorf("cacheKey global = %q, want %q", key, expected)
	}
}

func TestCacheKeyUserScope(t *testing.T) {
	key := cacheKey("portfolio", "abc123", CacheScopeUser, "user1")
	expected := "tools:cache:portfolio:user1:abc123"
	if key != expected {
		t.Errorf("cacheKey user = %q, want %q", key, expected)
	}
}

func TestCachedExecuteNoRedis(t *testing.T) {
	called := false
	data, err := CachedExecute(context.TODO(), "test", "hash", CacheScopeGlobal, "", 0, func() (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("fetchFn should be called when Redis is nil")
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("data = %s, want {\"ok\":true}", string(data))
	}
}
