# chatbot

LLM-backed chatbot service, hosted on the appliance, that converses with users and acts on the system (currently: device-store devices/groups) via tool use. See `README.md` for the full design — this file covers implementation conventions and the concrete decisions the README left open.

Current state: **API-only PoC**. No UI. Single deployment mode (one binary, no `api`/`rule-state`-style split — unlike `ittt-orchestrator`, there's no separate evaluation daemon; the REST API and the LLM processing loop run in the same process, coordinated across replicas purely through the database lock and RabbitMQ).

## Architecture

```
User / UI
    |
    | HTTP (REST + SSE)
    v
[ chatbot-service ] <----RabbitMQ (conversationEvents, fanout)----> [ chatbot-service replica N ]
    |        |
    |        +--> MariaDB (conversations, dialog_entries -- the lock + audit trail)
    |
    +--> Anthropic Messages API (streaming, tool use)
    |
    +--> device-store public API (Bearer JWT, as the bot's own user)
```

Every replica subscribes to the same `conversationEvents` fanout exchange. A "terminate" event only does something on the replica that happens to be running that conversation's `process()` goroutine; an "updated" event wakes any `.../follow/{id}` SSE connections open on any replica for that conversation, which then re-read DialogEntries from MariaDB (the database, not the event, is always the source of truth -- see `internal/events.Registry`).

## Project Layout

```
main.go                        # wiring: config, persistence, LLM client, device-store client, events, HTTP server
internal/config/                # viper-based config
internal/persistence/           # storage interface (locking + DialogEntry read/write contract)
internal/persistence/mariadb/   # MariaDB implementation
internal/events/                # RabbitMQ pub/sub + in-process fan-out registries (termination, SSE updates)
internal/devicestore/           # device-store public API client (list/trigger devices & groups)
internal/llm/                   # Anthropic Messages API client, tool schema, tool dispatch
internal/conversation/          # the README's "Architecture" section, literally: locking, the turn/tool loop, termination and recovery
internal/restwebapp/            # huma REST + SSE handlers
restmodels/                     # REST/JSON data models (Conversation, DialogEntry)
eventmodels/                    # RabbitMQ event schema (ConversationEvent)
migrations/                     # Flyway SQL migrations
```

## Key implementation decisions not fully specified by README.md

**API path prefix is `chatbot-service`, not `chatbot`.** The README's endpoint list uses `/chatbot-service/v0/...` even though the repo/module is `chatbot` -- kept as specified rather than "fixed", since it's an explicit, repeated choice in the design doc (mirrors `device-store` vs `device-store-internal` being different from the repo name too).

**Thinking is explicitly disabled on every LLM call.** The README's `DialogEntry` type enum (`USER_INPUT`, `USER_STOP`, `AGENT_MESSAGE`, `AGENT_ERROR`, the four tool-call/response types) has no slot for a thinking block. Rather than silently dropping thinking content or inventing an undocumented DialogEntry type, `internal/llm.Client.RunTurn` sends `thinking: {type: "disabled"}` on every request. If extended thinking is wanted later, that's a README change (new DialogEntry type) plus a code change together, not a silent capability the persisted transcript can't represent.

**Tool surface is four fixed tools**, not one dynamically generated per conversation from the live device list:
- `list_devices` / `list_groups` -- discovery; generic tool calls (`AGENT_GENERIC_TOOL_CALL`/`_RESPONSE`), output is the raw JSON device-store returned.
- `trigger_device_capability` / `trigger_group_capability` -- the two structured tools from the README (`AGENT_DEVICE_CAPABILITY_TRIGGER_CALL`/`_RESPONSE`, `AGENT_GROUP_CAPABILITY_TRIGGER_CALL`/`_RESPONSE`).

This keeps the tool schema fixed and cacheable regardless of how many devices exist; the model discovers devices/groups/capabilities by calling `list_devices`/`list_groups`, the same way a human would look them up, rather than the system prompt enumerating them. `internal/llm.ClassifyTool` is the single source of truth mapping a tool name to which DialogEntry pairing it gets -- `internal/conversation` never re-derives that mapping independently, so a `*_CALL` and its `*_RESPONSE` can never end up as mismatched types.

**Device-store is called via its public API with the bot's own JWT**, not the unauthenticated internal API. The internal API (`device-store-internal`) only exposes single-device lookups and capability triggers -- no list endpoints -- and per the README's "the bot is authenticated as its own user, to the system, and makes actions via that user", using one consistent, authenticated identity for both discovery and triggering is the more faithful implementation, and gives device-store's own audit trail a real actor for every bot-initiated trigger.

**Recovery from an interrupted turn** (termination, or a lock stolen after the 300s timeout) never rolls back to the previous user turn and never replays a truncated content block -- see the design discussion in this repo's conversation history for the full reasoning. In short: only fully-streamed content blocks are ever persisted (an in-flight, not-yet-`content_block_stop`'d block is simply dropped, no DB trace), and any tool call that got persisted (`*_CALL`) but never got its `*_RESPONSE` before the interruption is resolved with a synthetic `is_error`/`Success: false` result (`"Interrupted before completion. Please retry if needed."`) the next time the conversation is picked up, via `internal/conversation.findUnresolvedToolCalls` + `buildToolResponseEntry`. This satisfies the Messages API's hard requirement that every `tool_use` block have a matching `tool_result` before the conversation can be replayed, without ever re-executing a capability trigger that may have already taken effect.

**Locking column names**: `conversations.lock_id` is the replica identifier the README calls "currently processed by what replica"; `locked_at` is the timestamp used for the 300-second-timeout takeover. The acquire/re-acquire SQL matches the README's "Replica Conversation lock" section verbatim.

## Configuration (Environment Variables)

| Variable | Default | Required |
|---|---|---|
| `DATABASE_HOST` | — | yes |
| `DATABASE_PORT` | `3306` | no |
| `DATABASE_USER` | — | yes |
| `DATABASE_PASSWORD` | — | yes |
| `DATABASE_DATABASE` | `chatbot` | no |
| `EVENT_CONNECTIONSTRING` | — | yes |
| `DEVICE_STORE_URL` | `http://device-store:8080` | no |
| `DEVICE_STORE_JWT` | — | yes (stopgap -- see README's Configuration section; will move to per-user/dynamic credentials later) |
| `ANTHROPIC_MODEL` | `claude-opus-5` | no |
| `LOCK_TIMEOUT_SECONDS` | `300` | no (the README-specified value) |
| `LOCK_MAX_TOOL_LOOP_ITERATIONS` | `25` | no (not in the README; a bound on the step 6/7 tool-call loop so a misbehaving model can't hold a conversation's lock forever) |

Viper maps dots/hyphens to underscores, same convention as the rest of the monorepo.

**The Anthropic API key is database-backed config, not an environment variable.** It is stored
in the `api_keys` table (`internal/persistence.Persistence`'s `*APIKey*` methods /
`restmodels.APIKey`), managed via the `/chatbot-service/v0/api-keys` REST endpoints
(`internal/restwebapp.WebApp.{List,Create,Get,Update,Delete}APIKey`), and fetched fresh by
`internal/conversation.Service.New`/`Input` (via `persistence.Persistence.ActiveAPIKeyValue`)
for every request that will actually talk to the LLM -- `internal/llm.Client` itself holds no
credential, only the model ID; `RunTurn` takes the key as a parameter and builds a fresh
Anthropic SDK client with it on every turn. `api_keys.active` is constrained to at most one row
at a time by a generated-column unique index (`active_slot`, see `migrations/V002.sql`) in
addition to the application-level "deactivate the others first" logic in
`internal/persistence/mariadb`. `New`/`Input` return `persistence.ErrNoActiveAPIKey`, mapped to
HTTP/503, if no key is active -- this fails fast before a conversation is created/flipped to
`AGENT_IN_PROGRESS`, rather than leaving it stuck with no way to make progress.

## Development

```bash
go build ./...
go vet ./...
go run . 
```

API docs available at `/chatbot-service/docs` (Swagger UI) and `/chatbot-service/openapi` (raw spec) when running locally.

## Database Migrations

Managed by Flyway via `Dockerfile.migrater`, same as every other service in this monorepo. Migrations live in `migrations/` as `VNNN.sql`. Run the migrater container/Job before deploying a new API version.

## Documentation

* `README.md` is the design source of truth; keep it in sync with behavior changes, not just this file.

## Not yet implemented (explicitly out of scope for this PoC pass)

* No inbound authentication on the chatbot's own REST API -- the README's Configuration section only mentions a JWT the bot uses to call *other* services, not one it validates on its own endpoints. Add `huemie-lib/middleware.UseTokenMiddleware` here if/when that changes.
* No automated tests yet.
* No k8s manifests in `huemie-gitops-base` yet for this service (Deployment/Service/NetworkPolicy/migrater Job, plus a RabbitMQ NetworkPolicy ingress entry) -- needed before this can actually run in the cluster.
