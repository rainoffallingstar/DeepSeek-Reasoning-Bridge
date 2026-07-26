package main

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultCacheTTL        = 2 * time.Hour
	defaultCacheMaxEntries = 10_000
)

// PluginConfig holds the plugin configuration supplied under
// plugins.configs.deepseek-reasoning-bridge.
type PluginConfig struct {
	TargetModels     []string
	FallbackStrategy string
	PlaceholderText  string
	CacheTTL         time.Duration
	CacheMaxEntries  int
}

type rawPluginConfig struct {
	TargetModels     yaml.Node `yaml:"target_models"`
	FallbackStrategy string    `yaml:"fallback_strategy"`
	PlaceholderText  string    `yaml:"placeholder_text"`
	CacheTTL         string    `yaml:"cache_ttl"`
	CacheMaxEntries  *int      `yaml:"cache_max_entries"`
}

var (
	configMu      sync.RWMutex
	currentConfig = defaultConfig()
)

func defaultConfig() PluginConfig {
	return PluginConfig{
		TargetModels:     []string{"deepseek-*"},
		FallbackStrategy: "content",
		PlaceholderText:  "[reasoning unavailable]",
		CacheTTL:         defaultCacheTTL,
		CacheMaxEntries:  defaultCacheMaxEntries,
	}
}

func loadConfig(yamlBytes []byte) (PluginConfig, error) {
	cfg := defaultConfig()
	if len(yamlBytes) == 0 {
		return cfg, nil
	}

	var raw rawPluginConfig
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return PluginConfig{}, fmt.Errorf("decode plugin config: %w", err)
	}

	if raw.TargetModels.Kind != 0 {
		models, errModels := decodeTargetModels(raw.TargetModels)
		if errModels != nil {
			return PluginConfig{}, errModels
		}
		cfg.TargetModels = models
	}
	if value := strings.ToLower(strings.TrimSpace(raw.FallbackStrategy)); value != "" {
		switch value {
		case "content", "placeholder", "passthrough":
			cfg.FallbackStrategy = value
		default:
			return PluginConfig{}, fmt.Errorf("unsupported fallback_strategy %q", value)
		}
	}
	if value := strings.TrimSpace(raw.PlaceholderText); value != "" {
		cfg.PlaceholderText = value
	}
	if value := strings.TrimSpace(raw.CacheTTL); value != "" {
		ttl, errParse := time.ParseDuration(value)
		if errParse != nil || ttl <= 0 {
			return PluginConfig{}, fmt.Errorf("invalid cache_ttl %q", value)
		}
		cfg.CacheTTL = ttl
	}
	if raw.CacheMaxEntries != nil {
		if *raw.CacheMaxEntries <= 0 {
			return PluginConfig{}, fmt.Errorf("cache_max_entries must be positive")
		}
		cfg.CacheMaxEntries = *raw.CacheMaxEntries
	}

	cfg.TargetModels = normalizePatterns(cfg.TargetModels)
	return cfg, nil
}

func decodeTargetModels(node yaml.Node) ([]string, error) {
	switch node.Kind {
	case yaml.ScalarNode:
		return normalizePatterns(strings.Split(node.Value, ",")), nil
	case yaml.SequenceNode:
		models := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item == nil || item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("target_models entries must be strings")
			}
			models = append(models, item.Value)
		}
		return normalizePatterns(models), nil
	default:
		return nil, fmt.Errorf("target_models must be a string or list")
	}
}

func storeConfig(cfg PluginConfig) {
	reasoningEntries.Reset()
	streamEntries.Reset()
	reasoningEntries.Configure(cfg.CacheTTL, cfg.CacheMaxEntries)
	streamEntries.Configure(cfg.CacheTTL, cfg.CacheMaxEntries)
	configMu.Lock()
	currentConfig = cfg
	configMu.Unlock()
}

func getConfig() PluginConfig {
	configMu.RLock()
	defer configMu.RUnlock()
	return currentConfig
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.ToLower(strings.TrimSpace(pattern))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return []string{"deepseek-*"}
	}
	return out
}

func modelMatches(model string, patterns []string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, pattern := range patterns {
		if matched, errMatch := path.Match(pattern, model); errMatch == nil && matched {
			return true
		}
	}
	return false
}
