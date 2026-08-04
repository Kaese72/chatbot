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
    |        +--> MariaDB (conversations, dialog_entries, identity -- the lock + audit trail + the bot's own saved credential)
    |
    +--> Anthropic Messages API (streaming, tool use)
    |
    +--> authentication service (create identity at setup time; log in as it fresh before every tool call)
    |
    +--> device-store public API (Bearer use-token, as the bot's own self-provisioned user)
```

Every replica subscribes to the same `conversationEvents` fanout exchange. A "terminate" event only does something on the replica that happens to be running that conversation's `process()` goroutine; an "updated" event wakes any `.../follow/{id}` SSE connections open on any replica for that conversation, which then re-read DialogEntries from MariaDB (the database, not the event, is always the source of truth -- see `internal/events.Registry`).

## Project Layout

```
main.go                        # wiring: config, persistence, LLM client, device-store client, events, HTTP server
internal/config/                # viper-based config
internal/persistence/           # storage interface (locking + DialogEntry read/write contract + identity credential)
internal/persistence/mariadb/   # MariaDB implementation
internal/events/                # RabbitMQ pub/sub + in-process fan-out registries (termination, SSE updates)
internal/authclient/            # authentication service client (create user, login) -- used only by internal/identity
internal/identity/              # the chatbot's self-provisioned identity: setup, status, fresh-token-per-call
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

**Device-store is called via its public API with the bot's own identity**, not the unauthenticated internal API. The internal API (`device-store-internal`) only exposes single-device lookups and capability triggers -- no list endpoints -- and per the README's "the bot is authenticated as its own user, to the system, and makes actions via that user", using one consistent, authenticated identity for both discovery and triggering is the more faithful implementation, and gives device-store's own audit trail a real actor for every bot-initiated trigger.

**The identity itself is self-provisioned, not deploy-time config.** `POST /chatbot-service/v0/identities/setup` (`internal/restwebapp.WebApp.SetupIdentity`) forwards the caller's own already-validated bearer token to `internal/authclient.Client.CreateUser`, which calls the authentication service's `POST /users` with a freshly generated random username/password and the given display name; `internal/identity.Service.Setup` then saves that username/password via `persistence.Persistence.SaveIdentity` (table `identity`, migrations/V003.sql, a single row pinned to `id = 1`). `devicestore.NewClient` is constructed with `identity.Service.DeviceStoreToken` as its token provider (see `internal/devicestore.Client.tokenProvider`), so every single tool call logs in fresh via `internal/authclient.Client.Login` rather than reusing one credential fetched at startup. No identity saved (`persistence.ErrIdentityNotConfigured`), or a login that fails against the one saved, surfaces as an ordinary failed tool result (`internal/llm.Dispatcher.Dispatch`'s existing error-formatting path) -- the conversation itself is never blocked by identity setup being incomplete, only individual tool calls are. `GET /chatbot-service/v0/identities/status` reports only whether a row is saved locally, for UI onboarding; it does not verify the underlying authentication-service user still exists. Re-running setup always replaces the saved row wholesale (no "already configured" guard, unlike the authentication service's own bootstrap setup) -- this is also the only recovery path if that user was deleted directly in the authentication service. This mechanism was deliberately designed to require zero changes to the authentication service itself; see the README's **Identity** section for the full rationale and its documented TODO.

**Recovery from an interrupted turn** (termination, or a lock stolen after the 300s timeout) never rolls back to the previous user turn and never replays a truncated content block -- see the design discussion in this repo's conversation history for the full reasoning. In short: only fully-streamed content blocks are ever persisted (an in-flight, not-yet-`content_block_stop`'d block is simply dropped, no DB trace), and any tool call that got persisted (`*_CALL`) but never got its `*_RESPONSE` before the interruption is resolved with a synthetic `is_error`/`Success: false` result (`"Interrupted before completion. Please retry if needed."`) the next time the conversation is picked up, via `internal/conversation.findUnresolvedToolCalls` + `buildToolResponseEntry`. This satisfies the Messages API's hard requirement that every `tool_use` block have a matching `tool_result` before the conversation can be replayed, without ever re-executing a capability trigger that may have already taken effect.

**Locking column names**: `conversations.lock_id` is the replica identifier the README calls "currently processed by what replica"; `locked_at` is the timestamp used for the 300-second-timeout takeover. The acquire/re-acquire SQL matches the README's "Replica Conversation lock" section verbatim.

**Inbound auth is `huemie-lib/middleware.UseTokenMiddleware`, same as `device-store`.** Every `/chatbot-service/v0/...` route (conversations, api-keys, the SSE follow endpoint) requires a valid RS256 `use` token from the authentication service; only `/chatbot-service/openapi` and `/chatbot-service/docs` are exempt (mirrors `device-store`'s public router skip-list). The service verifies signatures only -- it holds the authentication service's RSA *public* key (`config.Loaded.Auth.RSAPublicKeyPath`), never a secret, and never calls back to the authentication service. There is deliberately no separate internal/unauthenticated router like `device-store-internal`: unlike device-store, nothing else in the cluster calls the chatbot's API on the bot's behalf, so every route goes through the one authenticated router.

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
| `ANTHROPIC_MODEL` | `claude-opus-5` | no |
| `AUTH_RSA_PUBLIC_KEY_PATH` | — | yes (authentication service's RS256 public key, PEM/PKIX; same convention as `device-store`'s `AUTH_RSA_PUBLIC_KEY_PATH`) |
| `AUTHENTICATION_URL` | `http://authentication:8080` | no (the authentication service's own REST API, used to create/log in as the bot's identity -- see `internal/identity`) |
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

**The device-store credential is likewise database-backed, not an environment variable** -- see
"The identity itself is self-provisioned" above and the README's **Identity** section. Unlike the
Anthropic API key, there is no equivalent fail-fast check at conversation-start time: identity
setup being incomplete only ever surfaces as an individual tool call failing, since (unlike the
LLM key) it's entirely possible for a conversation to complete usefully without ever calling a
tool.

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

* No automated tests yet.
* No lifecycle management for abandoned identities: re-running `POST /identities/setup` leaves
  the previous authentication-service user in place, un-deleted. See the README's **Identity**
  section TODO for the intended eventual replacement of this whole mechanism.
