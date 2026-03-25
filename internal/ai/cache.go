package ai

import (
	"sync"
	"time"
)

type cacheEntry struct {
	value     string
	createdAt time.Time
}

// Cache is a simple in-memory key-value cache with TTL expiry.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

// NewCache creates a new Cache with the given TTL for entries.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

// Get retrieves a value from the cache. Returns the value and true if found
// and not expired, or empty string and false otherwise.
func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Since(entry.createdAt) > c.ttl {
		return "", false
	}
	return entry.value, true
}

// Set stores a value in the cache with the current timestamp.
func (c *Cache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		value:     value,
		createdAt: time.Now(),
	}
}
