# SimpleAPI

轻量级 AI 协议代理，用 Go 编写。只读 `config.yaml`、不引入数据库，把三类主流大模型 API 统一成三个客户端兼容接口，并按入站 API key 细分授权。

设计参考 [CLIProxyAPI](https://github.com/GreenTeodoro839/CLIProxyAPI) 的协议兼容、模型别名、失败切换与 Anthropic `web_search` 透明转发，但刻意去掉了多账号 OAuth、数据库、插件、多 provider 聚合等能力，保持单文件配置、易部署。

- 完整规格见 [`DEVELOPMENT.md`](DEVELOPMENT.md)
- 接口契约见 [`web_api.openapi.yaml`](web_api.openapi.yaml)
- 接口说明（含 curl 示例）见 [`docs/api.md`](docs/api.md)

## 特性

- **三类客户端接口**：`/v1/messages`（anthropic）、`/v1/chat/completions`（openai_completion）、`/v1/responses`（codex），外加 `/v1/models`。
- **三种上游 provider**：`anthropic`、`openai_completion`、`codex`。同协议透传，跨协议翻译（文本 / 图片 / 工具调用，流式 + 非流式，共 6 个方向）。
- **模型别名路由**：客户端用 `aliasB` 调用，路由到 `providerName/aliasA`，响应里的 `model` 自动改回 `aliasB`。
- **按 API key 授权**：每个入站 key 单独配置允许的协议与模型；同一 `aliasB` 可挂多个候选，按 `priority` 排序，连续失败自动切换。
- **Payload 规则**：`default / default-raw / override / override-raw / filter` 五类，按 internal id / 协议 / 请求头 / payload 内容匹配，用 JSON path 改写出站请求体。
- **Anthropic web_search 透明转发**：带 `web_search_*` 工具的请求可转发到指定 target 模型。
- **管理 API**：在线增删改 provider / key / payload、整体替换配置、reload、查看 usage，原子写回 `config.yaml` 并热生效。
- **内存用量统计**：按 provider / `aliasA` / internal id 聚合（**不**用 `aliasB`），重启丢失。

## 模型身份

- 内部模型 id 形如 **`providerName/aliasA`**，按第一个 `/` 切分（因此 provider `name` 不能含 `/`，但可以含 `_`）。
- `aliasA` 是 provider 模型的内部别名（为空则等于上游真实模型名）。
- `aliasB` 是客户端看到的名字（为空则等于 `aliasA`），只在某个入站 API key 的范围内生效。
- 客户端请求体里的 `model` 填 `aliasB`；统计与 payload 规则匹配用 `aliasA` / internal id，不用 `aliasB`。

## 快速开始

```bash
go build -o ./bin/proxy ./cmd/proxy

# 设置必要的环境变量（密钥用 ${VAR} 占位，启动时展开）
export PROXY_ADMIN_KEY=change-me
# 取消 config.yaml 里 providers / api_keys 的注释并填入你自己的上游与客户端 key
./bin/proxy -config config.yaml
```

启动参数：`-config`（默认 `config.yaml`）、`-listen`（覆盖 `server.listen`）、`-log-level`（debug|info|warn|error）、`-log-json`。

> `config.yaml` 默认只保留 `server` / `proxy` / `management` 骨架，`payload` / `providers` / `api_keys` 全部注释，即开箱不含任何模型与密钥——请按需取消注释，或启动后通过管理 API 在线配置。

## 上游 URL 约定

| provider type          | `url` 填法                  | 实际请求路径              |
|------------------------|-----------------------------|---------------------------|
| `anthropic`            | base（不含 `/v1`）          | `{url}/v1/messages`       |
| `openai_completion`    | 含 `/v1` 的 base           | `{url}/chat/completions`  |
| `codex`                | 含 `/v1` 的 base           | `{url}/responses`         |

认证头：anthropic 用 `x-api-key`（+ 你配置的 headers，如 `anthropic-version`）；openai_completion / codex 用 `Authorization: Bearer`。

## 客户端调用示例

```bash
# Anthropic 风格（model 填 aliasB）
curl http://127.0.0.1:8317/v1/messages \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"claude","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'

# OpenAI Chat Completions 风格
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer $CLIENT_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-mini","messages":[{"role":"user","content":"hi"}]}'
```

支持 `Authorization: Bearer <key>`，也兼容 `x-api-key` 头。

## 超时与流式

- **非流式**：受 `server.request_timeout_seconds` 总超时约束。
- **流式**：默认**不受总超时约束**，只随客户端连接结束。`server.stream_idle_timeout_seconds` 默认 `0`（关闭，推荐）；设为 `>0` 时，仅当上游"连续 N 秒一个字节都没收到"才中止（每个 chunk 都会重置计时器，正常输出卡顿不会被中断）。

## 失败切换

`proxy.max_consecutive_failures` 控制同一候选模型的连续失败阈值；达到后，该 `aliasB` 的后续请求会跳过它、尝试下一个优先级候选。`proxy.failure_reset_seconds` 控制失败计数的自动清零（`0` = 永不清零）。失败判定包含网络错误、超时，以及 `proxy.upstream_retry_status_codes`（默认 408/429/500/502/503/504）。客户端错误（400/401/403 等）不计入上游失败。

## 配置校验

启动与管理 API 写入都会做完整校验，失败则启动退出 / 返回 `422`。硬错误包括：重复 provider `name`、同一 provider 下重复 `aliasA`、provider name 含 `/`；其余如引用不存在的模型、非法 raw JSON、payload 规则改写顶层 `model` 字段等也会被拒绝。详见 [`DEVELOPMENT.md`](DEVELOPMENT.md) §5。

## 测试

```bash
go test ./...          # 单元测试
```

端到端 smoke 脚本（针对真实上游）见 `scripts/`（按需自行配置上游 key 后运行）。

## 许可证

见 [`LICENSE`](LICENSE)。
