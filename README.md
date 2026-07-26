# DeepSeek Reasoning Bridge

`deepseek-reasoning-bridge` 是一个 CPA（CLIProxyAPI）动态插件，用于在工具调用的多轮交互以及 OpenAI、Anthropic、Gemini 协议转换过程中保留 DeepSeek 推理内容。

该插件已在本机 CPA `v7.2.88` 环境中完成实际测试。

## 功能

- 捕获与工具调用关联的 `reasoning_content` 和 Claude `thinking`。
- 根据完整、精确的工具调用 ID 集合，在下一次请求中恢复推理内容。
- 支持流式和非流式请求。
- 支持 OpenAI、Claude 和 Gemini 协议链路。
- 精确推理内容不可用时，支持可配置的回退策略。
- 推理内容仅保存在内存中，不改写响应正文或 SSE 事件。

## 构建

需要 Go 1.23+ 并启用 cgo。

```bash
make test
make build
```

macOS 构建产物为 `bin/deepseek-reasoning-bridge.dylib`，Linux 为 `.so`，Windows 为 `.dll`。

## 部署到 CPA

将构建产物复制到 CPA 当前版本对应的平台插件目录。以 macOS arm64 为例：

```bash
cp bin/deepseek-reasoning-bridge.dylib \
  "$HOME/Library/Application Support/Quotio/proxy/upstream/<version>/plugins/darwin/arm64/deepseek-reasoning-bridge-v0.5.0.dylib"
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

## 运行监控

Dashboard 地址：

```text
/v0/resource/plugins/deepseek-reasoning-bridge/status
```

监控指标默认为手动拉取。页面或接口仅在收到请求时生成一次快照，不会自动刷新或在后台轮询。添加 `?format=json`，或发送 `Accept: application/json` 请求头，可获取 JSON 数据。

Dashboard 仅提供聚合运行指标，包括协议流量、缓存命中率、回退使用量、修复消息数、流状态、缓存生命周期事件和关联错误。它不会暴露推理文本、工具调用 ID、请求正文、API Key 或其他凭据。

CPA 资源路由可能不受管理接口认证保护。请使用与其他 CPA 资源页面相同的监听地址、反向代理和网络访问控制。

## 配置项

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `target_models` | `['deepseek-*']` | 插件处理的模型模式，不区分大小写。 |
| `fallback_strategy` | `content` | 可选值：`content`、`placeholder`、`passthrough`。 |
| `placeholder_text` | `[reasoning unavailable]` | 使用占位回退策略时填充的文本。 |
| `cache_ttl` | `2h` | 推理缓存和未完成流状态的有效期。 |
| `cache_max_entries` | `10000` | 已完成推理缓存和活动流状态各自的最大条目数。 |

推理内容仅存储在进程内存中，CPA 或插件重启后会丢失。精确恢复依赖客户端原样回传唯一的工具调用 ID。
