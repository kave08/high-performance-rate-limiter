package config

import (
	"sync"
	"time"
)

// Cache provides a thread-safe, TTL-based local cache for configuration values
type Cache struct {
	items   map[string]*cacheItem
	mu      sync.RWMutex
	maxSize int
	cleanup *time.Ticker
	stop    chan struct{}
}

// cacheItem represents a cached configuration value with expiration
type cacheItem struct {
	value     int
	expiresAt time.Time
}

// CacheStats provides statistics about the cache
type CacheStats struct {
	Size    int `json:"size"`
	Active  int `json:"active"`
	Expired int `json:"expired"`
	MaxSize int `json:"max_size"`
}

// NewCache creates a new cache with the specified maximum size and default TTL
func NewCache(maxSize int, defaultTTL time.Duration) *Cache {
	c := &Cache{
		items:   make(map[string]*cacheItem),
		maxSize: maxSize,
		cleanup: time.NewTicker(time.Minute),
		stop:    make(chan struct{}),
	}

	go c.cleanupExpired()

	return c
}

// Get retrieves a value from the cache
func (c *Cache) Get(key string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return 0, false
	}

	if time.Now().After(item.expiresAt) {
		return 0, false
	}

	return item.value, true
}

// Set stores a value in the cache with the specified TTL
func (c *Cache) Set(key string, value int, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If cache is at max size and key doesn't exist, remove oldest entry
	if len(c.items) >= c.maxSize {
		if _, exists := c.items[key]; !exists {
			c.evictOldest()
		}
	}

	c.items[key] = &cacheItem{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// Delete removes a value from the cache
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
}

// Size returns the current number of items in the cache
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.items)
}

// Clear removes all items from the cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*cacheItem)
}

// Stop stops the cache cleanup routine
func (c *Cache) Stop() {
	close(c.stop)
	c.cleanup.Stop()
}

// cleanupExpired removes expired items from the cache
func (c *Cache) cleanupExpired() {
	for {
		select {
		case <-c.cleanup.C:
			c.removeExpired()
		case <-c.stop:
			return
		}
	}
}

// removeExpired removes all expired items
func (c *Cache) removeExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, item := range c.items {
		if now.After(item.expiresAt) {
			delete(c.items, key)
		}
	}
}

// evictOldest removes the oldest item from the cache (simple FIFO)
func (c *Cache) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	first := true
	for key, item := range c.items {
		if first || item.expiresAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = item.expiresAt
			first = false
		}
	}

	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}

// GetStats returns cache statistics
func (c *Cache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	active := 0
	expired := 0

	for _, item := range c.items {
		if now.After(item.expiresAt) {
			expired++
		} else {
			active++
		}
	}

	return CacheStats{
		Size:    len(c.items),
		Active:  active,
		Expired: expired,
		MaxSize: c.maxSize,
	}
}
