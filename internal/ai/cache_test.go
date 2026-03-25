package ai

import (
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	cache := NewCache(1 * time.Hour)
	cache.Set("key1", "value1")
	val, ok := cache.Get("key1")
	if !ok || val != "value1" {
		t.Error("expected cache hit")
	}
	_, ok = cache.Get("missing")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := NewCache(1 * time.Millisecond)
	cache.Set("key1", "value1")
	time.Sleep(5 * time.Millisecond)
	_, ok := cache.Get("key1")
	if ok {
		t.Error("expected expired cache miss")
	}
}
