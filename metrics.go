package main

import (
	"sync/atomic"
	"time"
)

const (
	pluginID      = "deepseek-reasoning-bridge"
	pluginName    = "DeepSeek Reasoning Bridge"
	pluginVersion = "0.5.0"
)

type runtimeMetricStore struct {
	startedUnixNano atomic.Int64

	openAIRequests atomic.Uint64
	claudeRequests atomic.Uint64
	geminiRequests atomic.Uint64

	requestNormalizations  atomic.Uint64
	requestInterceptions   atomic.Uint64
	responseNormalizations atomic.Uint64
	responseInterceptions  atomic.Uint64
	streamChunks           atomic.Uint64
	skippedNonTarget       atomic.Uint64

	cacheHits            atomic.Uint64
	cacheMisses          atomic.Uint64
	contentFallbacks     atomic.Uint64
	placeholderFallbacks atomic.Uint64
	passthroughFallbacks atomic.Uint64
	repairedMessages     atomic.Uint64

	cacheWrites      atomic.Uint64
	cacheExpired     atomic.Uint64
	cacheEvicted     atomic.Uint64
	completedStreams atomic.Uint64
	streamExpired    atomic.Uint64
	streamEvicted    atomic.Uint64

	malformedPayloads  atomic.Uint64
	missingToolCallIDs atomic.Uint64
	missingStreamIDs   atomic.Uint64
}

var runtimeMetrics = newRuntimeMetricStore()

func newRuntimeMetricStore() *runtimeMetricStore {
	metrics := &runtimeMetricStore{}
	metrics.startedUnixNano.Store(time.Now().UTC().UnixNano())
	return metrics
}

func (metrics *runtimeMetricStore) Reset(now time.Time) {
	if metrics == nil {
		return
	}
	metrics.startedUnixNano.Store(now.UTC().UnixNano())
	for _, counter := range []*atomic.Uint64{
		&metrics.openAIRequests, &metrics.claudeRequests, &metrics.geminiRequests,
		&metrics.requestNormalizations, &metrics.requestInterceptions,
		&metrics.responseNormalizations, &metrics.responseInterceptions,
		&metrics.streamChunks, &metrics.skippedNonTarget,
		&metrics.cacheHits, &metrics.cacheMisses, &metrics.contentFallbacks,
		&metrics.placeholderFallbacks, &metrics.passthroughFallbacks,
		&metrics.repairedMessages, &metrics.cacheWrites, &metrics.cacheExpired,
		&metrics.cacheEvicted, &metrics.completedStreams, &metrics.streamExpired,
		&metrics.streamEvicted, &metrics.malformedPayloads,
		&metrics.missingToolCallIDs, &metrics.missingStreamIDs,
	} {
		counter.Store(0)
	}
}

func (metrics *runtimeMetricStore) observeProtocol(format string) {
	switch format {
	case "openai":
		metrics.openAIRequests.Add(1)
	case "claude":
		metrics.claudeRequests.Add(1)
	case "gemini":
		metrics.geminiRequests.Add(1)
	}
}

type dashboardSnapshot struct {
	Plugin        dashboardPlugin        `json:"plugin"`
	Status        string                 `json:"status"`
	StartedAt     time.Time              `json:"started_at"`
	GeneratedAt   time.Time              `json:"generated_at"`
	UptimeSeconds int64                  `json:"uptime_seconds"`
	Requests      dashboardRequests      `json:"requests"`
	Restoration   dashboardRestoration   `json:"restoration"`
	Capture       dashboardCapture       `json:"capture"`
	Cache         dashboardCache         `json:"cache"`
	Errors        dashboardErrors        `json:"errors"`
	Configuration dashboardConfiguration `json:"configuration"`
	Privacy       dashboardPrivacy       `json:"privacy"`
}

type dashboardPlugin struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type dashboardRequests struct {
	OpenAI                 uint64 `json:"openai"`
	Claude                 uint64 `json:"claude"`
	Gemini                 uint64 `json:"gemini"`
	RequestNormalizations  uint64 `json:"request_normalizations"`
	RequestInterceptions   uint64 `json:"request_interceptions"`
	ResponseNormalizations uint64 `json:"response_normalizations"`
	ResponseInterceptions  uint64 `json:"response_interceptions"`
	StreamChunks           uint64 `json:"stream_chunks"`
	SkippedNonTarget       uint64 `json:"skipped_non_target"`
}

type dashboardRestoration struct {
	ExactCacheHits       uint64  `json:"exact_cache_hits"`
	CacheMisses          uint64  `json:"cache_misses"`
	HitRate              float64 `json:"hit_rate"`
	ContentFallbacks     uint64  `json:"content_fallbacks"`
	PlaceholderFallbacks uint64  `json:"placeholder_fallbacks"`
	PassthroughFallbacks uint64  `json:"passthrough_fallbacks"`
	RepairedMessages     uint64  `json:"repaired_messages"`
}

type dashboardCapture struct {
	ReasoningWrites  uint64 `json:"reasoning_writes"`
	CompletedStreams uint64 `json:"completed_streams"`
}

type dashboardCache struct {
	ReasoningEntries int    `json:"reasoning_entries"`
	ActiveStreams    int    `json:"active_streams"`
	Capacity         int    `json:"capacity"`
	TTL              string `json:"ttl"`
	ExpiredEntries   uint64 `json:"expired_entries"`
	EvictedEntries   uint64 `json:"evicted_entries"`
	ExpiredStreams   uint64 `json:"expired_streams"`
	EvictedStreams   uint64 `json:"evicted_streams"`
}

type dashboardErrors struct {
	MalformedPayloads  uint64 `json:"malformed_payloads"`
	MissingToolCallIDs uint64 `json:"missing_tool_call_ids"`
	MissingStreamIDs   uint64 `json:"missing_stream_ids"`
}

type dashboardConfiguration struct {
	TargetModelCount int    `json:"target_model_count"`
	FallbackStrategy string `json:"fallback_strategy"`
}

type dashboardPrivacy struct {
	ReasoningExposed     bool `json:"reasoning_exposed"`
	ToolCallIDsExposed   bool `json:"tool_call_ids_exposed"`
	RequestBodiesExposed bool `json:"request_bodies_exposed"`
}

func recordFallback(kind fallbackKind) {
	switch kind {
	case fallbackKindContent:
		runtimeMetrics.contentFallbacks.Add(1)
	case fallbackKindPlaceholder:
		runtimeMetrics.placeholderFallbacks.Add(1)
	case fallbackKindPassthrough:
		runtimeMetrics.passthroughFallbacks.Add(1)
	}
}

func currentDashboardSnapshot(now time.Time) dashboardSnapshot {
	now = now.UTC()
	startedAt := time.Unix(0, runtimeMetrics.startedUnixNano.Load()).UTC()
	if startedAt.After(now) {
		startedAt = now
	}
	cfg := getConfig()
	hits := runtimeMetrics.cacheHits.Load()
	misses := runtimeMetrics.cacheMisses.Load()
	hitRate := float64(0)
	if attempts := hits + misses; attempts > 0 {
		hitRate = float64(hits) / float64(attempts)
	}
	return dashboardSnapshot{
		Plugin:        dashboardPlugin{ID: pluginID, Name: pluginName, Version: pluginVersion},
		Status:        "operational",
		StartedAt:     startedAt,
		GeneratedAt:   now,
		UptimeSeconds: int64(now.Sub(startedAt).Seconds()),
		Requests: dashboardRequests{
			OpenAI:                 runtimeMetrics.openAIRequests.Load(),
			Claude:                 runtimeMetrics.claudeRequests.Load(),
			Gemini:                 runtimeMetrics.geminiRequests.Load(),
			RequestNormalizations:  runtimeMetrics.requestNormalizations.Load(),
			RequestInterceptions:   runtimeMetrics.requestInterceptions.Load(),
			ResponseNormalizations: runtimeMetrics.responseNormalizations.Load(),
			ResponseInterceptions:  runtimeMetrics.responseInterceptions.Load(),
			StreamChunks:           runtimeMetrics.streamChunks.Load(),
			SkippedNonTarget:       runtimeMetrics.skippedNonTarget.Load(),
		},
		Restoration: dashboardRestoration{
			ExactCacheHits:       hits,
			CacheMisses:          misses,
			HitRate:              hitRate,
			ContentFallbacks:     runtimeMetrics.contentFallbacks.Load(),
			PlaceholderFallbacks: runtimeMetrics.placeholderFallbacks.Load(),
			PassthroughFallbacks: runtimeMetrics.passthroughFallbacks.Load(),
			RepairedMessages:     runtimeMetrics.repairedMessages.Load(),
		},
		Capture: dashboardCapture{
			ReasoningWrites:  runtimeMetrics.cacheWrites.Load(),
			CompletedStreams: runtimeMetrics.completedStreams.Load(),
		},
		Cache: dashboardCache{
			ReasoningEntries: reasoningEntries.Len(),
			ActiveStreams:    streamEntries.Len(),
			Capacity:         cfg.CacheMaxEntries,
			TTL:              cfg.CacheTTL.String(),
			ExpiredEntries:   runtimeMetrics.cacheExpired.Load(),
			EvictedEntries:   runtimeMetrics.cacheEvicted.Load(),
			ExpiredStreams:   runtimeMetrics.streamExpired.Load(),
			EvictedStreams:   runtimeMetrics.streamEvicted.Load(),
		},
		Errors: dashboardErrors{
			MalformedPayloads:  runtimeMetrics.malformedPayloads.Load(),
			MissingToolCallIDs: runtimeMetrics.missingToolCallIDs.Load(),
			MissingStreamIDs:   runtimeMetrics.missingStreamIDs.Load(),
		},
		Configuration: dashboardConfiguration{
			TargetModelCount: len(cfg.TargetModels),
			FallbackStrategy: cfg.FallbackStrategy,
		},
		Privacy: dashboardPrivacy{
			ReasoningExposed:     false,
			ToolCallIDsExposed:   false,
			RequestBodiesExposed: false,
		},
	}
}
