package main

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

type streamState struct {
	Reasoning strings.Builder
	ToolIDs   map[string]struct{}
	CreatedAt time.Time
	ExpiresAt time.Time
}

type streamStore struct {
	mu          sync.Mutex
	states      map[string]*streamState
	ttl         time.Duration
	maxEntries  int
	lastCleanup time.Time
	now         func() time.Time
	observe     bool
}

var streamEntries = func() *streamStore {
	store := newStreamStore(defaultCacheTTL, defaultCacheMaxEntries)
	store.observe = true
	return store
}()

func newStreamStore(ttl time.Duration, maxEntries int) *streamStore {
	return &streamStore{
		states:     make(map[string]*streamState),
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

func (store *streamStore) Configure(ttl time.Duration, maxEntries int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if ttl > 0 {
		store.ttl = ttl
	}
	if maxEntries > 0 {
		store.maxEntries = maxEntries
	}
	now := store.now()
	store.cleanupLocked(now)
	store.lastCleanup = now
	store.evictOverflowLocked()
}

func (store *streamStore) Add(key, reasoning string, toolIDs []string) {
	if key == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now()
	state := store.states[key]
	if state != nil && !state.ExpiresAt.After(now) {
		delete(store.states, key)
		if store.observe {
			runtimeMetrics.streamExpired.Add(1)
		}
		state = nil
	}
	store.cleanupIfDueLocked(now)
	if state == nil {
		state = &streamState{
			ToolIDs:   make(map[string]struct{}),
			CreatedAt: now,
		}
		store.states[key] = state
	}
	state.ExpiresAt = now.Add(store.ttl)
	state.Reasoning.WriteString(reasoning)
	for _, id := range toolIDs {
		if id = strings.TrimSpace(id); id != "" {
			state.ToolIDs[id] = struct{}{}
		}
	}
	store.evictOverflowLocked()
}

func (store *streamStore) Finish(key string) {
	if key == "" {
		return
	}
	store.mu.Lock()
	state := store.states[key]
	delete(store.states, key)
	if state != nil && !state.ExpiresAt.After(store.now()) {
		if store.observe {
			runtimeMetrics.streamExpired.Add(1)
		}
		state = nil
	}
	store.mu.Unlock()
	if state == nil {
		return
	}
	ids := make([]string, 0, len(state.ToolIDs))
	for id := range state.ToolIDs {
		ids = append(ids, id)
	}
	if store.observe {
		runtimeMetrics.completedStreams.Add(1)
	}
	reasoningEntries.Put(ids, state.Reasoning.String())
}

func (store *streamStore) Len() int {
	if store == nil {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cleanupLocked(store.now())
	return len(store.states)
}

func (store *streamStore) Reset() {
	store.mu.Lock()
	store.states = make(map[string]*streamState)
	store.lastCleanup = time.Time{}
	store.mu.Unlock()
}

func (store *streamStore) cleanupIfDueLocked(now time.Time) {
	interval := store.ttl
	if interval <= 0 || interval > time.Minute {
		interval = time.Minute
	}
	if store.lastCleanup.IsZero() || now.Before(store.lastCleanup) || now.Sub(store.lastCleanup) >= interval {
		store.cleanupLocked(now)
		store.lastCleanup = now
	}
}

func (store *streamStore) cleanupLocked(now time.Time) {
	expired := 0
	for key, state := range store.states {
		if !state.ExpiresAt.After(now) {
			delete(store.states, key)
			expired++
		}
	}
	if store.observe && expired > 0 {
		runtimeMetrics.streamExpired.Add(uint64(expired))
	}
}

func (store *streamStore) evictOverflowLocked() {
	evicted := 0
	for store.maxEntries > 0 && len(store.states) > store.maxEntries {
		oldestKey := ""
		var oldestTime time.Time
		for key, state := range store.states {
			if oldestKey == "" || state.CreatedAt.Before(oldestTime) || (state.CreatedAt.Equal(oldestTime) && key < oldestKey) {
				oldestKey = key
				oldestTime = state.CreatedAt
			}
		}
		delete(store.states, oldestKey)
		evicted++
	}
	if store.observe && evicted > 0 {
		runtimeMetrics.streamEvicted.Add(uint64(evicted))
	}
}

func captureNonStreamingResponse(body []byte) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		runtimeMetrics.malformedPayloads.Add(1)
		return
	}

	if choices := root.Get("choices"); choices.IsArray() {
		for _, choice := range choices.Array() {
			message := choice.Get("message")
			reasoning := reasoningText(message.Get("reasoning_content"))
			toolCalls := message.Get("tool_calls")
			toolIDs, complete := completeOpenAIToolCallIDs(toolCalls)
			if strings.TrimSpace(reasoning) != "" && toolCalls.IsArray() && len(toolCalls.Array()) > 0 && (!complete || len(toolIDs) == 0) {
				runtimeMetrics.missingToolCallIDs.Add(1)
				continue
			}
			reasoningEntries.Put(toolIDs, reasoning)
		}
	}

	if content := root.Get("content"); content.IsArray() {
		_, reasoning := claudeThinkingParts(content)
		toolIDs := claudeToolUseIDs(content)
		if strings.TrimSpace(reasoning) != "" && len(toolIDs) == 0 {
			runtimeMetrics.missingToolCallIDs.Add(1)
		}
		reasoningEntries.Put(toolIDs, reasoning)
	}
}

func captureStreamChunk(chunk []byte, history [][]byte, metadata map[string]any) {
	if isStreamTerminal(chunk) {
		return
	}
	root, ok := streamJSON(chunk)
	if !ok {
		runtimeMetrics.malformedPayloads.Add(1)
		return
	}
	identity := streamIdentity(root, history, metadata)
	if identity == "" {
		runtimeMetrics.missingStreamIDs.Add(1)
		return
	}

	if choices := root.Get("choices"); choices.IsArray() {
		for choicePosition, choice := range choices.Array() {
			choiceIndex := choice.Get("index").Int()
			if !choice.Get("index").Exists() {
				choiceIndex = int64(choicePosition)
			}
			key := "openai:" + identity + ":" + strconv.FormatInt(choiceIndex, 10)
			delta := choice.Get("delta")
			streamEntries.Add(key, reasoningText(delta.Get("reasoning_content")), openAIToolCallIDs(delta.Get("tool_calls")))
			if finish := choice.Get("finish_reason"); finish.Exists() && finish.Type != gjson.Null && strings.TrimSpace(finish.String()) != "" {
				streamEntries.Finish(key)
			}
		}
		return
	}

	key := "claude:" + identity
	switch root.Get("type").String() {
	case "content_block_start":
		block := root.Get("content_block")
		if block.Get("type").String() == "tool_use" {
			streamEntries.Add(key, "", []string{block.Get("id").String()})
		}
	case "content_block_delta":
		delta := root.Get("delta")
		if delta.Get("type").String() == "thinking_delta" {
			streamEntries.Add(key, delta.Get("thinking").String(), nil)
		}
	case "message_delta":
		if stopReason := strings.TrimSpace(root.Get("delta.stop_reason").String()); stopReason != "" {
			streamEntries.Finish(key)
		}
	case "message_stop":
		streamEntries.Finish(key)
	}
}

func isStreamTerminal(chunk []byte) bool {
	for _, line := range bytes.Split(bytes.TrimSpace(chunk), []byte("\n")) {
		line = bytes.TrimSpace(line)
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(line, []byte("[DONE]")) {
			return true
		}
	}
	return false
}

func streamJSON(chunk []byte) (gjson.Result, bool) {
	trimmed := bytes.TrimSpace(chunk)
	if gjson.ValidBytes(trimmed) {
		return gjson.ParseBytes(trimmed), true
	}

	dataLines := make([]string, 0, 1)
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			dataLines = append(dataLines, strings.TrimSpace(string(line[len("data:"):])))
		}
	}
	if len(dataLines) == 0 {
		return gjson.Result{}, false
	}
	data := strings.Join(dataLines, "\n")
	if !gjson.Valid(data) {
		return gjson.Result{}, false
	}
	return gjson.Parse(data), true
}

func streamIdentity(root gjson.Result, history [][]byte, metadata map[string]any) string {
	if id := identityFromJSON(root); id != "" {
		return id
	}
	for index := len(history) - 1; index >= 0; index-- {
		previous, ok := streamJSON(history[index])
		if ok {
			if id := identityFromJSON(previous); id != "" {
				return id
			}
		}
	}
	for _, key := range []string{"idempotency_key", "execution_session_id"} {
		if value, ok := metadata[key]; ok {
			if id := strings.TrimSpace(valueAsString(value)); id != "" {
				return "metadata:" + key + ":" + id
			}
		}
	}
	return ""
}

func valueAsString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func identityFromJSON(root gjson.Result) string {
	for _, path := range []string{"id", "message.id", "response.id"} {
		if id := strings.TrimSpace(root.Get(path).String()); id != "" {
			return id
		}
	}
	return ""
}

func reasoningText(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.String()
	}
	if !value.IsArray() {
		return ""
	}
	parts := make([]string, 0)
	for _, item := range value.Array() {
		switch {
		case item.Type == gjson.String:
			parts = append(parts, item.String())
		case item.Get("text").Type == gjson.String:
			parts = append(parts, item.Get("text").String())
		}
	}
	return strings.Join(parts, "")
}
