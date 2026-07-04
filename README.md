# SimpleAPI

A lightweight AI protocol proxy written in Go. Configuration comes only from
`config.yaml` (no database). It exposes three client-compatible interfaces and
fans requests out to three upstream provider types, with per-API-key
authorization, aliasing, failover, protocol translation, payload rewriting, and
Anthropic `web_search` forwarding.

See [`DEVELOPMENT.md`](DEVELOPMENT.md) for the full specification and
[`web_api.openapi.yaml`](web_api.openapi.yaml) for the API contract.

## Protocols

- Client interfaces: `/v1/messages` (anthropic), `/v1/chat/completions`
  (openai_completion), `/v1/responses` (codex), plus `/v1/models`.
- Upstream provider types: `anthropic`, `openai_completion`, `codex`.
- Same-protocol requests pass through; cross-protocol requests are translated.

## Model identity

- A provider model is identified internally by `providerName/aliasA` (split on
  the first `/`, so provider names must not contain `/`).
- Clients call with `aliasB`, which each API key maps to one or more
  `providerName/aliasA` candidates (sorted by priority). The response `model`
  field is rewritten back to `aliasB`. Statistics use `aliasA`/internal id, never
  `aliasB`.

## Build & run

```bash
go build -o ./bin/proxy ./cmd/proxy
PROXY_ADMIN_KEY=... CLIENT_API_KEY_DEV=... DEEPSEEK_API_KEY=... \
  ./bin/proxy -config config.yaml
```

Flags: `-config` (default `config.yaml`), `-listen` (overrides `server.listen`),
`-log-level` (debug|info|warn|error), `-log-json`.

Secrets use `${VAR}` / `${VAR:-default}` placeholders expanded at startup.

## Test

```bash
go test ./...                      # unit tests
bash scripts/smoke.sh              # end-to-end against DeepSeek (config.test.yaml)
```

The smoke test builds the binary and exercises passthrough, streaming,
cross-protocol translation, failover, management, and usage against the
DeepSeek upstream described in `deepseek.txt`.

## Management API

Under `management.base_path` (default `/-/api`), behind `X-Admin-Key`:
`config` GET/PUT, `validate`, `payload` GET/PUT, `reload`, `providers` CRUD,
`api-keys` CRUD, `models`, `usage`. `GET /-/health` is unauthenticated.
Validation failures return `422` with `{valid, errors:[{path,code,message}]}`;
on rejection the on-disk config is left unchanged.
