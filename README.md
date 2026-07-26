# DeepSeek Reasoning Bridge

`deepseek-reasoning-bridge` is a dynamic CPA (CLIProxyAPI) plugin that preserves DeepSeek reasoning state across tool-call turns and OpenAI, Anthropic, and Gemini protocol translations without changing CPA core.

DeepSeek returns `reasoning_content` with an assistant tool call and requires the same value to be replayed in the next request. Clients and OpenAI/Anthropic/Gemini conversions can otherwise drop it, causing HTTP 400 responses such as:

> The reasoning_content in the thinking mode must be passed back to the API.

## Behavior

The plugin registers CPA request/response interceptors plus request and pre-translation response normalizers.

- Response interceptors capture only complete reasoning associated with a non-empty set of tool-call IDs.
- Streaming reasoning deltas are accumulated per response before being cached.
- The cache key is the sorted, exact tool-call ID set—not the model name—so unrelated turns do not share a model-wide value.
- OpenAI requests missing `reasoning_content` recover it from the exact tool-call entry.
- Claude requests recover or preserve a `thinking` block and receive a structurally GPT-compatible signature immediately before CPA translates the next request.
- Gemini responses are observed in their upstream OpenAI form before CPA translates them. On the next turn, the Gemini request normalizer restores `reasoning_content` after CPA's Gemini-to-OpenAI conversion.
- Response bodies and SSE events are observed but never rewritten.
- The issue #37635 `reasoning_content` → `content` rewrite is intentionally not implemented because ordinary DeepSeek thinking chunks have the same shape and cannot be distinguished safely.

If no exact cache entry exists, the configured fallback is used. `content` first uses assistant answer text and then the placeholder; `placeholder` always uses the placeholder; `passthrough` leaves the request unchanged.

## Build and test

Requires Go 1.23+ with cgo enabled.

```bash
make test
make build
```

The library is written to `bin/deepseek-reasoning-bridge.dylib` on macOS, `.so` on Linux, or `.dll` on Windows.

## Install

Copy the shared library into CPA's plugin directory, then configure CPA:

```yaml
openai-compatibility:
  - name: "deepseek"
    base-url: "https://api.deepseek.com/v1"
    api-key-entries:
      - api-key: "sk-..."
    models:
      - name: "deepseek-chat"
        alias: "deepseek-chat"
        thinking:
          levels: ["low", "medium", "high"]

plugins:
  enabled: true
  dir: "plugins"
  configs:
    deepseek-reasoning-bridge:
      enabled: true
      priority: 10
      target_models:
        - "deepseek-*"
      fallback_strategy: "content"
      placeholder_text: "[reasoning unavailable]"
      cache_ttl: "2h"
      cache_max_entries: 10000
```

Restart CPA after installing or replacing the library.

## Runtime dashboard

Version `0.5.0` registers a read-only CPA Management API resource. Open the following URL on the CPA server:

```text
/v0/resource/plugins/deepseek-reasoning-bridge/status
```

The page reports uptime, protocol traffic, exact-cache hit rate, fallback usage, repaired messages, cache occupancy, active/completed streams, expiration/eviction counts, and correlation errors. Metrics are retrieved only when the page or endpoint is requested; there is no automatic refresh or background polling. Reload the page manually to obtain a new snapshot. Request the same URL with `Accept: application/json`, or append `?format=json`, for a machine-readable snapshot.

CPA resource routes are not management-authenticated. Apply the same listener, reverse-proxy, and network access controls used for other CPA resource pages. The plugin intentionally exposes aggregate counters and a limited configuration summary only; it never includes reasoning text, tool-call IDs, request bodies, placeholder text, API keys, or credentials. Counters are in-memory and reset when the plugin process restarts.

## Migrating from `deepseek-thinking`

Version `0.4.0` adopts the formal plugin ID `deepseek-reasoning-bridge`. Remove the old `deepseek-thinking` library, install the newly named library, and rename `plugins.configs.deepseek-thinking` to `plugins.configs.deepseek-reasoning-bridge` before restarting CPA. Running both identities at once would register duplicate hooks and is not supported.

## Configuration

| Field | Type | Default | Description |
|---|---|---|---|
| `target_models` | string or list | `["deepseek-*"]` | Case-insensitive shell globs checked by request and response hooks. |
| `fallback_strategy` | enum | `content` | `content`, `placeholder`, or `passthrough`. Exact tool-call cache recovery always takes priority. |
| `placeholder_text` | string | `[reasoning unavailable]` | Last-resort text for `content`, or fixed text for `placeholder`. |
| `cache_ttl` | duration | `2h` | Lifetime of completed reasoning entries and unfinished stream state. |
| `cache_max_entries` | integer | `10000` | Independent maximum for completed entries and active streams. Oldest entries are evicted first. |

## Safety and limitations

- Reasoning is held in process memory for the configured TTL. It is not logged or persisted; a plugin/CPA restart loses exact Gemini round-trip recovery for earlier turns.
- Correlation relies on provider-generated tool-call IDs being unique and replayed unchanged by the client.
- CPA exposes the Gemini request normalizer only after native Gemini-to-OpenAI conversion, so an externally seeded Gemini history cannot have `thought:true` separated from ordinary text by this plugin. Exact cache recovery takes priority; otherwise the configured fallback applies.
- The synthetic Claude signature is a CPA transport-compatibility marker. It is attached on the next request, not injected into Anthropic response events.
- The default target remains `deepseek-*`; other providers require explicit model patterns and compatibility testing.
