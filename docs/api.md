# Web API 说明

SimpleAPI 对外暴露两类 HTTP 接口：

- **客户端接口**：三个大模型兼容接口 + 模型列表，用入站 API key 鉴权。
- **管理接口**：在线管理配置与查看运行状态，用管理密钥鉴权。

默认监听 `http://127.0.0.1:8317`（由 `server.listen` 决定）。配置来源仅为 `config.yaml`，不使用数据库；管理接口的变更会原子写回 `config.yaml` 并热生效。

> 接口契约的形式化定义见仓库根目录的 `web_api.openapi.yaml`。本文是带示例的人读版。

---

## 1. 认证

### 客户端接口

客户端请求用入站 API key 鉴权，两种方式任选其一：

- `Authorization: Bearer <client-api-key>`（推荐）
- `x-api-key: <client-api-key>`

key 对应 `config.yaml` 里 `api_keys[].key`（支持 `${VAR}` 环境变量占位）。认证后，服务会按该 key 的 `allowed_protocols` 与 `models`（`aliasB`）授权。

### 管理接口

- 所有管理端点（除 `/-/health` 外）都要求请求头 `X-Admin-Key: <admin-key>`，与 `management.admin_key` 常量时间比较。
- `management.enabled: false` 时整套管理接口（含 `/-/health` 之外）禁用。
- 建议仅监听 `127.0.0.1`，或由反向代理负责 TLS 与访问控制；默认不要对公网开放管理接口。

---

## 2. 请求 / 响应约定

- 请求体均为 JSON（`Content-Type: application/json`）。
- 客户端请求体里的 `model` 字段填该 API key 下允许的 **`aliasB`**；服务据此路由到内部模型 `providerName/aliasA`，把 `model` 改写成上游真实模型名后再调用上游。
- 响应里的 `model` 字段会被改回客户端传入的 `aliasB`（受 `proxy.rewrite_response_model` 控制，默认开）。
- 是否流式由请求体顶层布尔 `stream` 决定（三种协议一致）；流式响应为 `text/event-stream`。
- 同协议请求透传；跨协议请求进入翻译层（文本 / 图片 / 工具调用，流式 + 非流式）。

---

## 3. 客户端接口

### POST `/v1/messages` —— Anthropic Messages 兼容

```bash
curl http://127.0.0.1:8317/v1/messages \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "claude",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

- `model` 必须是该 key 下允许的 `aliasB`。
- 若目标上游也是 anthropic 则透传；否则翻译成对应协议后再调用，响应翻译回 anthropic 形态。
- 流式：加 `"stream": true`，返回 SSE。

### POST `/v1/chat/completions` —— OpenAI Chat Completions 兼容

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{"model": "gpt-mini", "messages": [{"role": "user", "content": "hi"}]}'
```

### POST `/v1/responses` —— Codex / OpenAI Responses 兼容

```bash
curl http://127.0.0.1:8317/v1/responses \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{
    "model": "codex",
    "input": [{"type": "message", "role": "user",
               "content": [{"type": "input_text", "text": "hi"}]}]
  }'
```

### GET `/v1/models` —— 当前 key 可见模型

返回该 API key 可见的 `aliasB` 列表（同一 `aliasB` 多候选只出现一次）。

```bash
curl -H "Authorization: Bearer $CLIENT_API_KEY" http://127.0.0.1:8317/v1/models
```

```json
{"object": "list", "data": [{"id": "claude", "object": "model"},
                             {"id": "gpt-mini", "object": "model"}]}
```

---

## 4. 管理接口

基础路径由 `management.base_path` 决定（默认 `/v0/management`）。下面示例假设默认值。所有写接口都会先做完整配置校验：通过则原子写回 `config.yaml` 并重建运行时索引；失败返回 `422`，**不写文件、不生效**。

校验/写入结果统一返回：

```json
{"valid": true}
```

或失败时：

```json
{"valid": false,
 "errors": [{"path": "providers[1].name", "code": "duplicate_provider",
             "message": "duplicate provider name \"x\""}]}
```

### 健康检查

- `GET /-/health` —— 免鉴权。

```bash
curl http://127.0.0.1:8317/-/health        # {"status":"ok"}
```

### 配置整体

- `GET /v0/management/config` —— 返回完整配置（含明文 `providers[].key`、`api_keys[].key`、`management.admin_key`，不脱敏）。

```bash
curl -H "X-Admin-Key: $PROXY_ADMIN_KEY" http://127.0.0.1:8317/v0/management/config
```

- `PUT /v0/management/config` —— 用请求体整体替换配置（接受 JSON 或 YAML）。

```bash
curl -X PUT -H "X-Admin-Key: $PROXY_ADMIN_KEY" -H "Content-Type: application/json" \
  --data-binary @config.json http://127.0.0.1:8317/v0/management/config
```

- `POST /v0/management/validate` —— 只校验、不写入。

```bash
curl -X POST -H "X-Admin-Key: $PROXY_ADMIN_KEY" -H "Content-Type: application/json" \
  --data-binary @config.json http://127.0.0.1:8317/v0/management/validate
```

- `POST /v0/management/reload` —— 重新读取磁盘上的 `config.yaml` 并校验；失败则保持当前运行配置不变、返回 `422`。

```bash
curl -X POST -H "X-Admin-Key: $PROXY_ADMIN_KEY" http://127.0.0.1:8317/v0/management/reload
```

### Payload 规则

- `GET /v0/management/payload` —— 返回当前 `payload` 节。
- `PUT /v0/management/payload` —— 替换 `payload` 节（会合成完整配置后整体校验；raw 值必须是合法 JSON 片段，且不得改写/删除顶层 `model` 字段）。

```bash
curl -X PUT -H "X-Admin-Key: $PROXY_ADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"override":[{"models":[{"name":"openai-main/gpt41mini"}],
                    "params":{"temperature":0.2}}]}' \
  http://127.0.0.1:8317/v0/management/payload
```

### Providers

- `GET /v0/management/providers` —— provider 列表（含明文 key）。
- `POST /v0/management/providers` —— 新增 provider；重复 `name` 或同 provider 下重复 `aliasA` 返回 `422`。

```bash
curl -X POST -H "X-Admin-Key: $PROXY_ADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"name":"openrouter","type":"openai_completion","url":"https://openrouter.ai/api/v1",
       "key":"sk-...","headers":{},"models":[{"model":"m","aliasA":"a"}]}' \
  http://127.0.0.1:8317/v0/management/providers
```

- `GET /v0/management/providers/:name` —— 单个 provider（含明文 key）；不存在返回 `404`。
- `PUT /v0/management/providers/:name` —— 替换单个 provider。
- `DELETE /v0/management/providers/:name` —— 删除；若仍有 API key 引用该 provider 的模型，返回 `422 provider_in_use`。

```bash
curl -X DELETE -H "X-Admin-Key: $PROXY_ADMIN_KEY" http://127.0.0.1:8317/v0/management/providers/openrouter
```

### API Keys

- `GET /v0/management/api-keys` —— 入站 key 列表（含明文 key）。
- `POST /v0/management/api-keys` —— 新增；重复 `name` 或重复 key 值返回 `422`。
- `GET /v0/management/api-keys/:keyName` / `PUT /v0/management/api-keys/:keyName` / `DELETE /v0/management/api-keys/:keyName`

```bash
curl -X POST -H "X-Admin-Key: $PROXY_ADMIN_KEY" -H "Content-Type: application/json" \
  -d '{"name":"dev","key":"${CLIENT_API_KEY_DEV}",
       "allowed_protocols":["anthropic","openai_completion","codex"],
       "models":[{"model":"openai-main/gpt41mini","aliasB":"gpt-mini"}]}' \
  http://127.0.0.1:8317/v0/management/api-keys
```

> `:name` / `:keyName` 路径参数不应包含 `/`。

### 内部模型索引

- `GET /v0/management/models` —— 以 internal id（`providerName/aliasA`）为维度的模型列表。

```bash
curl -H "X-Admin-Key: $PROXY_ADMIN_KEY" http://127.0.0.1:8317/v0/management/models
```

```json
{"models":[{"id":"anthropic-main/sonnet4","provider":"anthropic-main",
            "provider_type":"anthropic","aliasA":"sonnet4",
            "upstream_model":"claude-sonnet-4-20250514"}]}
```

### 用量统计

- `GET /v0/management/usage` —— 内存统计，按内部维度聚合（**不**含 `aliasB`）。重启丢失。

```bash
curl -H "X-Admin-Key: $PROXY_ADMIN_KEY" http://127.0.0.1:8317/v0/management/usage
```

```json
{"items":[{"provider":"openai-main","provider_type":"openai_completion",
           "aliasA":"gpt41mini","upstream_model":"gpt-4.1-mini",
           "internal_model":"openai-main/gpt41mini",
           "source_protocol":"openai_completion","target_provider_type":"openai_completion",
           "http_status":200,"requests":12,"failures":0,
           "input_tokens":234,"output_tokens":567,
           "cache_read_tokens":0,"cache_creation_tokens":0,"cached_tokens":120,
           "reasoning_tokens":48,"total_tokens":801}]}
```

> 由 `proxy.usage_statistics_enabled` 控制是否记录。token（含缓存/推理 token）从响应中抽取：流式从 SSE 事件（anthropic `message_start`/`message_delta`、codex `response.completed`、openai chunk usage），非流式从响应体 `usage`。维度：anthropic 取 `cache_read_input_tokens` / `cache_creation_input_tokens`；openai/codex 取 `prompt_tokens_details.cached_tokens` 与 `*_details.reasoning_tokens`；`total_tokens` 优先取上游上报值，缺省时按 input+output(+cache) 推算。

### 调用记录

- `GET /v0/management/call-log` —— 最近 N 条上游调用记录（每条=一次上游尝试；同一次客户端请求因失败切换会产生多条，共享同一 `request_id`）。按时间倒序，**非破坏性**读取（不删除记录）。内存环形缓冲，重启丢失。

```bash
curl -H "X-Admin-Key: $PROXY_ADMIN_KEY" \
  'http://127.0.0.1:8317/v0/management/call-log?limit=20'
```

```json
{"items":[{
  "request_id":"req-7","timestamp":"2026-07-04T22:45:10Z",
  "endpoint":"POST /v1/messages","api_key":"dev-all",
  "source_protocol":"anthropic","alias":"claude",
  "provider":"anthropic-main","provider_type":"anthropic",
  "model":"claude-sonnet-4-20250514","internal_model":"anthropic-main/sonnet4",
  "http_status":200,"latency_ms":842,"failed":false,
  "tokens":{"input_tokens":156,"output_tokens":40,
            "cache_read_tokens":120,"cache_creation_tokens":0,
            "cached_tokens":0,"reasoning_tokens":0,"total_tokens":316}}]}
```

> 由 `proxy.call_log_max_entries`（默认 1000，`0`=关闭）控制环形缓冲容量；**仅启动时生效，改后需重启**。`api_key` 返回的是入站 key 的 **name**（非密钥明文）。调用记录与 `usage_statistics_enabled` 相互独立。

---

## 5. 错误响应

代理自身错误统一返回 JSON：

```json
{"error":{"code":"model_not_allowed",
          "message":"...","details":{}}}
```

常见错误码：

| HTTP | code                       | 含义                                       |
|------|----------------------------|--------------------------------------------|
| 400  | `invalid_request`          | 请求体格式错误、缺 `model` 字段等         |
| 401  | `unauthorized`             | 缺少或无效的客户端 key / 管理密钥         |
| 403  | `protocol_not_allowed`     | 该 key 不允许当前协议                     |
| 403  | `model_not_allowed`        | （保留）模型授权相关                      |
| 404  | `model_not_found`          | `aliasB` 在该 key 下不存在                |
| 404  | `not_found`                | 管理接口的资源不存在                      |
| 502  | `no_available_upstream`    | 所有候选都失败                            |
| 502  | `upstream_error`           | 上游调用失败                              |
| 501  | `translation_not_supported`| 无可用的跨协议翻译器                      |
| 422  | （`ValidationResult`）     | 管理 API 配置校验失败（返回 `valid:false` + `errors`，不走上面的 error 信封） |

上游错误在同协议透传场景下会尽量原样透传，但不会泄露其他 key 或管理配置。

---

## 6. 说明

- 所有写管理接口都遵循"先合成完整配置 → 完整校验 → 原子写盘 → 重建索引"的顺序，校验失败时磁盘文件与运行时状态都不变。
- `provider.name` 不能含 `/`（用于 internal id 切分）；可以含 `_`。
- `api_keys[].models[].model` 与 `anthropic_web_search_forward.target_model` 必须填已存在的 internal id（`providerName/aliasA`）。
- payload 规则的 `params` 不允许修改或删除顶层 `model` 字段（防止绕过路由与别名）。
