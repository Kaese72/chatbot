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
    +--> authentication service's internal listener (own Kubernetes ServiceAccount token in,
    |    impersonation use-token for the conversation's owner out -- see internal/serviceauth)
    |
    +--> device-store public API (Bearer use-token, as the conversation's owner, via impersonation)
```

Every replica subscribes to the same `conversationEvents` fanout exchange. A "terminate" event only does something on the replica that happens to be running that conversation's `process()` goroutine; an "updated" event wakes any `.../follow/{id}` SSE connections open on any replica for that conversation, which then re-read DialogEntries from MariaDB (the database, not the event, is always the source of truth -- see `internal/events.Registry`).

## Project Layout

```
main.go                        # wiring: config, persistence, LLM client, device-store client, events, HTTP server
internal/config/                # viper-based config
internal/persistence/           # storage interface (locking + DialogEntry read/write contract)
internal/persistence/mariadb/   # MariaDB implementation
internal/events/                # RabbitMQ pub/sub + in-process fan-out registries (termination, SSE updates)
internal/authclient/            # authentication service's internal-listener client (impersonate) -- used only by internal/serviceauth
internal/serviceauth/           # this pod's own Kubernetes ServiceAccount token in, impersonation use-token for the conversation's owner out
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

**Device-store is called via its public API, impersonating the conversation's owner**, not the unauthenticated internal API. The internal API (`device-store-internal`) only exposes single-device lookups and capability triggers -- no list endpoints -- and using one consistent, authenticated identity (the real human user, not the bot) for both discovery and triggering is the more faithful implementation, and gives device-store's own audit trail the real actor for every trigger, not a blanket bot identity.

**Impersonation is a Kubernetes-service-to-service exchange, not a grantable user permission.** `internal/serviceauth.Provider.Token` (wired as `devicestore.NewClient`'s token provider, see `internal/devicestore.Client.tokenProvider`) is called fresh before every tool call: it reads the acting user ID that `internal/conversation.Service.New`/`Input` set on the background-processing context (`serviceauth.ContextWithActingUser`, read via `serviceauth.ActingUser`), re-reads this pod's own projected Kubernetes ServiceAccount token from disk (kubelet rotates its contents in place, so it's never cached), and presents it to the authentication service's *internal* listener via `internal/authclient.Client.Impersonate`. That listener (a separate port from authentication's public API) verifies the ServiceAccount token with the Kubernetes `TokenReview` API (`huemie-lib/k8sauth.RequireServiceAccount`, checking the caller names an allow-listed ServiceAccount in its own namespace) and, if it passes, mints and returns an ordinary short-lived `use` token for the acting user, carrying that user's own real permissions. No permission flag, grant, or endpoint for this exists on the human-facing side; authorization is entirely "are you the chatbot's own Kubernetes ServiceAccount". `serviceauth.Provider.Token` fails loudly (never silently proceeds) if no acting user is set on the context, since that would otherwise silently fall back to acting as nobody.

**Recovery from an interrupted turn** (termination, or a lock stolen after the 300s timeout) never rolls back to the previous user turn and never replays a truncated content block -- see the design discussion in this repo's conversation history for the full reasoning. In short: only fully-streamed content blocks are ever persisted (an in-flight, not-yet-`content_block_stop`'d block is simply dropped, no DB trace), and any tool call that got persisted (`*_CALL`) but never got its `*_RESPONSE` before the interruption is resolved with a synthetic `is_error`/`Success: false` result (`"Interrupted before completion. Please retry if needed."`) the next time the conversation is picked up, via `internal/conversation.findUnresolvedToolCalls` + `buildToolResponseEntry`. This satisfies the Messages API's hard requirement that every `tool_use` block have a matching `tool_result` before the conversation can be replayed, without ever re-executing a capability trigger that may have already taken effect.

**Locking column names**: `conversations.lock_id` is the replica identifier the README calls "currently processed by what replica"; `locked_at` is the timestamp used for the 300-second-timeout takeover. The acquire/re-acquire SQL matches the README's "Replica Conversation lock" section verbatim.

**Prompt caching is on, via three `cache_control` breakpoints per `RunTurn` call** (`internal/llm/client.go`): the system prompt, the last tool definition in `ToolDefinitions()` (covers the whole fixed tools array), and the last content block of the last message in history, added by `withHistoryCacheBreakpoint` without mutating the caller's slice. Anthropic's prompt caching isn't automatic -- nothing before an explicit breakpoint is cached -- and allows at most 4 breakpoints per request, hence marking only the *last* tool/message rather than every one. Since `internal/conversation.process`'s `messages` slice only ever grows, every later call within the same turn's tool loop, and every later turn of the same conversation, reads an increasingly large cached prefix instead of re-billing it at full price. The system prompt and tools breakpoints alone already collapse to a cache hit across every single request this service ever makes, since both are identical for every conversation.

**`AGENT_INITIATIVE_RELEASED` is the explicit "your turn now" marker**, added so a client can drive its UI purely off the DialogEntry stream instead of also polling `GET /conversations/{id}` after every entry. `internal/conversation.process` appends it (via the `initiativeReleasedEntry` helper) at the single fall-through point every non-termination exit from `mainLoop` converges on, immediately before `ReleaseLock` -- end_turn, an already-appended `AGENT_ERROR`, and the iteration-limit bailout all reach it. Termination does not: `handleTermination` already appends `USER_STOP` for that case, so exactly one of the two entry types always marks the end of a turn, never both. It carries no payload (`internal/persistence/mariadb.attachPayload`'s case for it is a no-op, same as `USER_STOP`'s). `migrations/V004.sql` backfills one of these onto every pre-existing conversation currently sitting with `initiative = 'USER'`, so old conversations aren't missing the marker a client now depends on; conversations that were `AGENT_IN_PROGRESS` at migration time are deliberately left alone since the in-flight turn will append the real one itself when it actually finishes.

**Inbound auth is `authentication/usertoken.Middleware`** (see the privacy note below; `huemie-lib` no longer has a `middleware` package -- token verification lives with the service that issues the token). Every `/chatbot-service/v0/...` route (conversations, api-keys, the SSE follow endpoint) requires a valid RS256 `use` token from the authentication service; only `/chatbot-service/openapi` and `/chatbot-service/docs` are exempt (mirrors `device-store`'s public router skip-list). The service verifies signatures only -- it holds the authentication service's RSA *public* key (`config.Loaded.Auth.RSAPublicKeyPath`), never a secret, and never calls back to the authentication service. There is deliberately no separate internal/unauthenticated router like `device-store-internal`: unlike device-store, nothing else in the cluster calls the chatbot's API on the bot's behalf, so every route goes through the one authenticated router.

**Conversations are private per user, scoped by `conversations.owner_id`** (`migrations/V005.sql`). The authentication service owns the `use` token format and exports it as the public package `github.com/Kaese72/authentication/usertoken` (same module, no separate `go.mod`; needs authentication v0.0.4+). `usertoken.Middleware` verifies the token and puts its `id` claim -- the user ID -- in the request context; handlers read it with `usertoken.UserID(ctx)`, wrapped by `restwebapp.callerID` which turns "absent" into a 401. The middleware rejects any token without a valid `id`, so behind it the ID is always present (only the skipped docs/openapi routes lack one). The public key is loaded with `usertoken.LoadPublicKeyFromFile`; from `huemie-lib` only its logging is used. Rules to keep:
- Every user-facing `persistence.Persistence` method takes an `ownerID` and puts `AND owner_id = ?` in its SQL. A conversation owned by someone else is reported as `ErrConversationNotFound` (404), never 403, so existence isn't leaked. When adding a new conversation endpoint, thread `ownerID` through -- don't fetch by ID alone.
- `persistence.ListDialogEntries` is deliberately *not* owner-scoped, because `conversation.process` (background, no calling user) reads history through it. Any user-facing caller must establish ownership first: `conversation.Service.ListDialogEntries` does so via `GetConversation`.
- `Terminate` goes through `GetConversation(ownerID, ...)` before publishing the RabbitMQ event; without that any user could stop anyone's turn.
- The SSE follow route can't return an error from its handler (the 200 stream is already open), so ownership is checked up front by `WebApp.RequireConversationOwner`, a huma operation middleware, giving a proper 404.
- `owner_id` is still nullable in the schema, but the pre-existing ownerless conversations were deleted by `migrations/V006.sql`, so every row has an owner. It is not exposed in `restmodels.Conversation`.
- API keys are intentionally still service-wide, not per-user. The chatbot has no identity of its
  own to scope per-user in the first place -- see **Identity** in the README.

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
| `ANTHROPIC_MODEL` | `claude-haiku-4-5-20251001` | no |
| `AUTH_RSA_PUBLIC_KEY_PATH` | — | yes (authentication service's RS256 public key, PEM/PKIX; same convention as `device-store`'s `AUTH_RSA_PUBLIC_KEY_PATH`) |
| `AUTHENTICATION_INTERNAL_URL` | `http://authentication:8081` | no (authentication's internal listener, used to exchange this pod's own Kubernetes ServiceAccount token for an impersonation token -- see `internal/serviceauth`) |
| `SERVICE_TOKEN_PATH` | `/var/run/secrets/tokens/authentication-internal` | no (path to this pod's own projected, audience-bound Kubernetes ServiceAccount token) |
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

**There is no device-store credential to configure at all.** Every tool call impersonates the
conversation's owner fresh, via the Kubernetes-service-to-service exchange described in
**Impersonation** above and the README's **Identity** section -- there is nothing analogous to
the Anthropic API key's "no active key configured" failure mode, since the chatbot never holds
or manages a device-store credential of its own.

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

* Almost no automated tests: only `internal/restwebapp/privacy_test.go`, which exercises the per-user privacy rules over HTTP against a stub `Persistence` (no database, so the `owner_id` SQL itself is not covered).
