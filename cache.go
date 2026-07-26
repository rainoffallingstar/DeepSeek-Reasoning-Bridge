package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type reasoningEntry struct {
	Text      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type reasoningCache struct {
	mu          sync.Mutex
	entries     map[string]reasoningEntry
	ttl         time.Duration
	maxEntries  int
	lastCleanup time.Time
	now         func() time.Time
	observe     bool
}

var reasoningEntries = func() *reasoningCache {
	cache := newReasoningCache(defaultCacheTTL, defaultCacheMaxEntries)
	cache.observe = true
	return cache
}()

func newReasoningCache(ttl time.Duration, maxEntries int) *reasoningCache {
	return &reasoningCache{
		entries:    make(map[string]reasoningEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

func (cache *reasoningCache) Configure(ttl time.Duration, maxEntries int) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if ttl > 0 {
		cache.ttl = ttl
	}
	if maxEntries > 0 {
		cache.maxEntries = maxEntries
	}
	now := cache.now()
	cache.deleteExpiredLocked(now)
	cache.lastCleanup = now
	cache.evictOverflowLocked()
}

func (cache *reasoningCache) Put(toolCallIDs []string, text string) bool {
	if cache == nil || strings.TrimSpace(text) == "" {
		return false
	}
	key := toolCallKey(toolCallIDs)
	if key == "" {
		return false
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := cache.now()
	cache.cleanupIfDueLocked(now)
	cache.entries[key] = reasoningEntry{
		Text:      text,
		CreatedAt: now,
		ExpiresAt: now.Add(cache.ttl),
	}
	if cache.observe {
		runtimeMetrics.cacheWrites.Add(1)
	}
	cache.evictOverflowLocked()
	return true
}

func (cache *reasoningCache) Get(toolCallIDs []string) (string, bool) {
	if cache == nil {
		return "", false
	}
	key := toolCallKey(toolCallIDs)
	if key == "" {
		return "", false
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := cache.now()
	entry, ok := cache.entries[key]
	if !ok {
		return "", false
	}
	if !entry.ExpiresAt.After(now) {
		delete(cache.entries, key)
		if cache.observe {
			runtimeMetrics.cacheExpired.Add(1)
		}
		return "", false
	}
	return entry.Text, true
}

func (cache *reasoningCache) Len() int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.deleteExpiredLocked(cache.now())
	return len(cache.entries)
}

func (cache *reasoningCache) Reset() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.entries = make(map[string]reasoningEntry)
	cache.lastCleanup = time.Time{}
	cache.mu.Unlock()
}

func (cache *reasoningCache) cleanupIfDueLocked(now time.Time) {
	interval := cache.ttl
	if interval <= 0 || interval > time.Minute {
		interval = time.Minute
	}
	if cache.lastCleanup.IsZero() || now.Before(cache.lastCleanup) || now.Sub(cache.lastCleanup) >= interval {
		cache.deleteExpiredLocked(now)
		cache.lastCleanup = now
	}
}

func (cache *reasoningCache) deleteExpiredLocked(now time.Time) {
	expired := 0
	for key, entry := range cache.entries {
		if !entry.ExpiresAt.After(now) {
			delete(cache.entries, key)
			expired++
		}
	}
	if cache.observe && expired > 0 {
		runtimeMetrics.cacheExpired.Add(uint64(expired))
	}
}

func (cache *reasoningCache) evictOverflowLocked() {
	evicted := 0
	for cache.maxEntries > 0 && len(cache.entries) > cache.maxEntries {
		oldestKey := ""
		var oldestTime time.Time
		for key, entry := range cache.entries {
			if oldestKey == "" || entry.CreatedAt.Before(oldestTime) || (entry.CreatedAt.Equal(oldestTime) && key < oldestKey) {
				oldestKey = key
				oldestTime = entry.CreatedAt
			}
		}
		delete(cache.entries, oldestKey)
		evicted++
	}
	if cache.observe && evicted > 0 {
		runtimeMetrics.cacheEvicted.Add(uint64(evicted))
	}
}

func toolCallKey(toolCallIDs []string) string {
	if len(toolCallIDs) == 0 {
		return ""
	}
	unique := make(map[string]struct{}, len(toolCallIDs))
	for _, id := range toolCallIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			unique[id] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return ""
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\x00")
}
