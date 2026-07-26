package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type fallbackKind string

const (
	fallbackKindContent     fallbackKind = "content"
	fallbackKindPlaceholder fallbackKind = "placeholder"
	fallbackKindPassthrough fallbackKind = "passthrough"
)

func patchOpenAIRequest(body []byte, cfg PluginConfig) []byte {
	if !gjson.ValidBytes(body) {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil
	}

	out := body
	changed := false
	for messageIndex, message := range messages.Array() {
		if strings.TrimSpace(message.Get("role").String()) != "assistant" {
			continue
		}
		toolCalls := message.Get("tool_calls")
		toolCallIDs, complete := completeOpenAIToolCallIDs(toolCalls)
		if !complete || len(toolCallIDs) == 0 {
			if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
				runtimeMetrics.missingToolCallIDs.Add(1)
			}
			continue
		}

		reasoning := message.Get("reasoning_content")
		if reasoning.Type == gjson.String && strings.TrimSpace(reasoning.String()) != "" {
			reasoningEntries.Put(toolCallIDs, reasoning.String())
			continue
		}

		text, ok := reasoningEntries.Get(toolCallIDs)
		if ok {
			runtimeMetrics.cacheHits.Add(1)
		} else {
			runtimeMetrics.cacheMisses.Add(1)
			var fallback fallbackKind
			text, fallback, ok = fallbackOpenAIReasoning(message, cfg)
			recordFallback(fallback)
		}
		if !ok {
			continue
		}
		updated, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.reasoning_content", messageIndex), text)
		if errSet != nil {
			continue
		}
		out = updated
		changed = true
		runtimeMetrics.repairedMessages.Add(1)
	}
	if !changed {
		return nil
	}
	return out
}

func patchClaudeRequest(body []byte, cfg PluginConfig) []byte {
	if !gjson.ValidBytes(body) {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		runtimeMetrics.malformedPayloads.Add(1)
		return nil
	}

	out := body
	changed := false
	for messageIndex, message := range messages.Array() {
		if strings.TrimSpace(message.Get("role").String()) != "assistant" {
			continue
		}
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}

		toolCallIDs, complete := completeClaudeToolUseIDs(content)
		if !complete || len(toolCallIDs) == 0 {
			if content.IsArray() && countClaudeToolUseParts(content) > 0 {
				runtimeMetrics.missingToolCallIDs.Add(1)
			}
			continue
		}

		thinkingIndexes, thinkingText := claudeThinkingParts(content)
		if len(thinkingIndexes) > 0 {
			if strings.TrimSpace(thinkingText) != "" {
				reasoningEntries.Put(toolCallIDs, thinkingText)
			} else {
				thinkingText, ok := reasoningEntries.Get(toolCallIDs)
				if ok {
					runtimeMetrics.cacheHits.Add(1)
				} else {
					runtimeMetrics.cacheMisses.Add(1)
					var fallback fallbackKind
					thinkingText, fallback, ok = fallbackClaudeReasoning(message, cfg)
					recordFallback(fallback)
				}
				if ok {
					updated, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.content.%d.thinking", messageIndex, thinkingIndexes[0]), thinkingText)
					if errSet == nil {
						out = updated
						changed = true
						runtimeMetrics.repairedMessages.Add(1)
					}
				}
			}
			for _, contentIndex := range thinkingIndexes {
				signature := content.Array()[contentIndex].Get("signature").String()
				if isValidGPTSignature(signature) {
					continue
				}
				updated, errSet := sjson.SetBytes(out, fmt.Sprintf("messages.%d.content.%d.signature", messageIndex, contentIndex), synthesizeGPTSignature())
				if errSet != nil {
					continue
				}
				out = updated
				changed = true
			}
			continue
		}

		text, ok := reasoningEntries.Get(toolCallIDs)
		if ok {
			runtimeMetrics.cacheHits.Add(1)
		} else {
			runtimeMetrics.cacheMisses.Add(1)
			var fallback fallbackKind
			text, fallback, ok = fallbackClaudeReasoning(message, cfg)
			recordFallback(fallback)
		}
		if !ok {
			continue
		}
		patchedContent, okInsert := insertClaudeThinking(content, text, synthesizeGPTSignature())
		if !okInsert {
			continue
		}
		updated, errSet := sjson.SetRawBytes(out, fmt.Sprintf("messages.%d.content", messageIndex), patchedContent)
		if errSet != nil {
			continue
		}
		out = updated
		changed = true
		runtimeMetrics.repairedMessages.Add(1)
	}
	if !changed {
		return nil
	}
	return out
}

func openAIToolCallIDs(toolCalls gjson.Result) []string {
	if !toolCalls.IsArray() {
		return nil
	}
	ids := make([]string, 0, len(toolCalls.Array()))
	for _, toolCall := range toolCalls.Array() {
		if id := strings.TrimSpace(toolCall.Get("id").String()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func completeOpenAIToolCallIDs(toolCalls gjson.Result) ([]string, bool) {
	if !toolCalls.IsArray() {
		return nil, false
	}
	ids := make([]string, 0, len(toolCalls.Array()))
	for _, toolCall := range toolCalls.Array() {
		id := strings.TrimSpace(toolCall.Get("id").String())
		if id == "" {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func completeClaudeToolUseIDs(content gjson.Result) ([]string, bool) {
	if !content.IsArray() {
		return nil, false
	}
	ids := make([]string, 0)
	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_use" {
			continue
		}
		id := strings.TrimSpace(part.Get("id").String())
		if id == "" {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func countClaudeToolUseParts(content gjson.Result) int {
	count := 0
	for _, part := range content.Array() {
		if part.Get("type").String() == "tool_use" {
			count++
		}
	}
	return count
}

func claudeToolUseIDs(content gjson.Result) []string {
	ids := make([]string, 0)
	for _, part := range content.Array() {
		if part.Get("type").String() != "tool_use" {
			continue
		}
		if id := strings.TrimSpace(part.Get("id").String()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func claudeThinkingParts(content gjson.Result) ([]int, string) {
	indexes := make([]int, 0)
	texts := make([]string, 0)
	for index, part := range content.Array() {
		if part.Get("type").String() != "thinking" {
			continue
		}
		indexes = append(indexes, index)
		if text := part.Get("thinking").String(); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return indexes, strings.Join(texts, "\n\n")
}

func fallbackOpenAIReasoning(message gjson.Result, cfg PluginConfig) (string, fallbackKind, bool) {
	switch cfg.FallbackStrategy {
	case "passthrough":
		return "", fallbackKindPassthrough, false
	case "placeholder":
		return cfg.PlaceholderText, fallbackKindPlaceholder, strings.TrimSpace(cfg.PlaceholderText) != ""
	default:
		if text := extractOpenAIContent(message.Get("content")); strings.TrimSpace(text) != "" {
			return text, fallbackKindContent, true
		}
		return cfg.PlaceholderText, fallbackKindPlaceholder, strings.TrimSpace(cfg.PlaceholderText) != ""
	}
}

func fallbackClaudeReasoning(message gjson.Result, cfg PluginConfig) (string, fallbackKind, bool) {
	switch cfg.FallbackStrategy {
	case "passthrough":
		return "", fallbackKindPassthrough, false
	case "placeholder":
		return cfg.PlaceholderText, fallbackKindPlaceholder, strings.TrimSpace(cfg.PlaceholderText) != ""
	default:
		parts := make([]string, 0)
		for _, part := range message.Get("content").Array() {
			if part.Get("type").String() == "text" {
				if text := strings.TrimSpace(part.Get("text").String()); text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), fallbackKindContent, true
		}
		return cfg.PlaceholderText, fallbackKindPlaceholder, strings.TrimSpace(cfg.PlaceholderText) != ""
	}
}

func extractOpenAIContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	parts := make([]string, 0)
	for _, part := range content.Array() {
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func insertClaudeThinking(content gjson.Result, text, signature string) ([]byte, bool) {
	parts := content.Array()
	if len(parts) == 0 {
		return nil, false
	}
	thinking, errMarshal := json.Marshal(map[string]string{
		"type":      "thinking",
		"thinking":  text,
		"signature": signature,
	})
	if errMarshal != nil {
		return nil, false
	}

	out := make([]json.RawMessage, 0, len(parts)+1)
	inserted := false
	for _, part := range parts {
		if !inserted && part.Get("type").String() == "tool_use" {
			out = append(out, thinking)
			inserted = true
		}
		if json.Valid([]byte(part.Raw)) {
			out = append(out, json.RawMessage(part.Raw))
		}
	}
	if !inserted {
		return nil, false
	}
	raw, errMarshal := json.Marshal(out)
	return raw, errMarshal == nil
}
