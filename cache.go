package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type reasoningEntry struct {
	Text      string
	SizeBytes int
	CreatedAt time.Time
	ExpiresAt time.Time
}

type reasoningCache struct {
	mu          sync.Mutex
	entries     map[string]reasoningEntry
	ttl         time.Duration
	maxEntries  int
	maxBytes    int
	totalBytes  int
	lastCleanup time.Time
	now         func() time.Time
	observe     bool
}

var reasoningEntries = func() *reasoningCache {
	cache := newReasoningCache(defaultCacheTTL, defaultCacheMaxEntries)
	cache.maxBytes = defaultCacheMaxBytes
	cache.observe = true
	return cache
}()

func newReasoningCache(ttl time.Duration, maxEntries int) *reasoningCache {
	return &reasoningCache{
		entries:    make(map[string]reasoningEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
		maxBytes:   defaultCacheMaxBytes,
		now:        time.Now,
	}
}

func (cache *reasoningCache) Configure(ttl time.Duration, maxEntries, maxBytes int) {
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
	if maxBytes > 0 {
		cache.maxBytes = maxBytes
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
	textSizeBytes := len(text)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.maxBytes > 0 && textSizeBytes > cache.maxBytes {
		if cache.observe {
			runtimeMetrics.cacheEntryTooLarge.Add(1)
		}
		return false
	}
	now := cache.now()
	cache.cleanupIfDueLocked(now)
	if existing, exists := cache.entries[key]; exists {
		cache.totalBytes -= existing.SizeBytes
	}
	cache.entries[key] = reasoningEntry{
		Text:      text,
		SizeBytes: textSizeBytes,
		CreatedAt: now,
		ExpiresAt: now.Add(cache.ttl),
	}
	cache.totalBytes += textSizeBytes
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
		cache.deleteEntryLocked(key, entry)
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

func (cache *reasoningCache) Bytes() int {
	if cache == nil {
		return 0
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.deleteExpiredLocked(cache.now())
	return cache.totalBytes
}

func (cache *reasoningCache) Reset() {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	cache.entries = make(map[string]reasoningEntry)
	cache.totalBytes = 0
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
			cache.deleteEntryLocked(key, entry)
			expired++
		}
	}
	if cache.observe && expired > 0 {
		runtimeMetrics.cacheExpired.Add(uint64(expired))
	}
}

func (cache *reasoningCache) evictOverflowLocked() {
	evicted := 0
	for (cache.maxEntries > 0 && len(cache.entries) > cache.maxEntries) ||
		(cache.maxBytes > 0 && cache.totalBytes > cache.maxBytes) {
		oldestKey, oldestEntry := cache.oldestEntryLocked()
		if oldestKey == "" {
			break
		}
		cache.deleteEntryLocked(oldestKey, oldestEntry)
		evicted++
	}
	if cache.observe && evicted > 0 {
		runtimeMetrics.cacheEvicted.Add(uint64(evicted))
	}
}

func (cache *reasoningCache) oldestEntryLocked() (string, reasoningEntry) {
	oldestKey := ""
	var oldestEntry reasoningEntry
	for key, entry := range cache.entries {
		if oldestKey == "" || entry.CreatedAt.Before(oldestEntry.CreatedAt) ||
			(entry.CreatedAt.Equal(oldestEntry.CreatedAt) && key < oldestKey) {
			oldestKey = key
			oldestEntry = entry
		}
	}
	return oldestKey, oldestEntry
}

func (cache *reasoningCache) deleteEntryLocked(key string, entry reasoningEntry) {
	delete(cache.entries, key)
	cache.totalBytes -= entry.SizeBytes
	if cache.totalBytes < 0 {
		cache.totalBytes = 0
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
