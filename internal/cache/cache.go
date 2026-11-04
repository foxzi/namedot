package cache

import (
    "sync"
    "time"
)

type item struct {
    value      any
    expiresAt  time.Time
}

type Cache struct {
    mu    sync.RWMutex
    data  map[string]item
    size  int
}

func New(size int) *Cache {
    return &Cache{data: make(map[string]item, size), size: size}
}

func (c *Cache) Set(key string, value any, ttl time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if len(c.data) >= c.size {
        // naive eviction: delete random (first) item
        for k := range c.data {
            delete(c.data, k)
            break
        }
    }
    c.data[key] = item{value: value, expiresAt: time.Now().Add(ttl)}
}

func (c *Cache) Get(key string) (any, bool) {
    c.mu.RLock()
    it, ok := c.data[key]
    c.mu.RUnlock()
    if !ok {
        return nil, false
    }
    if time.Now().After(it.expiresAt) {
        c.mu.Lock()
        delete(c.data, key)
        c.mu.Unlock()
        return nil, false
    }
    return it.value, true
}


// Clear removes all items.
func (c *Cache) Clear() {
    c.mu.Lock()
    c.data = make(map[string]item, c.size)
    c.mu.Unlock()
}
