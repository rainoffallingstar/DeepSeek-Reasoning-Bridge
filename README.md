# DeepSeek Reasoning Bridge

[中文](#中文) | [English](#english)

## 中文

`deepseek-reasoning-bridge` 是一个 CPA（CLIProxyAPI）动态插件，用于在工具调用的多轮交互以及 OpenAI、Anthropic、Gemini 协议转换过程中保留 DeepSeek 推理内容。

版本 `0.5.2` 已在本机 CPA `v7.2.88` 环境完成实际测试，包括流式 tool call 中断后继续会话的恢复场景。

### 功能

- 捕获与工具调用关联的 `reasoning_content` 和 Claude `thinking`。
- 根据完整、精确的工具调用 ID 集合，在下一次请求中恢复推理内容。
- 支持 OpenAI、Claude 和 Gemini 协议链路。
- 支持流式和非流式请求。
- 流式 tool call 在结束帧到达前中断时，可从未完成流状态恢复已接收的推理内容。
- 精确推理内容不可用时，支持 `content`、`placeholder` 和 `passthrough` 回退策略。
- 推理内容仅保存在内存中，不改写响应正文或 SSE 事件。

### 构建

需要 Go 1.23+ 并启用 cgo。

```bash
make test
make build
```

macOS 构建产物为 `bin/deepseek-reasoning-bridge.dylib`，Linux 为 `.so`，Windows 为 `.dll`。

### 部署到 CPA

将构建产物复制到 CPA 当前版本对应的平台插件目录。以 macOS arm64 为例：

```bash
cp bin/deepseek-reasoning-bridge.dylib \
  "$HOME/Library/Application Support/Quotio/proxy/upstream/<version>/plugins/darwin/arm64/deepseek-reasoning-bridge-v0.5.2.dylib"
```

文件名应包含插件版本号。热重载时，新版本号必须高于已经部署的版本，CPA 才会选择新动态库。

在 CPA 配置文件的 `plugins.configs` 下添加：

```yaml
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

复制动态库后，修改一次 CPA 配置以触发插件热重载。部署过程中不要删除或修改其他 CPA 数据文件。

### 运行监控

Dashboard 地址：

```text
/v0/resource/plugins/deepseek-reasoning-bridge/status
```

监控指标默认为手动拉取。页面或接口仅在收到请求时生成一次快照，不会自动刷新或在后台轮询。添加 `?format=json`，或发送 `Accept: application/json` 请求头，可获取 JSON 数据。

Dashboard 仅提供聚合运行指标，包括协议流量、缓存命中率、回退使用量、修复消息数、流状态、缓存生命周期事件和关联错误。它不会暴露推理文本、工具调用 ID、请求正文、API Key 或其他凭据。

CPA 资源路由可能不受管理接口认证保护。请使用与其他 CPA 资源页面相同的监听地址、反向代理和网络访问控制。

### 配置项

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `target_models` | `['deepseek-*']` | 插件处理的模型模式，不区分大小写。 |
| `fallback_strategy` | `content` | 可选值：`content`、`placeholder`、`passthrough`。 |
| `placeholder_text` | `[reasoning unavailable]` | 使用占位回退策略时填充的文本。 |
| `cache_ttl` | `2h` | 推理缓存和未完成流状态的有效期。 |
| `cache_max_entries` | `10000` | 已完成推理缓存和活动流状态各自的最大条目数。 |

推理内容仅存储在进程内存中，CPA 或插件重启后会丢失。精确恢复依赖客户端原样回传唯一的工具调用 ID。插件升级后，请使用新产生的 tool call 验证；升级前已经丢失的原始 reasoning 无法事后恢复。

---

## English

`deepseek-reasoning-bridge` is a dynamic CPA (CLIProxyAPI) plugin that preserves DeepSeek reasoning across tool-call turns and OpenAI, Anthropic, and Gemini protocol conversions.

Version `0.5.2` has been tested successfully on a local CPA `v7.2.88` installation, including continuing a conversation after a streaming tool call is interrupted.

### Features

- Captures `reasoning_content` and Claude `thinking` associated with tool calls.
- Restores reasoning on the next request using the complete, exact tool-call ID set.
- Supports OpenAI, Claude, and Gemini protocol paths.
- Supports streaming and non-streaming requests.
- Recovers received reasoning from unfinished stream state when a tool call is interrupted before its terminal frame.
- Supports `content`, `placeholder`, and `passthrough` fallback strategies when exact reasoning is unavailable.
- Keeps reasoning in memory and never rewrites response bodies or SSE events.

### Build

Go 1.23+ with cgo enabled is required.

```bash
make test
make build
```

The build output is `bin/deepseek-reasoning-bridge.dylib` on macOS, `.so` on Linux, or `.dll` on Windows.

### Install In CPA

Copy the library into the platform plugin directory for the active CPA version. For macOS arm64:

```bash
cp bin/deepseek-reasoning-bridge.dylib \
  "$HOME/Library/Application Support/Quotio/proxy/upstream/<version>/plugins/darwin/arm64/deepseek-reasoning-bridge-v0.5.2.dylib"
```

Use a versioned filename. For hot reload, the new version must be higher than every previously deployed version so CPA selects the new library.

Add the plugin under `plugins.configs` in the CPA configuration:

```yaml
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

After copying the library, change the CPA configuration once to trigger plugin hot reload. Do not delete or modify unrelated CPA data files during deployment.

### Runtime Dashboard

Dashboard path:

```text
/v0/resource/plugins/deepseek-reasoning-bridge/status
```

Metrics use manual pull by default. The page or endpoint generates one snapshot only when requested; it does not auto-refresh or poll in the background. Append `?format=json` or send `Accept: application/json` for JSON output.

The Dashboard exposes aggregate metrics only, including protocol traffic, cache hit rate, fallback usage, repaired messages, stream activity, cache lifecycle events, and correlation errors. It never exposes reasoning text, tool-call IDs, request bodies, API keys, or credentials.

CPA resource routes may not be protected by management authentication. Apply the same listener, reverse-proxy, and network access controls used for other CPA resource pages.

### Configuration

| Field | Default | Description |
|---|---|---|
| `target_models` | `['deepseek-*']` | Case-insensitive model patterns handled by the plugin. |
| `fallback_strategy` | `content` | One of `content`, `placeholder`, or `passthrough`. |
| `placeholder_text` | `[reasoning unavailable]` | Text used by placeholder fallback. |
| `cache_ttl` | `2h` | Lifetime of reasoning cache and unfinished stream state. |
| `cache_max_entries` | `10000` | Maximum entries for completed reasoning and active streams. |

Reasoning is stored in process memory only and is lost when CPA or the plugin restarts. Exact recovery depends on clients replaying unique tool-call IDs unchanged. After upgrading the plugin, validate with newly generated tool calls; reasoning already lost before the upgrade cannot be reconstructed afterward.
