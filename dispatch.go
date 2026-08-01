package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcRegistration struct {
	SchemaVersion uint32         `json:"schema_version"`
	Metadata      pluginMetadata `json:"metadata"`
	Capabilities  pluginCaps     `json:"capabilities"`
}

type pluginMetadata struct {
	Name             string              `json:"Name"`
	Version          string              `json:"Version"`
	Author           string              `json:"Author"`
	GitHubRepository string              `json:"GitHubRepository"`
	Logo             string              `json:"Logo"`
	ConfigFields     []pluginConfigField `json:"ConfigFields"`
}

type pluginConfigField struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"description,omitempty"`
}

type pluginCaps struct {
	RequestNormalizer        bool `json:"request_normalizer"`
	RequestInterceptor       bool `json:"request_interceptor"`
	ResponseBeforeTranslator bool `json:"response_before_translator"`
	ResponseInterceptor      bool `json:"response_interceptor"`
	StreamChunkInterceptor   bool `json:"response_stream_interceptor"`
	ManagementAPI            bool `json:"management_api"`
}

type requestTransformRequest struct {
	FromFormat string `json:"FromFormat"`
	ToFormat   string `json:"ToFormat"`
	Model      string `json:"Model"`
	Stream     bool   `json:"Stream"`
	Body       []byte `json:"Body"`
}

type responseTransformRequest struct {
	FromFormat        string `json:"FromFormat"`
	ToFormat          string `json:"ToFormat"`
	Model             string `json:"Model"`
	Stream            bool   `json:"Stream"`
	OriginalRequest   []byte `json:"OriginalRequest"`
	TranslatedRequest []byte `json:"TranslatedRequest"`
	Body              []byte `json:"Body"`
}

type payloadResponse struct {
	Body []byte `json:"Body,omitempty"`
}

type requestInterceptRequest struct {
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

type requestInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
}

type responseInterceptRequest struct {
	SourceFormat    string         `json:"SourceFormat"`
	Model           string         `json:"Model"`
	RequestedModel  string         `json:"RequestedModel"`
	Stream          bool           `json:"Stream"`
	RequestHeaders  http.Header    `json:"RequestHeaders"`
	ResponseHeaders http.Header    `json:"ResponseHeaders"`
	OriginalRequest []byte         `json:"OriginalRequest"`
	RequestBody     []byte         `json:"RequestBody"`
	Body            []byte         `json:"Body"`
	StatusCode      int            `json:"StatusCode"`
	Metadata        map[string]any `json:"Metadata"`
}

type responseInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
}

type streamChunkInterceptRequest struct {
	SourceFormat    string         `json:"SourceFormat"`
	Model           string         `json:"Model"`
	RequestedModel  string         `json:"RequestedModel"`
	RequestHeaders  http.Header    `json:"RequestHeaders"`
	ResponseHeaders http.Header    `json:"ResponseHeaders"`
	OriginalRequest []byte         `json:"OriginalRequest"`
	RequestBody     []byte         `json:"RequestBody"`
	Body            []byte         `json:"Body"`
	HistoryChunks   [][]byte       `json:"HistoryChunks"`
	ChunkIndex      int            `json:"ChunkIndex"`
	Metadata        map[string]any `json:"Metadata"`
}

type streamChunkInterceptResponse struct {
	Headers      http.Header `json:"Headers,omitempty"`
	Body         []byte      `json:"Body,omitempty"`
	ClearHeaders []string    `json:"ClearHeaders,omitempty"`
	DropChunk    bool        `json:"DropChunk,omitempty"`
}

func handleMethod(method string, requestBytes []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return handleLifecycle(requestBytes)
	case "plugin.shutdown":
		reasoningEntries.Reset()
		streamEntries.Reset()
		return okEnvelopeJSON(`{}`)
	case "request.intercept_before":
		return okEnvelopeJSON(`{}`)
	case "request.normalize":
		return handleRequestNormalize(requestBytes)
	case "request.intercept_after":
		return handleRequestInterceptAfter(requestBytes)
	case "response.normalize_before":
		return handleResponseNormalizeBefore(requestBytes)
	case "response.intercept_after":
		return handleResponseInterceptAfter(requestBytes)
	case "response.intercept_stream_chunk":
		return handleStreamChunkIntercept(requestBytes)
	case "management.register":
		return managementRegistration()
	case "management.handle":
		return handleManagementMethod(requestBytes)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleLifecycle(requestBytes []byte) ([]byte, error) {
	var req rpcLifecycleRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		return nil, fmt.Errorf("decode lifecycle request: %w", err)
	}
	cfg, errConfig := loadConfig(req.ConfigYAML)
	if errConfig != nil {
		return nil, errConfig
	}
	storeConfig(cfg)
	return registrationResponse()
}

func registrationResponse() ([]byte, error) {
	reg := rpcRegistration{
		SchemaVersion: 1,
		Metadata: pluginMetadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           "cpa-usage-keeper",
			GitHubRepository: "https://github.com/router-for-me/cpa-plugin-deepseek-reasoning-bridge",
			ConfigFields: []pluginConfigField{
				{Name: "target_models", Type: "array", Description: "Model-name globs to act on."},
				{Name: "fallback_strategy", Type: "enum", EnumValues: []string{"content", "placeholder", "passthrough"}, Description: "Fallback used when no exact tool-call cache entry exists."},
				{Name: "placeholder_text", Type: "string", Description: "Text used by content and placeholder fallback modes."},
				{Name: "cache_ttl", Type: "string", Description: "Maximum lifetime of reasoning and stream state entries."},
				{Name: "cache_max_entries", Type: "integer", Description: "Maximum number of completed reasoning and active stream entries."},
				{Name: "cache_max_bytes", Type: "integer", Description: "Maximum total bytes retained by completed reasoning cache."},
				{Name: "stream_max_lifetime", Type: "string", Description: "Absolute lifetime limit for an unfinished stream."},
				{Name: "stream_idle_ttl", Type: "string", Description: "Idle timeout for an unfinished stream."},
				{Name: "stream_max_reasoning_bytes", Type: "integer", Description: "Maximum reasoning bytes retained for one unfinished stream."},
			},
		},
		Capabilities: pluginCaps{
			RequestNormalizer:        true,
			RequestInterceptor:       true,
			ResponseBeforeTranslator: true,
			ResponseInterceptor:      true,
			StreamChunkInterceptor:   true,
			ManagementAPI:            true,
		},
	}
	raw, errMarshal := json.Marshal(reg)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal registration: %w", errMarshal)
	}
	return okEnvelopeJSON(string(raw))
}

func handleRequestNormalize(requestBytes []byte) ([]byte, error) {
	var req requestTransformRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil, fmt.Errorf("decode request.normalize: %w", err)
	}
	runtimeMetrics.requestNormalizations.Add(1)
	if !formatPair(req.FromFormat, req.ToFormat, "gemini", "openai") {
		return okEnvelopeJSON(`{}`)
	}
	cfg := getConfig()
	if !hookTargetsModel(cfg.TargetModels, []string{req.Model}, req.Body) {
		runtimeMetrics.skippedNonTarget.Add(1)
		return okEnvelopeJSON(`{}`)
	}
	runtimeMetrics.observeProtocol("gemini")
	patched := patchOpenAIRequest(req.Body, cfg)
	if len(patched) == 0 {
		return okEnvelopeJSON(`{}`)
	}
	return marshalEnvelopeResult(payloadResponse{Body: patched})
}

func handleResponseNormalizeBefore(requestBytes []byte) ([]byte, error) {
	var req responseTransformRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil, fmt.Errorf("decode response.normalize_before: %w", err)
	}
	runtimeMetrics.responseNormalizations.Add(1)
	if !formatPair(req.FromFormat, req.ToFormat, "openai", "gemini") &&
		!formatPair(req.FromFormat, req.ToFormat, "openai", "claude") {
		return okEnvelopeJSON(`{}`)
	}
	cfg := getConfig()
	if !hookTargetsModel(cfg.TargetModels, []string{req.Model}, req.TranslatedRequest, req.OriginalRequest) {
		runtimeMetrics.skippedNonTarget.Add(1)
		return okEnvelopeJSON(`{}`)
	}
	if req.Stream {
		captureStreamChunk(req.Body, nil, nil)
	} else {
		captureNonStreamingResponse(req.Body)
	}
	return okEnvelopeJSON(`{}`)
}

func formatPair(from, to, wantFrom, wantTo string) bool {
	return strings.EqualFold(strings.TrimSpace(from), wantFrom) && strings.EqualFold(strings.TrimSpace(to), wantTo)
}

func handleRequestInterceptAfter(requestBytes []byte) ([]byte, error) {
	var req requestInterceptRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil, fmt.Errorf("decode request.intercept_after: %w", err)
	}
	runtimeMetrics.requestInterceptions.Add(1)

	cfg := getConfig()
	if !hookTargetsModel(cfg.TargetModels, []string{req.Model, req.RequestedModel}, req.Body) {
		runtimeMetrics.skippedNonTarget.Add(1)
		return okEnvelopeJSON(`{}`)
	}

	var patched []byte
	switch strings.ToLower(strings.TrimSpace(req.SourceFormat)) {
	case "claude":
		runtimeMetrics.observeProtocol("claude")
		patched = patchClaudeRequest(req.Body, cfg)
	case "openai":
		runtimeMetrics.observeProtocol("openai")
		patched = patchOpenAIRequest(req.Body, cfg)
	default:
		return okEnvelopeJSON(`{}`)
	}
	if len(patched) == 0 {
		return okEnvelopeJSON(`{}`)
	}
	return marshalEnvelopeResult(requestInterceptResponse{Body: patched})
}

func handleResponseInterceptAfter(requestBytes []byte) ([]byte, error) {
	var req responseInterceptRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil, fmt.Errorf("decode response.intercept_after: %w", err)
	}
	runtimeMetrics.responseInterceptions.Add(1)
	cfg := getConfig()
	if !hookTargetsModel(cfg.TargetModels, []string{req.Model, req.RequestedModel}, req.RequestBody, req.OriginalRequest) {
		runtimeMetrics.skippedNonTarget.Add(1)
		return okEnvelopeJSON(`{}`)
	}
	captureNonStreamingResponse(req.Body)
	return okEnvelopeJSON(`{}`)
}

func handleStreamChunkIntercept(requestBytes []byte) ([]byte, error) {
	var req streamChunkInterceptRequest
	if err := json.Unmarshal(requestBytes, &req); err != nil {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil, fmt.Errorf("decode response.intercept_stream_chunk: %w", err)
	}
	cfg := getConfig()
	if !hookTargetsModel(cfg.TargetModels, []string{req.Model, req.RequestedModel}, req.RequestBody, req.OriginalRequest) {
		runtimeMetrics.skippedNonTarget.Add(1)
		return okEnvelopeJSON(`{}`)
	}
	if req.ChunkIndex < 0 {
		return okEnvelopeJSON(`{}`)
	}
	runtimeMetrics.streamChunks.Add(1)
	captureStreamChunk(req.Body, req.HistoryChunks, req.Metadata)
	return okEnvelopeJSON(`{}`)
}

func hookTargetsModel(patterns []string, modelNames []string, requestBodies ...[]byte) bool {
	for _, modelName := range modelNames {
		if modelMatches(modelName, patterns) {
			return true
		}
	}
	for _, body := range requestBodies {
		if modelMatches(gjson.GetBytes(body, "model").String(), patterns) {
			return true
		}
	}
	return false
}

func marshalEnvelopeResult(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal interceptor response: %w", errMarshal)
	}
	return okEnvelopeJSON(string(raw))
}
