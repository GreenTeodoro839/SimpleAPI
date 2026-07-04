# 轻量级 AI 协议代理开发文档

## 1. 目标

本项目是一个使用 Go 编写的轻量级 AI API 代理服务，配置来源只使用 `config.yaml`，不引入数据库。服务对外提供三类兼容接口：

- Anthropic Messages API：`/v1/messages`
- OpenAI Chat Completions API：`/v1/chat/completions`
- Codex / OpenAI Responses API：`/v1/responses`

上游提供商只支持三种类型：

- `anthropic`
- `openai_completion`
- `codex`

设计参考 CLIProxyAPI 的协议兼容、模型别名、失败切换和 Anthropic `web_search` 透明转发思路，但刻意去掉多账号 OAuth、数据库、复杂控制台、插件、多 provider 扩展等能力。

## 2. 非目标

- 不做数据库、队列或外部状态存储。
- 不做一个上游配置挂多个 API key。
- 不做 Gemini、Qwen、Grok、Vertex、OpenRouter 聚合等额外 provider。
- 不做复杂计费系统。使用统计只按 provider、`aliasA`、真实模型、协议等内部维度记录。
- 不把客户端层别名 `aliasB` 作为统计主键。

## 3. 名词定义

`provider`

上游提供商配置项。每个 provider 只有一套 `name`、`type`、`url`、`headers`、`key`。`name` 和 `url` 必填，`name` 不能包含 `_`。

`aliasA`

上游模型在主配置里的内部别名。若为空，默认等于真实模型名 `model`。

`internal model id`

配置和授权层用来唯一确定模型的 ID，格式为：

```text
providerName_aliasA
```

解析时按第一个 `_` 切分。因为 provider `name` 禁止包含 `_`，所以 `aliasA` 可被完整保留。

`aliasB`

客户端调用时看到和填写的模型名。`aliasB` 只在某个入站 API key 的授权范围内生效。若为空，默认等于对应模型的 `aliasA`。

`protocol`

客户端访问本代理时使用的接口协议。固定为：

- `anthropic`
- `openai_completion`
- `codex`

`provider type`

上游 provider 的真实协议类型，枚举同上。客户端协议和上游 provider type 相同时透传，不同时进入协议翻译。

## 4. 配置模型

配置文件固定为 `config.yaml`。推荐根结构：

- `version`：配置版本。
- `server`：监听地址、超时。
- `proxy`：失败切换、响应 model 重写、统计等代理行为。
- `management`：管理 API 开关和管理密钥。
- `payload`：出站请求 payload 规则配置，可按模型、协议、请求头和请求体条件改写或删除字段。
- `providers`：上游 provider 列表。
- `api_keys`：入站 API key 及其协议、模型授权。

`providers[].models[].model` 是上游真实模型名。`providers[].models[].aliasA` 是内部别名。

`api_keys[].models[].model` 必须填写 `providerName_aliasA`。`api_keys[].models[].aliasB` 是客户端可见模型名。

## 5. 配置校验规则

启动时必须完整校验 `config.yaml`。校验失败时打印明确错误并退出。

管理 API 修改配置时，也必须对修改后的完整配置做同样校验。校验失败时返回 `422 Unprocessable Entity`，不得写入文件，不得更新运行时配置。

必需校验项：

- `providers[].name` 必填，不能包含 `_`。
- `providers[].url` 必填。
- `providers[].type` 必须是 `anthropic`、`openai_completion`、`codex` 之一。
- provider `name` 全局唯一。
- 同一个 provider 下，生效后的 `aliasA` 不能重复。
- 由 `providerName_aliasA` 组成的 internal model id 必须全局唯一。
- `api_keys[].name` 全局唯一。
- `api_keys[].key` 全局唯一。
- `api_keys[].allowed_protocols` 只能包含三种固定协议。
- `api_keys[].models[].model` 必须能解析到已存在的 internal model id。
- `api_keys[].models[].priority` 缺省为 `0`。
- `api_keys[].models[].aliasB` 缺省为目标模型的 `aliasA`。
- `anthropic_web_search_forward.target_model` 若启用，必须指向已存在的 internal model id。
- `payload.default-raw` 和 `payload.override-raw` 中的每个参数值必须是合法 JSON 片段。
- `payload.*[].models[].protocol` 和 `payload.*[].models[].from-protocol` 只能是 `anthropic`、`openai_completion`、`codex`。
- `payload.*[].params` 不允许修改或删除顶层 `model` 字段，防止绕过路由和别名规则。

重复 provider name 或同一 provider 下重复 `aliasA` 是硬错误。运行期 Web API 必须拒绝此类修改；若用户手动把 `config.yaml` 改成这样，服务重启或 reload 时必须报错退出或拒绝 reload。

## 6. 启动流程

1. 读取 `config.yaml`。
2. 展开环境变量形式的密钥占位符，例如 `${ANTHROPIC_API_KEY}`。
3. 做完整配置校验。
4. 构建 provider 索引：`providerName -> provider`。
5. 构建模型索引：`providerName_aliasA -> provider model`。
6. 构建每个入站 API key 的模型路由表：`aliasB -> []candidate`。
7. 对同一个 `aliasB` 的候选模型按 `priority` 从大到小排序；同优先级保持配置顺序。
8. 启动 HTTP 服务。

## 7. 入站认证与授权

客户端请求使用 Bearer token：

```http
Authorization: Bearer <client-api-key>
```

也可兼容 `x-api-key`，但推荐只在文档中主推 Bearer token。

认证成功后，服务拿到对应的 `api_keys[]` 配置，并执行：

- 当前请求路径对应的 protocol 必须存在于 `allowed_protocols`。
- 请求体里的 `model` 必须是该 key 下某个模型配置的 `aliasB`。
- 如果同一 key 下有多个相同 `aliasB`，按优先级和失败状态选择实际 internal model id。

## 8. 请求路由行为

客户端以 `aliasB` 作为 `model` 调用代理。

处理步骤：

1. 根据 URL 路径确定客户端 protocol。
2. 认证入站 API key。
3. 检查该 key 是否允许当前 protocol。
4. 从请求体读取 `model`，作为 `aliasB`。
5. 在该 key 的模型路由表中查找 `aliasB` 对应的候选模型。
6. 按优先级从高到低选择候选模型。
7. 跳过已达到最大连续失败次数的候选模型。
8. 将请求体中的 `model` 改为上游真实模型名。
9. 若客户端 protocol 等于上游 provider type，透传请求。
10. 若不同，执行协议翻译。
11. 在最终出站 payload 上应用 `payload` 规则。
12. 调用上游。
13. 返回客户端前，将响应中的 `model` 字段改回客户端请求使用的 `aliasB`。

响应 model 重写必须覆盖流式和非流式两种情况：

- 非流式 JSON：重写顶层 `model`。
- SSE 流：逐个 data chunk 重写其中的 `model` 字段；不解析失败的 chunk 原样转发或中断，具体策略应在实现中保持一致。

## 9. 失败切换

主配置提供 `proxy.max_consecutive_failures`。

候选模型调用失败后，给该 API key 下对应的 internal model id 增加连续失败计数。达到阈值后，后续同一 `aliasB` 请求跳过该候选模型，尝试下一个优先级候选。

建议失败判定包括：

- 网络连接失败。
- 请求超时。
- 上游返回 `408`、`429`、`500`、`502`、`503`、`504`。
- 上游流式响应在首个有效 chunk 前失败。

不建议把客户端错误计入上游失败：

- `400`
- `401`
- `403`
- 请求体格式错误
- 模型不存在
- 入站 key 无权限

成功响应后，将该候选模型的连续失败计数清零。

可选实现 `proxy.failure_reset_seconds`：候选模型超过该时间没有新失败，可自动清零失败状态，避免一次故障永久影响路由。

## 10. Anthropic Web Search 转发

Anthropic 请求中如果包含 server-side web search 工具，例如：

```json
{"type": "web_search_20250305"}
```

且当前选中的 provider model 配置了：

```yaml
anthropic_web_search_forward:
  enabled: true
  target_model: "providerName_aliasA"
```

则该请求改为转发到 `target_model`。

行为要求：

- 只检查 Anthropic 协议请求。
- `tools[].type` 以 `web_search` 开头即视为触发。
- 请求体保持原样，仅路由目标和上游真实 `model` 改变。
- 如果目标模型等于当前模型，不转发，避免自循环。
- 返回客户端时，`model` 仍改回原始 `aliasB`，让转发对客户端透明。
- 统计记录实际承接请求的 target model 的 provider、`aliasA` 和真实模型。

## 11. Payload 规则配置

Payload 规则用于在请求已经完成鉴权、模型路由、`web_search` 转发决策、协议翻译和上游真实 `model` 改写之后，对最终发给上游的 JSON payload 做显式调整。

支持五类规则，执行顺序固定为：

1. `default`：仅当目标 JSON path 不存在时写入普通 JSON 值。同一路径多个规则命中时，先写入的规则生效。
2. `default-raw`：仅当目标 JSON path 不存在时写入原始 JSON 片段。同一路径多个规则命中时，先写入的规则生效。
3. `override`：无论目标 JSON path 是否存在，都写入普通 JSON 值。同一路径多个规则命中时，后写入的规则生效。
4. `override-raw`：无论目标 JSON path 是否存在，都写入原始 JSON 片段。同一路径多个规则命中时，后写入的规则生效。
5. `filter`：删除匹配的 JSON path。

规则结构参考 CLIProxyAPI，但只保留本项目需要的三种协议：

```yaml
payload:
  override:
    - models:
        - name: "codex-main_codex5"
          protocol: "codex"
          from-protocol: "codex"
          headers:
            X-Client-Tier: "dev-*"
          match:
            - "metadata.client": "codex"
          not-match:
            - "metadata.mode": "debug"
          exist:
            - "input"
          not-exist:
            - "metadata.disable_payload"
      params:
        "reasoning.effort": "medium"
```

`models[].name` 支持精确匹配和 `*` 通配符。匹配候选值建议按以下顺序实现：

- internal model id：`providerName_aliasA`
- `aliasA`
- 上游真实模型名

配置时推荐优先写 internal model id，因为它全局唯一，不受客户端 `aliasB` 影响。

`models[].protocol` 表示最终出站 provider type。`models[].from-protocol` 表示客户端入站 protocol。两者都可省略；省略代表不限制。

`models[].headers` 要求请求头全部匹配，值支持 `*` 通配符。header 名建议大小写不敏感。

`match`、`not-match`、`exist`、`not-exist` 用于根据 payload 内容缩小规则范围：

- `match`：所有 JSON path 必须等于配置值。
- `not-match`：所有 JSON path 必须不等于配置值。
- `exist`：所有 JSON path 必须存在且不是 `null`。
- `not-exist`：所有 JSON path 必须不存在或为 `null`。

`params` 使用 JSON path 写法。Go 实现建议直接使用成熟库处理 JSON path 读写，例如 `tidwall/gjson` 和 `tidwall/sjson`，不要用字符串拼接改 JSON。

`default-raw` 和 `override-raw` 的参数值是原始 JSON 片段，适合设置对象或数组：

```yaml
payload:
  override-raw:
    - models:
        - name: "openai-main_gpt41mini"
          protocol: "openai_completion"
      params:
        "response_format": "{\"type\":\"json_object\"}"
```

如果 Anthropic `web_search` 触发转发，payload 规则应以最终承接请求的 target model 作为模型匹配对象，但 `from-protocol` 仍保留客户端原始协议。

## 12. 协议翻译边界

同协议不做字段重组，只处理认证头、URL、`model` 重写、显式配置的 payload 规则、响应 `model` 重写。

跨协议翻译只覆盖三种协议之间的基础文本、图片、工具调用和流式响应映射。建议第一版明确限制：

- 支持 system/user/assistant 基础消息。
- 支持普通文本内容。
- 支持 Anthropic content block 与 OpenAI message content 的最小互转。
- 支持 OpenAI Chat Completions stream 与 Anthropic SSE stream 的基础文本增量互转。
- 支持 Codex Responses 的 input/output 文本基础映射。
- 对复杂工具调用、reasoning、cache_control、computer use、内置工具等字段先做最佳努力透传或返回明确不支持错误。

跨协议翻译失败时返回 `400` 或 `501`，不要伪造上游错误。

## 13. 使用统计

统计维度必须使用内部模型维度：

- provider name
- provider type
- `aliasA`
- upstream actual model
- internal model id
- source protocol
- target provider type
- HTTP 状态
- token 用量，如果上游返回

不要以 `aliasB` 作为主统计维度。`aliasB` 可以作为请求标签记录，但不能作为聚合主键，因为它只属于某个入站 API key 的客户端命名空间。

由于不使用数据库，统计默认只保存在内存中。重启丢失是可接受行为。如需长期统计，应后续通过日志采集或外部系统实现。

## 14. 管理 API 行为

管理 API 只操作 `config.yaml` 和当前内存配置。

写入流程：

1. 接收完整配置或局部修改。
2. 在内存中合成修改后的完整配置。
3. 运行完整校验。
4. 校验失败返回 `422`，响应包含具体字段路径和错误原因。
5. 校验成功后写入临时文件。
6. flush 后原子 rename 覆盖 `config.yaml`。
7. 重新加载运行时索引。

管理 API 必须要求管理密钥，例如：

```http
X-Admin-Key: <admin-key>
```

默认不要对公网开放管理 API。推荐监听 `127.0.0.1`，或由反向代理负责 TLS 和访问控制。

## 15. 建议目录结构

```text
cmd/proxy/              # 程序入口
internal/config/        # YAML 读取、校验、索引构建、原子写入
internal/auth/          # 入站 API key 认证与授权
internal/router/        # aliasB -> internal model id 路由与失败切换
internal/provider/      # provider 请求执行
internal/translate/     # 三种协议之间的翻译
internal/payload/       # payload 规则匹配、校验和 JSON path 改写
internal/websearch/     # Anthropic web_search 触发检测和转发决策
internal/usage/         # 内存统计
internal/management/    # 管理 API
internal/httpapi/       # 对外三类兼容接口
```

以上只是组织建议，不代表必须拆成这些包。

## 16. 错误响应约定

代理自身产生的错误统一返回 JSON：

```json
{
  "error": {
    "code": "model_not_allowed",
    "message": "model alias is not allowed for this api key",
    "details": {}
  }
}
```

建议错误码：

- `invalid_request`
- `unauthorized`
- `protocol_not_allowed`
- `model_not_allowed`
- `model_not_found`
- `no_available_upstream`
- `translation_not_supported`
- `upstream_error`
- `config_validation_failed`

上游错误同协议场景可尽量透传，但不得泄露其他 API key 或管理配置。

## 17. 验收标准

第一版完成时至少满足：

- 能从 `config.yaml` 启动。
- 配置中 provider name 重复时启动失败。
- 同一 provider 下 `aliasA` 重复时启动失败。
- 管理 API 修改出重复 name 或重复 `aliasA` 时返回 `422`，文件不变。
- 三个对外接口都能按入站 API key 鉴权。
- 客户端使用 `aliasB` 请求，服务能路由到 `providerName_aliasA` 并改写成真实模型。
- 同协议透传。
- 跨协议进入翻译层。
- 返回客户端时 `model` 改回 `aliasB`。
- 同一 API key 下相同 `aliasB` 能按优先级和连续失败次数切换。
- Anthropic `web_search` 请求能透明转发到配置的 target model。
- Payload 规则能按 internal model id、协议、请求头、payload 条件命中，并按 `default`、`default-raw`、`override`、`override-raw`、`filter` 顺序生效。
- 非法 raw JSON payload 规则在启动和管理 API 写入时被拒绝。
- 使用统计聚合使用 `aliasA` / internal model id，而不是 `aliasB`。
