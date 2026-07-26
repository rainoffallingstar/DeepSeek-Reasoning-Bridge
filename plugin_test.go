package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func resetTestState(t *testing.T) PluginConfig {
	t.Helper()
	cfg := defaultConfig()
	storeConfig(cfg)
	runtimeMetrics.Reset(time.Now())
	reasoningEntries.Reset()
	streamEntries.Reset()
	return cfg
}

func TestLoadConfigSupportsScalarAndSequenceModels(t *testing.T) {
	scalar, errScalar := loadConfig([]byte("target_models: deepseek-*, deepseek-r1\nfallback_strategy: passthrough\ncache_ttl: 30m\ncache_max_entries: 50\n"))
	if errScalar != nil {
		t.Fatal(errScalar)
	}
	if len(scalar.TargetModels) != 2 || scalar.TargetModels[1] != "deepseek-r1" {
		t.Fatalf("scalar target models = %v", scalar.TargetModels)
	}
	if scalar.FallbackStrategy != "passthrough" || scalar.CacheTTL != 30*time.Minute || scalar.CacheMaxEntries != 50 {
		t.Fatalf("unexpected scalar config: %+v", scalar)
	}

	sequence, errSequence := loadConfig([]byte("target_models:\n  - deepseek-*\n  - 'vendor/deepseek-*' # comment\n"))
	if errSequence != nil {
		t.Fatal(errSequence)
	}
	if len(sequence.TargetModels) != 2 || sequence.TargetModels[1] != "vendor/deepseek-*" {
		t.Fatalf("sequence target models = %v", sequence.TargetModels)
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	if _, err := loadConfig([]byte("fallback_strategy: cache\n")); err == nil {
		t.Fatal("expected legacy model-level cache strategy to be rejected")
	}
	if _, err := loadConfig([]byte("cache_ttl: never\n")); err == nil {
		t.Fatal("expected invalid cache ttl to be rejected")
	}
	if _, err := loadConfig([]byte("cache_max_entries: 0\n")); err == nil {
		t.Fatal("expected zero cache capacity to be rejected")
	}
}

func TestModelMatchesDeepSeekOnly(t *testing.T) {
	patterns := []string{"deepseek-*", "vendor/deepseek-*"}
	for _, model := range []string{"deepseek-chat", "DeepSeek-V4-Pro", "vendor/deepseek-r1"} {
		if !modelMatches(model, patterns) {
			t.Errorf("expected %q to match", model)
		}
	}
	for _, model := range []string{"gpt-5", "claude-opus", "deepseek"} {
		if modelMatches(model, patterns) {
			t.Errorf("did not expect %q to match", model)
		}
	}
}

func TestRPCContractAcceptsRealHTTPHeaders(t *testing.T) {
	resetTestState(t)
	req := requestInterceptRequest{
		SourceFormat: "openai",
		ToFormat:     "openai",
		Model:        "deepseek-chat",
		Headers: http.Header{
			"Authorization": {"Bearer token"},
			"X-Multi":       {"one", "two"},
		},
		Body: []byte(`{"messages":[{"role":"assistant","content":"checking","tool_calls":[{"id":"call_headers","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`),
	}
	rawRequest, _ := json.Marshal(req)
	rawResponse, errHandle := handleMethod("request.intercept_after", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	body := interceptorBody(t, rawResponse)
	if got := gjson.GetBytes(body, "messages.0.reasoning_content").String(); got != "checking" {
		t.Fatalf("reasoning_content = %q", got)
	}
}

func TestReasoningCacheKeysByExactToolCallSet(t *testing.T) {
	cache := newReasoningCache(time.Hour, 10)
	if !cache.Put([]string{"call_b", "call_a"}, "reasoning AB") {
		t.Fatal("expected cache put")
	}
	if got, ok := cache.Get([]string{"call_a", "call_b"}); !ok || got != "reasoning AB" {
		t.Fatalf("order-independent get = %q, %v", got, ok)
	}
	if _, ok := cache.Get([]string{"call_a"}); ok {
		t.Fatal("partial tool-call set must not match")
	}
	if _, ok := cache.Get([]string{"call_c"}); ok {
		t.Fatal("different tool-call set must not match")
	}
}

func TestReasoningCacheExpiresAndEvictsOldest(t *testing.T) {
	cache := newReasoningCache(time.Minute, 2)
	now := time.Unix(100, 0)
	cache.now = func() time.Time { return now }
	cache.Put([]string{"a"}, "A")
	now = now.Add(time.Second)
	cache.Put([]string{"b"}, "B")
	now = now.Add(time.Second)
	cache.Put([]string{"c"}, "C")
	if _, ok := cache.Get([]string{"a"}); ok {
		t.Fatal("oldest entry should be evicted")
	}
	if cache.Len() != 2 {
		t.Fatalf("cache len = %d", cache.Len())
	}
	now = now.Add(2 * time.Minute)
	if _, ok := cache.Get([]string{"b"}); ok {
		t.Fatal("expired entry should not be returned")
	}
}

func TestPatchOpenAIRequestRestoresEachToolRoundIndependently(t *testing.T) {
	cfg := resetTestState(t)
	reasoningEntries.Put([]string{"call_1"}, "reasoning one")
	reasoningEntries.Put([]string{"call_2"}, "reasoning two")
	body := []byte(`{"messages":[
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"one","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"two","arguments":"{}"}}]}
	]}`)
	patched := patchOpenAIRequest(body, cfg)
	if patched == nil {
		t.Fatal("expected patched request")
	}
	if got := gjson.GetBytes(patched, "messages.0.reasoning_content").String(); got != "reasoning one" {
		t.Fatalf("first reasoning = %q", got)
	}
	if got := gjson.GetBytes(patched, "messages.2.reasoning_content").String(); got != "reasoning two" {
		t.Fatalf("second reasoning = %q", got)
	}
}

func TestPatchOpenAIRequestFallbackAndPassthrough(t *testing.T) {
	cfg := resetTestState(t)
	body := []byte(`{"messages":[{"role":"assistant","content":"fallback text","tool_calls":[{"id":"missing","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
	patched := patchOpenAIRequest(body, cfg)
	if got := gjson.GetBytes(patched, "messages.0.reasoning_content").String(); got != "fallback text" {
		t.Fatalf("content fallback = %q", got)
	}
	cfg.FallbackStrategy = "passthrough"
	if patched := patchOpenAIRequest(body, cfg); patched != nil {
		t.Fatal("passthrough fallback must leave request unchanged")
	}
}

func TestPatchClaudeRequestSignsExistingThinking(t *testing.T) {
	cfg := resetTestState(t)
	body := []byte(`{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":"use the tool"},
		{"type":"tool_use","id":"call_claude","name":"lookup","input":{}}
	]}]}`)
	patched := patchClaudeRequest(body, cfg)
	if patched == nil {
		t.Fatal("expected signature patch")
	}
	signature := gjson.GetBytes(patched, "messages.0.content.0.signature").String()
	if !isValidGPTSignature(signature) {
		t.Fatalf("invalid synthesized signature %q", signature)
	}
	if got, ok := reasoningEntries.Get([]string{"call_claude"}); !ok || got != "use the tool" {
		t.Fatalf("cached existing thinking = %q, %v", got, ok)
	}
}

func TestPatchClaudeRequestRestoresMissingThinkingBeforeToolUse(t *testing.T) {
	cfg := resetTestState(t)
	reasoningEntries.Put([]string{"call_restore"}, "restored thought")
	body := []byte(`{"messages":[{"role":"assistant","content":[
		{"type":"text","text":"Checking"},
		{"type":"tool_use","id":"call_restore","name":"lookup","input":{}}
	]}]}`)
	patched := patchClaudeRequest(body, cfg)
	if patched == nil {
		t.Fatal("expected restored thinking")
	}
	if got := gjson.GetBytes(patched, "messages.0.content.1.type").String(); got != "thinking" {
		t.Fatalf("inserted content type = %q", got)
	}
	if got := gjson.GetBytes(patched, "messages.0.content.1.thinking").String(); got != "restored thought" {
		t.Fatalf("restored thinking = %q", got)
	}
	if !isValidGPTSignature(gjson.GetBytes(patched, "messages.0.content.1.signature").String()) {
		t.Fatal("restored thinking must have GPT-compatible signature")
	}
	if got := gjson.GetBytes(patched, "messages.0.content.2.type").String(); got != "tool_use" {
		t.Fatalf("tool_use moved incorrectly: %q", got)
	}
}

func TestPatchClaudeRequestRestoresEmptyThinkingBlock(t *testing.T) {
	cfg := resetTestState(t)
	reasoningEntries.Put([]string{"call_empty"}, "recovered thought")
	body := []byte(`{"messages":[{"role":"assistant","content":[
		{"type":"thinking","thinking":""},
		{"type":"tool_use","id":"call_empty","name":"lookup","input":{}}
	]}]}`)
	patched := patchClaudeRequest(body, cfg)
	if patched == nil {
		t.Fatal("expected empty thinking block to be restored")
	}
	if got := gjson.GetBytes(patched, "messages.0.content.0.thinking").String(); got != "recovered thought" {
		t.Fatalf("restored empty thinking = %q", got)
	}
	if !isValidGPTSignature(gjson.GetBytes(patched, "messages.0.content.0.signature").String()) {
		t.Fatal("restored thinking must have a compatible signature")
	}
}

func TestCaptureNonStreamingResponses(t *testing.T) {
	resetTestState(t)
	captureNonStreamingResponse([]byte(`{"choices":[{"message":{"reasoning_content":"openai reasoning","tool_calls":[{"id":"call_openai"}]}}]}`))
	if got, ok := reasoningEntries.Get([]string{"call_openai"}); !ok || got != "openai reasoning" {
		t.Fatalf("OpenAI capture = %q, %v", got, ok)
	}
	captureNonStreamingResponse([]byte(`{"content":[{"type":"thinking","thinking":"claude reasoning"},{"type":"tool_use","id":"call_claude"}]}`))
	if got, ok := reasoningEntries.Get([]string{"call_claude"}); !ok || got != "claude reasoning" {
		t.Fatalf("Claude capture = %q, %v", got, ok)
	}
}

func TestStreamTerminalDoesNotCountAsMalformed(t *testing.T) {
	resetTestState(t)
	captureStreamChunk([]byte("data: [DONE]\n\n"), nil, nil)
	if got := currentDashboardSnapshot(time.Now()).Errors.MalformedPayloads; got != 0 {
		t.Fatalf("terminal SSE malformed count = %d", got)
	}
}

func TestInterruptedOpenAIStreamRestoresReasoningOnResend(t *testing.T) {
	cfg := resetTestState(t)
	captureStreamChunk([]byte(`{"id":"resp_interrupted","choices":[{"index":0,"delta":{"reasoning_content":"Need to read the file first."},"finish_reason":null}]}`), nil, nil)
	captureStreamChunk([]byte(`{"id":"resp_interrupted","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_read","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},"finish_reason":null}]}`), nil, nil)

	resentRequest := []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_read","type":"function","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}]},{"role":"user","content":"continue"}]}`)
	patchedRequest := patchOpenAIRequest(resentRequest, cfg)
	if patchedRequest == nil {
		t.Fatal("expected interrupted tool call request to be repaired")
	}
	if got := gjson.GetBytes(patchedRequest, "messages.0.reasoning_content").String(); got != "Need to read the file first." {
		t.Fatalf("interrupted stream reasoning = %q", got)
	}
}

func TestInterruptedClaudeStreamRestoresThinkingOnResend(t *testing.T) {
	cfg := resetTestState(t)
	start := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_interrupted\"}}\n\n")
	thinking := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Need to read the file first.\"}}\n\n")
	tool := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_claude_read\",\"name\":\"read\",\"input\":{\"path\":\"README.md\"}}}\n\n")
	captureStreamChunk(start, nil, nil)
	captureStreamChunk(thinking, [][]byte{start}, nil)
	captureStreamChunk(tool, [][]byte{start, thinking}, nil)

	resentRequest := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_claude_read","name":"read","input":{"path":"README.md"}}]},{"role":"user","content":"continue"}]}`)
	patchedRequest := patchClaudeRequest(resentRequest, cfg)
	if patchedRequest == nil {
		t.Fatal("expected interrupted Claude tool call request to be repaired")
	}
	if got := gjson.GetBytes(patchedRequest, "messages.0.content.0.thinking").String(); got != "Need to read the file first." {
		t.Fatalf("interrupted Claude stream thinking = %q", got)
	}
	if got := gjson.GetBytes(patchedRequest, "messages.0.content.1.type").String(); got != "tool_use" {
		t.Fatalf("Claude tool_use moved incorrectly: %q", got)
	}
}

func TestActiveStreamRecoveryRejectsAmbiguousToolCallIDs(t *testing.T) {
	store := newStreamStore(time.Hour, 10)
	store.Add("openai:response-a:0", "reasoning A", []string{"call_duplicate"})
	store.Add("openai:response-b:0", "reasoning B", []string{"call_duplicate"})
	if reasoning, ok := store.FindReasoning([]string{"call_duplicate"}); ok {
		t.Fatalf("ambiguous active streams must not match, got %q", reasoning)
	}
}

func TestCaptureOpenAIStreamAccumulatesCompleteReasoning(t *testing.T) {
	resetTestState(t)
	captureStreamChunk([]byte(`{"id":"resp_1","choices":[{"index":0,"delta":{"reasoning_content":"I need "},"finish_reason":null}]}`), nil, nil)
	captureStreamChunk([]byte(`{"id":"resp_1","choices":[{"index":0,"delta":{"reasoning_content":"a tool"},"finish_reason":null}]}`), nil, nil)
	captureStreamChunk([]byte(`{"id":"resp_1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_stream"}]},"finish_reason":null}]}`), nil, nil)
	captureStreamChunk([]byte(`{"id":"resp_1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), nil, nil)
	if got, ok := reasoningEntries.Get([]string{"call_stream"}); !ok || got != "I need a tool" {
		t.Fatalf("stream reasoning = %q, %v", got, ok)
	}
}

func TestConcurrentStreamsInOneSessionUseProviderResponseIDs(t *testing.T) {
	resetTestState(t)
	metadata := map[string]any{"execution_session_id": "shared-session"}
	chunks := [][]byte{
		[]byte(`{"id":"resp_a","choices":[{"index":0,"delta":{"reasoning_content":"reason A","tool_calls":[{"id":"call_a"}]},"finish_reason":null}]}`),
		[]byte(`{"id":"resp_b","choices":[{"index":0,"delta":{"reasoning_content":"reason B","tool_calls":[{"id":"call_b"}]},"finish_reason":null}]}`),
		[]byte(`{"id":"resp_a","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		[]byte(`{"id":"resp_b","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
	}
	for _, chunk := range chunks {
		captureStreamChunk(chunk, nil, metadata)
	}
	if got, ok := reasoningEntries.Get([]string{"call_a"}); !ok || got != "reason A" {
		t.Fatalf("stream A reasoning = %q, %v", got, ok)
	}
	if got, ok := reasoningEntries.Get([]string{"call_b"}); !ok || got != "reason B" {
		t.Fatalf("stream B reasoning = %q, %v", got, ok)
	}
}

func TestCaptureClaudeSSEStreamUsesHistoryIdentity(t *testing.T) {
	resetTestState(t)
	start := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n")
	thinkingA := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Need \"}}\n\n")
	thinkingB := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"tool\"}}\n\n")
	tool := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_sse\"}}\n\n")
	finish := []byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
	captureStreamChunk(start, nil, nil)
	captureStreamChunk(thinkingA, [][]byte{start}, nil)
	captureStreamChunk(thinkingB, [][]byte{start, thinkingA}, nil)
	captureStreamChunk(tool, [][]byte{start, thinkingA, thinkingB}, nil)
	captureStreamChunk(finish, [][]byte{start, thinkingA, thinkingB, tool}, nil)
	if got, ok := reasoningEntries.Get([]string{"call_sse"}); !ok || got != "Need tool" {
		t.Fatalf("Claude stream reasoning = %q, %v", got, ok)
	}
}

func TestCaptureClaudeStreamUsesMetadataAfterHistoryEviction(t *testing.T) {
	resetTestState(t)
	metadata := map[string]any{"execution_session_id": "session-long"}
	thinking := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"long thought\"}}\n\n")
	tool := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_long\"}}\n\n")
	finish := []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	captureStreamChunk(thinking, nil, metadata)
	captureStreamChunk(tool, nil, metadata)
	captureStreamChunk(finish, nil, metadata)
	if got, ok := reasoningEntries.Get([]string{"call_long"}); !ok || got != "long thought" {
		t.Fatalf("metadata-correlated stream reasoning = %q, %v", got, ok)
	}
}

func TestAliasResponseCaptureStillRestoresDeepSeekRequest(t *testing.T) {
	resetTestState(t)
	responseReq := responseInterceptRequest{
		SourceFormat: "openai",
		Model:        "claude-opus-alias",
		RequestBody:  []byte(`{"model":"deepseek-v4-pro"}`),
		Body:         []byte(`{"choices":[{"message":{"reasoning_content":"alias reasoning","tool_calls":[{"id":"call_alias"}]}}]}`),
	}
	rawResponseReq, _ := json.Marshal(responseReq)
	if _, err := handleMethod("response.intercept_after", rawResponseReq); err != nil {
		t.Fatal(err)
	}

	requestReq := requestInterceptRequest{
		SourceFormat: "openai",
		Model:        "deepseek-v4-pro",
		Body:         []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_alias"}]}]}`),
	}
	rawRequestReq, _ := json.Marshal(requestReq)
	rawResult, err := handleMethod("request.intercept_after", rawRequestReq)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(interceptorBody(t, rawResult), "messages.0.reasoning_content").String(); got != "alias reasoning" {
		t.Fatalf("alias restored reasoning = %q", got)
	}
}

func TestShutdownClearsSensitiveState(t *testing.T) {
	resetTestState(t)
	reasoningEntries.Put([]string{"call_shutdown"}, "sensitive reasoning")
	streamEntries.Add("stream_shutdown", "partial", []string{"call_partial"})
	if _, err := handleMethod("plugin.shutdown", nil); err != nil {
		t.Fatal(err)
	}
	if reasoningEntries.Len() != 0 {
		t.Fatal("shutdown did not clear completed reasoning")
	}
	streamEntries.mu.Lock()
	streamCount := len(streamEntries.states)
	streamEntries.mu.Unlock()
	if streamCount != 0 {
		t.Fatal("shutdown did not clear stream state")
	}
}

func TestNonDeepSeekRequestIsBytePreserving(t *testing.T) {
	resetTestState(t)
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_other"}]}]}`)
	req := requestInterceptRequest{SourceFormat: "openai", Model: "gpt-5", Body: body}
	rawReq, _ := json.Marshal(req)
	rawResult, err := handleMethod("request.intercept_after", rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(rawResult, &env); err != nil {
		t.Fatal(err)
	}
	if string(env.Result) != "{}" {
		t.Fatalf("non-DeepSeek response = %s", env.Result)
	}
}

func TestConcurrentSessionsDoNotCrossToolCallIDs(t *testing.T) {
	resetTestState(t)
	const sessions = 100
	var wg sync.WaitGroup
	for index := 0; index < sessions; index++ {
		index := index
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "call_" + time.Unix(int64(index), 0).Format("150405")
			text := "reasoning_" + id
			reasoningEntries.Put([]string{id}, text)
			if got, ok := reasoningEntries.Get([]string{id}); !ok || got != text {
				t.Errorf("session %d got %q, %v", index, got, ok)
			}
		}()
	}
	wg.Wait()
}

func TestGeminiRequestNormalizerRestoresCachedReasoning(t *testing.T) {
	resetTestState(t)
	reasoningEntries.Put([]string{"call_gemini"}, "cached Gemini reasoning")

	request := requestTransformRequest{
		FromFormat: "gemini",
		ToFormat:   "openai",
		Model:      "deepseek-chat",
		Body:       []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_gemini","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`),
	}
	rawRequest, _ := json.Marshal(request)
	rawResponse, errHandle := handleMethod("request.normalize", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	body := interceptorBody(t, rawResponse)
	if got := gjson.GetBytes(body, "messages.0.reasoning_content").String(); got != "cached Gemini reasoning" {
		t.Fatalf("Gemini normalized reasoning_content = %q", got)
	}
}

func TestGeminiNonStreamingRoundTripRestoresReasoning(t *testing.T) {
	resetTestState(t)
	responseRequest := responseTransformRequest{
		FromFormat: "openai",
		ToFormat:   "gemini",
		Model:      "deepseek-chat",
		Body:       []byte(`{"choices":[{"message":{"reasoning_content":"reason before tool","tool_calls":[{"id":"call_roundtrip"}]}}]}`),
	}
	rawResponseRequest, _ := json.Marshal(responseRequest)
	if _, errHandle := handleMethod("response.normalize_before", rawResponseRequest); errHandle != nil {
		t.Fatal(errHandle)
	}

	request := requestTransformRequest{
		FromFormat: "gemini",
		ToFormat:   "openai",
		Model:      "deepseek-chat",
		Body:       []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_roundtrip","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_roundtrip","content":"ok"}]}`),
	}
	rawRequest, _ := json.Marshal(request)
	rawNormalized, errHandle := handleMethod("request.normalize", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	body := interceptorBody(t, rawNormalized)
	if got := gjson.GetBytes(body, "messages.0.reasoning_content").String(); got != "reason before tool" {
		t.Fatalf("round-trip reasoning_content = %q", got)
	}
}

func TestClaudeStreamingRoundTripRestoresReasoning(t *testing.T) {
	resetTestState(t)
	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_claude","choices":[{"index":0,"delta":{"reasoning_content":"streamed "},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_claude","choices":[{"index":0,"delta":{"reasoning_content":"reasoning","tool_calls":[{"index":0,"id":"call_stream_claude"}]},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_claude","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
	}
	for _, chunk := range chunks {
		responseRequest := responseTransformRequest{
			FromFormat: "openai",
			ToFormat:   "claude",
			Model:      "deepseek-chat",
			Stream:     true,
			Body:       chunk,
		}
		rawResponseRequest, _ := json.Marshal(responseRequest)
		if _, errHandle := handleMethod("response.normalize_before", rawResponseRequest); errHandle != nil {
			t.Fatal(errHandle)
		}
	}

	request := requestInterceptRequest{
		SourceFormat: "claude",
		Model:        "deepseek-chat",
		Body:         []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_stream_claude","name":"lookup","input":{}}]},{"role":"user","content":"continue"}]}`),
	}
	rawRequest, _ := json.Marshal(request)
	rawIntercepted, errHandle := handleMethod("request.intercept_after", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	body := interceptorBody(t, rawIntercepted)
	if got := gjson.GetBytes(body, "messages.0.content.0.thinking").String(); got != "streamed reasoning" {
		t.Fatalf("Claude round-trip thinking = %q", got)
	}
}

func TestGeminiStreamingRoundTripRestoresReasoning(t *testing.T) {
	resetTestState(t)
	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl_gemini","choices":[{"index":0,"delta":{"reasoning_content":"streamed "},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_gemini","choices":[{"index":0,"delta":{"reasoning_content":"reasoning","tool_calls":[{"index":0,"id":"call_stream_gemini"}]},"finish_reason":null}]}`),
		[]byte(`data: {"id":"chatcmpl_gemini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
	}
	for _, chunk := range chunks {
		responseRequest := responseTransformRequest{
			FromFormat: "openai",
			ToFormat:   "gemini",
			Model:      "deepseek-chat",
			Stream:     true,
			Body:       chunk,
		}
		rawResponseRequest, _ := json.Marshal(responseRequest)
		if _, errHandle := handleMethod("response.normalize_before", rawResponseRequest); errHandle != nil {
			t.Fatal(errHandle)
		}
	}

	request := requestTransformRequest{
		FromFormat: "gemini",
		ToFormat:   "openai",
		Model:      "deepseek-chat",
		Body:       []byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_stream_gemini","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`),
	}
	rawRequest, _ := json.Marshal(request)
	rawNormalized, errHandle := handleMethod("request.normalize", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	body := interceptorBody(t, rawNormalized)
	if got := gjson.GetBytes(body, "messages.0.reasoning_content").String(); got != "streamed reasoning" {
		t.Fatalf("stream round-trip reasoning_content = %q", got)
	}
}

func TestGeminiNormalizerIgnoresOtherFormatPairsAndModels(t *testing.T) {
	resetTestState(t)
	reasoningEntries.Put([]string{"call_ignored"}, "must not be injected")
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_ignored"}]}]}`)
	requests := []requestTransformRequest{
		{FromFormat: "openai", ToFormat: "openai", Model: "deepseek-chat", Body: body},
		{FromFormat: "gemini", ToFormat: "openai", Model: "gpt-5", Body: body},
	}
	for _, request := range requests {
		rawRequest, _ := json.Marshal(request)
		rawResponse, errHandle := handleMethod("request.normalize", rawRequest)
		if errHandle != nil {
			t.Fatal(errHandle)
		}
		var responseEnvelope envelope
		if errUnmarshal := json.Unmarshal(rawResponse, &responseEnvelope); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if string(responseEnvelope.Result) != "{}" {
			t.Fatalf("unexpected normalization for %+v: %s", request, responseEnvelope.Result)
		}
	}
}

func TestNonTargetResponsesDoNotPopulateCaches(t *testing.T) {
	resetTestState(t)
	nonTargetNormalized := responseTransformRequest{
		FromFormat: "openai",
		ToFormat:   "gemini",
		Model:      "gpt-5",
		Body:       []byte(`{"choices":[{"message":{"reasoning_content":"unrelated","tool_calls":[{"id":"call_unrelated_normalized"}]}}]}`),
	}
	rawNormalized, _ := json.Marshal(nonTargetNormalized)
	if _, errHandle := handleMethod("response.normalize_before", rawNormalized); errHandle != nil {
		t.Fatal(errHandle)
	}
	nonTargetNormalized.Stream = true
	nonTargetNormalized.Body = []byte(`data: {"id":"chatcmpl_unrelated_normalized","choices":[{"index":0,"delta":{"reasoning_content":"unrelated","tool_calls":[{"id":"call_unrelated_normalized"}]},"finish_reason":null}]}`)
	rawNormalizedStream, _ := json.Marshal(nonTargetNormalized)
	if _, errHandle := handleMethod("response.normalize_before", rawNormalizedStream); errHandle != nil {
		t.Fatal(errHandle)
	}

	nonStreaming := responseInterceptRequest{
		Model: "gpt-5",
		Body:  []byte(`{"choices":[{"message":{"reasoning_content":"unrelated","tool_calls":[{"id":"call_unrelated"}]}}]}`),
	}
	rawNonStreaming, _ := json.Marshal(nonStreaming)
	if _, errHandle := handleMethod("response.intercept_after", rawNonStreaming); errHandle != nil {
		t.Fatal(errHandle)
	}
	if reasoningEntries.Len() != 0 {
		t.Fatal("non-target non-streaming response polluted reasoning cache")
	}

	streaming := streamChunkInterceptRequest{
		Model:      "gpt-5",
		ChunkIndex: 0,
		Body:       []byte(`data: {"id":"chatcmpl_unrelated","choices":[{"index":0,"delta":{"reasoning_content":"unrelated","tool_calls":[{"id":"call_unrelated"}]},"finish_reason":null}]}`),
	}
	rawStreaming, _ := json.Marshal(streaming)
	if _, errHandle := handleMethod("response.intercept_stream_chunk", rawStreaming); errHandle != nil {
		t.Fatal(errHandle)
	}
	streamEntries.mu.Lock()
	activeStreams := len(streamEntries.states)
	streamEntries.mu.Unlock()
	if activeStreams != 0 {
		t.Fatalf("non-target streaming response created %d active stream entries", activeStreams)
	}
}

func TestGeminiResponseNormalizerUsesTranslatedModelForAliases(t *testing.T) {
	resetTestState(t)
	request := responseTransformRequest{
		FromFormat:        "openai",
		ToFormat:          "gemini",
		Model:             "provider-alias",
		TranslatedRequest: []byte(`{"model":"deepseek-chat"}`),
		Body:              []byte(`{"choices":[{"message":{"reasoning_content":"aliased Gemini reasoning","tool_calls":[{"id":"call_gemini_alias"}]}}]}`),
	}
	rawRequest, _ := json.Marshal(request)
	if _, errHandle := handleMethod("response.normalize_before", rawRequest); errHandle != nil {
		t.Fatal(errHandle)
	}
	if got, ok := reasoningEntries.Get([]string{"call_gemini_alias"}); !ok || got != "aliased Gemini reasoning" {
		t.Fatalf("aliased Gemini capture = %q, %v", got, ok)
	}
}

func TestRegistrationContainsOnlySupportedConfiguration(t *testing.T) {
	raw, err := registrationResponse()
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatal("registration envelope is not OK")
	}
	if got := gjson.GetBytes(env.Result, "metadata.Name").String(); got != "deepseek-reasoning-bridge" {
		t.Fatalf("registration name = %q", got)
	}
	if gjson.GetBytes(env.Result, "metadata.Version").String() != pluginVersion {
		t.Fatalf("registration = %s", env.Result)
	}
	for _, metadataPath := range []string{"metadata.Name", "metadata.Version", "metadata.Author", "metadata.GitHubRepository"} {
		if value := gjson.GetBytes(env.Result, metadataPath).String(); value == "" {
			t.Fatalf("required registration metadata %s is empty", metadataPath)
		}
	}
	for _, capabilityPath := range []string{
		"capabilities.request_normalizer",
		"capabilities.request_interceptor",
		"capabilities.response_before_translator",
		"capabilities.response_interceptor",
		"capabilities.response_stream_interceptor",
		"capabilities.management_api",
	} {
		if !gjson.GetBytes(env.Result, capabilityPath).Bool() {
			t.Fatalf("required registration capability %s is disabled", capabilityPath)
		}
	}
	if gjson.GetBytes(env.Result, `metadata.ConfigFields.#(name=="fix_misplaced_reasoning")`).Exists() {
		t.Fatal("unsafe misplaced-reasoning configuration must not be registered")
	}
}

func TestManagementRegistersStatusDashboard(t *testing.T) {
	raw, errHandle := handleMethod("management.register", []byte(`{}`))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if got := gjson.GetBytes(env.Result, "resources.0.Path").String(); got != "/status" {
		t.Fatalf("management status path = %q", got)
	}
	if got := gjson.GetBytes(env.Result, "resources.0.Menu").String(); got != pluginName {
		t.Fatalf("management menu = %q", got)
	}
}

func TestManagementDashboardServesHTMLAndJSONWithoutSensitiveData(t *testing.T) {
	cfg := resetTestState(t)
	cfg.PlaceholderText = "private-placeholder-marker"
	storeConfig(cfg)
	reasoningEntries.Put([]string{"private-tool-call-id"}, "private-reasoning-marker")

	htmlResponse := managementCall(t, managementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/resource/plugins/deepseek-reasoning-bridge/status",
		Headers: http.Header{"Accept": []string{"text/html"}},
	})
	if htmlResponse.StatusCode != http.StatusOK || !strings.Contains(htmlResponse.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("HTML management response = %+v", htmlResponse)
	}
	if !strings.Contains(string(htmlResponse.Body), pluginName) {
		t.Fatal("dashboard HTML is missing plugin name")
	}
	lowercaseHTML := strings.ToLower(string(htmlResponse.Body))
	if !strings.Contains(lowercaseHTML, "manual pull") {
		t.Fatal("dashboard HTML must identify metrics retrieval as manual pull")
	}
	for _, automaticRefreshMarker := range []string{
		`http-equiv="refresh"`,
		"setinterval(",
		"settimeout(",
	} {
		if strings.Contains(lowercaseHTML, automaticRefreshMarker) {
			t.Fatalf("dashboard HTML contains automatic refresh marker %q", automaticRefreshMarker)
		}
	}
	assertNoDashboardSecrets(t, htmlResponse.Body)

	jsonResponse := managementCall(t, managementRequest{
		Method:  http.MethodGet,
		Path:    "/v0/resource/plugins/deepseek-reasoning-bridge/status",
		Headers: http.Header{"Accept": []string{"application/json"}},
	})
	if jsonResponse.StatusCode != http.StatusOK || !strings.Contains(jsonResponse.Headers.Get("Content-Type"), "application/json") {
		t.Fatalf("JSON management response = %+v", jsonResponse)
	}
	if got := gjson.GetBytes(jsonResponse.Body, "plugin.version").String(); got != pluginVersion {
		t.Fatalf("dashboard version = %q", got)
	}
	if gjson.GetBytes(jsonResponse.Body, "privacy.reasoning_exposed").Bool() ||
		gjson.GetBytes(jsonResponse.Body, "privacy.tool_call_ids_exposed").Bool() ||
		gjson.GetBytes(jsonResponse.Body, "privacy.request_bodies_exposed").Bool() {
		t.Fatal("dashboard privacy declaration is unsafe")
	}
	assertNoDashboardSecrets(t, jsonResponse.Body)
}

func TestDashboardMetricsTrackRecoveryAndFallbacks(t *testing.T) {
	cfg := resetTestState(t)
	reasoningEntries.Put([]string{"cache-hit"}, "cached thought")
	patchOpenAIRequest([]byte(`{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"cache-hit"}]}]}`), cfg)
	patchOpenAIRequest([]byte(`{"messages":[{"role":"assistant","content":"fallback thought","tool_calls":[{"id":"cache-miss"}]}]}`), cfg)

	snapshot := currentDashboardSnapshot(time.Now())
	if snapshot.Restoration.ExactCacheHits != 1 || snapshot.Restoration.CacheMisses != 1 {
		t.Fatalf("recovery metrics = %+v", snapshot.Restoration)
	}
	if snapshot.Restoration.HitRate != 0.5 || snapshot.Restoration.ContentFallbacks != 1 || snapshot.Restoration.RepairedMessages != 2 {
		t.Fatalf("restoration metrics = %+v", snapshot.Restoration)
	}
	if snapshot.Cache.ReasoningEntries != 1 || snapshot.Capture.ReasoningWrites != 1 {
		t.Fatalf("cache metrics = cache %+v capture %+v", snapshot.Cache, snapshot.Capture)
	}
}

func TestDashboardSnapshotIsRaceSafe(t *testing.T) {
	resetTestState(t)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				runtimeMetrics.cacheHits.Add(1)
				reasoningEntries.Put([]string{fmt.Sprintf("worker-%d-%d", worker, iteration)}, "private")
				_ = currentDashboardSnapshot(time.Now())
			}
		}()
	}
	workers.Wait()
	if got := currentDashboardSnapshot(time.Now()).Restoration.ExactCacheHits; got != 800 {
		t.Fatalf("concurrent cache hits = %d", got)
	}
}

func managementCall(t *testing.T, request managementRequest) managementResponse {
	t.Helper()
	rawRequest, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawResponse, errHandle := handleMethod("management.handle", rawRequest)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var response managementResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return response
}

func assertNoDashboardSecrets(t *testing.T, body []byte) {
	t.Helper()
	for _, secret := range []string{"private-placeholder-marker", "private-tool-call-id", "private-reasoning-marker"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("dashboard exposed sensitive marker %q", secret)
		}
	}
}

func interceptorBody(t *testing.T, rawEnvelope []byte) []byte {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rawEnvelope, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("interceptor error: %+v", env.Error)
	}
	var response struct {
		Body []byte `json:"Body"`
	}
	if err := json.Unmarshal(env.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response.Body
}
