# agent-harness-go

AI agent harness for **Human Initiative**: an HTTP service that lets a Gemini
model (via [Genkit](https://genkit.dev)) answer questions and call registered
tools to do so. Single-tenant and standalone; no database needed.

Built with a **hexagonal (ports & adapters)** architecture, so the model
provider, the tools, and the storage backend can each be swapped without
touching business logic. See [Architecture](#architecture).

## Scope

- ✅ One authenticated endpoint that runs a tool-calling conversation turn.
- ✅ Two example tools (`get_current_time`, `calculator`) showing the pattern
  for adding real ones.
- ✅ API-key auth, per-key rate limiting, request size limits, timeouts,
  graceful shutdown, structured and correlated logs, Swagger docs.
- ❌ No per-user authorization: every valid API key can use every tool.
- ❌ No durable storage: conversations live in process memory, are bounded,
  and are lost on restart.
- ❌ Single instance only (see [Scaling beyond one instance](#scaling-beyond-one-instance)).

## Quickstart

```bash
cp .env.example .env
# set GEMINI_API_KEY and API_KEYS (generate a key: openssl rand -hex 32)
set -a; source .env; set +a
make run
```

```bash
curl -s http://localhost:8080/api/v1/chat \
  -H 'Content-Type: application/json' \
  -H "X-API-Key: $API_KEYS" \
  -d '{"message": "What is (12 + 8) * 3, and what time is it in Asia/Jakarta?"}' | jq
```

```json
{
  "conversation_id": "3f9c9b1e-2f0a-4a3e-9b1a-6e4b6f0c1a2b",
  "reply": "(12 + 8) * 3 is 60. It is 20:31 in Asia/Jakarta.",
  "tool_calls": [
    { "name": "calculator", "arguments": {"expression": "(12 + 8) * 3"}, "result": "60", "duration_ms": 1 },
    { "name": "get_current_time", "arguments": {"timezone": "Asia/Jakarta"}, "result": "2026-09-25T20:31:04+07:00", "duration_ms": 0 }
  ]
}
```

Send `conversation_id` back on the next request to continue the conversation.
Turns of one conversation must be sent one at a time (a concurrent turn gets
`409 conversation_busy`).

## API

| Method | Path                  | Auth        | Purpose                                     |
|--------|-----------------------|-------------|---------------------------------------------|
| POST   | `/api/v1/chat`        | `X-API-Key` | Send a message, get a reply and tool calls  |
| GET    | `/healthz`            | none        | Liveness probe                              |
| GET    | `/readyz`             | none        | Readiness probe                             |
| GET    | `/swagger/index.html` | none        | Swagger UI (only when `SWAGGER_ENABLED=true`) |

Every response carries an `X-Request-Id` header (a well-formed incoming
`X-Request-Id` is reused). Every error has the same shape:

```json
{ "code": "rate_limited", "error": "rate limit exceeded, retry later", "request_id": "5b1f6c9e-..." }
```

Branch on `code`, never on `error`:

| HTTP | `code`              | Meaning                                                        |
|------|---------------------|----------------------------------------------------------------|
| 400  | `invalid_request`   | Malformed JSON, unknown field, empty/oversized message, bad `conversation_id` |
| 401  | `unauthorized`      | Missing or invalid `X-API-Key`                                 |
| 404  | `not_found`         | No such endpoint                                               |
| 405  | `method_not_allowed`| Wrong HTTP method                                              |
| 409  | `conversation_busy` | Another turn of this conversation is still running            |
| 413  | `request_too_large` | Body exceeds `MAX_REQUEST_BYTES`                               |
| 429  | `rate_limited`      | Per-key limit hit; honour `Retry-After`                        |
| 500  | `tool_turn_limit`   | The model kept calling tools past `MAX_TOOL_TURNS`             |
| 500  | `internal_error`    | Anything unexpected; details are in the server log under `request_id` |
| 503  | `model_unavailable` | Gemini overloaded or rate-limited; honour `Retry-After`        |
| 504  | `timeout`           | No reply within `REQUEST_TIMEOUT_SECONDS`                      |

A **failing tool does not fail the request**: the error is shown to the model
(which can retry or explain), and appears in that call's `error` field in
`tool_calls`.

Full schemas are in Swagger, generated from the annotations on the handlers.
After changing an annotation, run `make swagger` and commit `api/swagger/`
(`make swagger-check` fails in CI if it is stale).

## Architecture

```
cmd/harness/main.go                composition root: the only place that knows
                                    every concrete adapter
api/swagger/                       generated OpenAPI spec (make swagger)

internal/
  domain/conversation/             pure types: Message, Role

  ports/outbound/                  interfaces the application depends on
    model.go                         ModelPort + its error contract
    tool.go                          ToolHandler
    conversation.go                  ConversationStore
    errors.go                        ErrModelUnavailable, ErrToolTurnLimit

  application/agent/               the use case: Service.Chat — validates
                                    input, windows history, calls the model
                                    with every tool, persists the turn

  adapters/
    inbound/httpapi/                 HTTP: chi router, auth, rate limit,
                                      middleware, handlers, error mapping
    outbound/genkitmodel/            ModelPort via Genkit + Gemini: the ONLY
                                      package that imports Genkit
    outbound/memory/                 ConversationStore in bounded memory
                                      (LRU cap, per-conversation cap, TTL)
    outbound/tools/                  ToolHandlers: get_current_time, calculator

  platform/
    config/                          the only code that reads the environment;
                                      validates everything at startup
    logger/                          slog setup + request-scoped attributes
```

**Dependency rule:** `domain` and `application` import only `ports` and each
other, never Genkit, chi, or an adapter. That is what lets
`application/agent` be tested with in-memory fakes, and lets any adapter be
replaced without touching business logic.

**Log correlation:** the HTTP layer attaches `request_id` (and `api_key_id`, a
fingerprint of the key, never the key itself) to the request context. Every
`*Context` log call in any layer then includes them, so one `request_id`
retrieves every log line of a request: access log, service, and tool calls.
Message contents, tool arguments, and tool results are never logged.

### Adding a tool

1. Implement `outbound.ToolHandler` in `internal/adapters/outbound/tools/`
   (see `current_time.go` for the simplest case, `calculator.go` for input
   validation). Validate arguments: they come from the model and are
   untrusted.
2. Add it to `registeredTools` in `cmd/harness/main.go`.
3. Add tests next to it.

Tool names must be unique (checked at startup). A tool that returns an error,
or even panics, cannot fail the request or crash the process: the adapter
reports the failure to the model.

### Scaling beyond one instance

Three things are per-process and must move to shared infrastructure (e.g.
Redis/Postgres) before running more than one replica:

- the conversation store (`adapters/outbound/memory`),
- the per-conversation "turn in progress" guard (`application/agent/keyset.go`),
- the rate limiter (`adapters/inbound/httpapi/ratelimit.go`); with N replicas
  the effective limit is N times the configured one.

## Configuration

Everything is configured through environment variables; see
[.env.example](.env.example) for the full list with defaults. Startup fails
with a list of every invalid or missing setting.

## Development

**Read [CONTRIBUTING.md](CONTRIBUTING.md) before your first change.** It is
the single source for the workflow; in short:

- **Trunk-based development**: `main` is always green and deployable. Work
  in short-lived branches (merged within 1–2 days) and hide unfinished work
  behind a config flag rather than keeping a branch open.
- **Branch names**: `<type>/<short-kebab-description>`, e.g.
  `feat/web-search-tool`, `fix/42-timeout-on-long-replies`, where type is
  `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`, `build` or
  `revert`.
- **Commits**: [Conventional Commits](https://www.conventionalcommits.org/),
  e.g. `feat(tools): add web search tool`. Every commit must stand on its own,
  because it lands on `main` as-is.
- **Pull requests**: the description template is applied automatically. A PR
  needs all CI checks green, an approval, resolved conversations, and an
  up-to-date branch. It is merged with **Rebase and merge** only, so history
  stays linear.

All of this is enforced, not just documented: git hooks locally
(`make hooks`), the `conventions` job in CI, and repository settings applied
by [`scripts/github-setup.sh`](scripts/github-setup.sh).

```bash
make setup         # once per clone: git hooks + pinned swag CLI
make help          # list targets
make ci            # run locally what CI runs (lint, race tests, swagger check, build)
make test          # unit tests
make swagger       # regenerate api/swagger after changing an annotation
make build         # static binary in bin/
make docker-build  # production image
```

## Deployment

```bash
docker build -t agent-harness-go .
docker run --rm -p 8080:8080 --env-file .env agent-harness-go
```

- The image is distroless and runs as a non-root user; timezone data is
  embedded in the binary.
- On SIGTERM the server stops accepting connections and waits up to
  `SHUTDOWN_TIMEOUT_SECONDS` for in-flight requests. In Kubernetes, set
  `terminationGracePeriodSeconds` higher than that.
- Point liveness probes at `/healthz` and readiness probes at `/readyz`.
