# go-prod-change-registry

A lightweight, append-only change registry for production environments. It records deployments, feature-flag flips, infrastructure mutations, and other production changes as immutable events in PostgreSQL, then exposes them through a RESTful API and an HTML dashboard. Derived annotations identify events that are currently starred or have an active alert. Teams can use it to correlate production changes with incidents and understand what changed, when, and by whom.

## Quickstart

```bash
PCR_API_TOKENS=my-secret-token \
PCR_OAUTH_CLIENT_ID=your-github-oauth-client-id \
PCR_OAUTH_CLIENT_SECRET=your-github-oauth-client-secret \
docker compose up -d --build
```

Register `http://localhost:8080/auth/callback` on the selected provider first. The server starts on `:8080`; navigate to `http://localhost:8080/login` and sign in with the configured provider. Compose defaults to GitHub and unrestricted authenticated users for local development only.

## Configuration

All configuration is via environment variables prefixed with `PCR_`.

| Variable | Required | Default | Description |
|---|---|---|---|
| `PCR_API_TOKENS` | Yes | -- | Comma-separated list of valid API tokens |
| `PCR_HUMAN_AUTH_PROVIDER` | Yes | -- | Exactly one dashboard identity provider: `github`, `google`, `authentik`, or trusted proxy `beyond` |
| `PCR_PUBLIC_URL` | Yes | -- | Canonical external origin used to build `/auth/callback`; HTTPS required except on loopback |
| `PCR_OAUTH_CLIENT_ID` | Except Beyond | -- | OAuth client ID registered with the selected provider |
| `PCR_OAUTH_CLIENT_SECRET` | Except Beyond | -- | OAuth client secret registered with the selected provider |
| `PCR_OIDC_ISSUER_URL` | Authentik only | -- | Recommended per-provider issuer, ending in `/application/o/<slug>/` |
| `PCR_ALLOWED_ORGS` | Conditional | -- | GitHub organizations, Google Workspace domains, or exact Authentik/Beyond groups; comma-separated OR semantics |
| `PCR_HUMAN_AUTH_ALLOWED_SUBJECTS` | Conditional | -- | Individual exceptions such as `github:12345`, `google:<sub>`, `authentik:<issuer>:<sub>`, or `beyond:<lowercase-email>` |
| `PCR_HUMAN_AUTH_ALLOW_ANY` | No | `false` | Explicitly allow any identity verified by the selected provider; mutually exclusive with restrictions |
| `PCR_HUMAN_SESSION_DURATION` | No | `12h` | Absolute, non-sliding human session and provider-policy freshness window; maximum 7 days |
| `PCR_ADDR` | No | `:8080` | Listen address (`host:port`) |
| `PCR_DATABASE_URL` | Yes | -- | PostgreSQL connection URL; require TLS outside local development |
| `PCR_REQUIRE_AUTH_READS` | No | `true` | Require auth for read endpoints (GET) |
| `PCR_AUTO_MIGRATE` | No | `true` | Run database migrations on startup |
| `PCR_DASHBOARD_REFRESH_SEC` | No | `60` | Dashboard auto-refresh interval in seconds |
| `PCR_READ_TIMEOUT` | No | `5s` | HTTP server read timeout (Go duration) |
| `PCR_WRITE_TIMEOUT` | No | `10s` | HTTP server write timeout (Go duration) |
| `PCR_SHUTDOWN_TIMEOUT` | No | `15s` | Graceful shutdown timeout (Go duration) |
| `PCR_DB_CONNECT_TIMEOUT` | No | `5s` | PostgreSQL startup connection timeout |
| `PCR_DB_MAX_CONNECTIONS` | No | `10` | Maximum PostgreSQL connections per server process |
| `PCR_DB_SLOW_QUERY_THRESHOLD` | No | `100ms` | Log a warning when a query exceeds this |
| `PCR_SESSION_SECRET` | No | (random) | HMAC key for dashboard session cookies. **Must be at least 32 bytes if set.** When unset, an ephemeral 32-byte secret is generated; sessions then expire on every restart. See [Production session secret](#production-session-secret) below |
| `PCR_COOKIE_SECURE` | No | `true` | Set the `Secure` flag on session cookies (requires HTTPS). Set to `false` for local dev without TLS |

## API Reference

Set up a convenience alias:

```bash
export PCR_TOKEN="your-token"
alias pcr='curl -s -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

### Endpoints

The API is append-only. There are no PUT, PATCH, or DELETE endpoints. Events are immutable once created.

| Method | Path | Description |
|---|---|---|
| `GET` | `/livez` | Process liveness (no auth or dependency checks) |
| `GET` | `/readyz` | Traffic readiness (no auth, verifies PostgreSQL connectivity) |
| `GET` | `/api/v1/health` | Backwards-compatible alias for `/readyz` |
| `POST` | `/api/v1/events` | Create a change event or meta-event |
| `GET` | `/api/v1/events` | List events (with filters) |
| `GET` | `/api/v1/current` | List active logical operations derived from phase events |
| `GET` | `/api/v1/events/{id}` | Get a single event |
| `GET` | `/api/v1/events/{id}/annotations` | Get derived annotation state (starred, alerted) |
| `GET` | `/api/v1/events/{id}/activity` | List annotations and lifecycle closure activity |
| `POST` | `/api/v1/events/{id}/links` | Append one or more external-link annotations |
| `POST` | `/api/v1/events/{id}/star` | Toggle star (creates a star or unstar meta-event) |
| `POST` | `/api/v1/events/{id}/alert` | Toggle alert (creates an alert or clear-alert meta-event) |
| `POST` | `/api/v1/events/{id}/close` | Close a current operation by appending a correlated end event |

### Dashboard routes

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Dashboard (requires a human session) |
| `GET` | `/events/{id}` | Event detail page |
| `POST` | `/events/{id}/star` | Toggle star (redirects back, requires CSRF token) |
| `POST` | `/events/{id}/alert` | Toggle alert state (redirects back, requires CSRF token) |
| `POST` | `/events/{id}/links` | Append external links (redirects back, requires CSRF token) |
| `POST` | `/events/{id}/close` | Close an open operation (redirects back, requires CSRF token) |
| `GET` | `/login` | Show login form |
| `GET` | `/auth/start` | Begin OAuth with the configured provider |
| `GET` | `/auth/callback` | Validate provider identity and establish a human session |
| `POST` | `/logout` | Clear the human session (requires CSRF token) |

### Query parameters for `GET /api/v1/events`

| Parameter | Type | Description |
|---|---|---|
| `start_after` | RFC 3339 timestamp | Events with `timestamp >=` this time |
| `start_before` | RFC 3339 timestamp | Events with `timestamp <` this time |
| `around` | RFC 3339 timestamp | Center of a time window; takes precedence over start bounds |
| `window` | Go duration (e.g. `30m`) | Half-width around `around`; defaults to `30m` when `around` is set |
| `user` | string | Filter by user name |
| `type` | string | Filter by event type (`deployment`, `feature-flag`, `k8s-change`, ...) |
| `tag` | string | Filter by tag (`key:value`); repeat with different keys to require multiple tags |
| `top_level` | bool | If `true`, exclude meta-events (only events without a `parent_id`) |
| `alerted` | bool | If exactly `true`, return events whose current derived alert state is active |
| `limit` | int | Max results, 1-200 (default 50) |
| `offset` | int | Pagination offset |

### Query parameters for `GET /api/v1/current`

Current results have no time bound. Filters apply after start/end events have been reduced into logical active operations.

| Parameter | Type | Description |
|---|---|---|
| `for_team` | string | Include this team's work, unattributed work, and all `scope=site` work |
| `scope` | string | Scope filter; repeat for OR semantics |
| `severity` | string | Case-insensitive severity filter; repeat for OR semantics |
| `type` | string | Exact event-type filter |
| `limit` | int | Max results, 1-200 (default 50) |
| `offset` | int | Pagination offset over reduced logical operations |

### Examples

**Health checks:**

Health endpoints do not require authentication. Liveness deliberately avoids PostgreSQL so a database outage does not cause a container restart loop; readiness verifies that the service can reach PostgreSQL before it receives traffic.

```bash
# Process is serving HTTP; always independent of PostgreSQL.
curl -s http://localhost:8080/livez
# Returns 200: {"status":"ok"}

# Service is ready to receive traffic.
curl -s http://localhost:8080/readyz
# Returns 200: {"status":"ok"}
# When PostgreSQL is unreachable:
# Returns 503: {"status":"unhealthy","reason":"database unreachable"}
```

**Create an event:**

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy payments-service v2.4.1",
  "long_description": "Rolling update across 3 regions",
	"links": [
	  {"label": "Pull request", "url": "https://github.com/example/payments/pull/241"},
	  {"label": "Runbook", "url": "https://notion.so/example/payments-rollout"}
	],
  "tags": {"service": "payments", "region": "us-east-1"}
}'
```

**List events with filters:**

```bash
pcr "http://localhost:8080/api/v1/events?type=deployment&start_after=2026-03-30T00:00:00Z&limit=10"
```

**Window query (incident correlation):**

```bash
pcr "http://localhost:8080/api/v1/events?around=2026-03-31T14:32:00Z&window=30m"
```

This returns events from 30 minutes before the given timestamp (inclusive) to 30 minutes after it (exclusive) -- useful for answering "what changed around the time of an incident?"

**List top-level events only (exclude meta-events):**

```bash
pcr "http://localhost:8080/api/v1/events?top_level=true"
```

**List current work visible to a team:**

```bash
pcr "http://localhost:8080/api/v1/current?for_team=payments"
pcr "http://localhost:8080/api/v1/current?for_team=payments&severity=sev0&severity=sev1"
pcr "http://localhost:8080/api/v1/current?scope=site"
```

**Get a single event:**

```bash
pcr http://localhost:8080/api/v1/events/abc123
```

**Get annotations for an event (derived star/alert state):**

```bash
pcr http://localhost:8080/api/v1/events/abc123/annotations
```

Returns:

```json
{"starred": true, "alerted": false}
```

**Toggle star:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/star
```

This creates a `star` or `unstar` meta-event depending on the current state.

**Append links to an existing event:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/links -d '{
  "user_name": "alice",
  "links": [
    {"label": "PagerDuty incident", "url": "https://example.pagerduty.com/incidents/P123"},
    {"label": "Remediation PR", "url": "https://github.com/example/service/pull/42"}
  ]
}'
```

This creates one immutable `link` annotation. The detail view aggregates original links and later link annotations. Labels are limited to 256 bytes; URLs are limited to 2048 bytes and must be absolute HTTP(S) URLs without credentials or control characters. The server renders links but never fetches them.

**List event activity:**

```bash
pcr http://localhost:8080/api/v1/events/abc123/activity
```

**Toggle alert:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/alert
```

**Close an active operation:**

```bash
pcr -X POST http://localhost:8080/api/v1/events/abc123/close -d '{
  "user_name": "release-manager",
  "description": "Rollout completed and verified"
}'
```

The server derives the event type and correlation identifier from the start event and appends an idempotent `phase=end` event.

**List every currently alerted event:**

```bash
pcr "http://localhost:8080/api/v1/events?alerted=true&top_level=true"
```

This API query has no implicit time bound. The dashboard's Alerts view does have the dashboard's default 24-hour event-time range unless a different range is selected.

## Meta-Events

Star and alert state are not stored as mutable fields on an event. Instead, each transition is a new, immutable meta-event that references the original event via `parent_id`.

### How it works

To star an event, a new event is created:

```json
{
  "parent_id": "original-event-id",
  "event_type": "star",
  "user_name": "sarah",
  "description": "starred"
}
```

To unstar, another meta-event is created:

```json
{
  "parent_id": "original-event-id",
  "event_type": "unstar",
  "user_name": "sarah",
  "description": "unstarred"
}
```

The current state is derived independently for each transition pair. Meta-events are considered in reverse database insertion order: the newest `star` or `unstar` determines `starred`, and the newest `alert` or `clear-alert` determines `alerted`. An event with no transition in a pair has the corresponding state set to `false`. Caller-supplied timestamps do not control reduction order.

The `GET /api/v1/events/{id}/annotations` endpoint returns this computed state. `GET /api/v1/events?alerted=true` uses the same latest-transition rule to return events whose alert state is currently active.

### Meta-event types

| Type | Effect |
|---|---|
| `star` | Marks the parent event as starred |
| `unstar` | Removes the star from the parent event |
| `alert` | Opens/activates the parent event's alert state |
| `clear-alert` | Closes/clears the parent event's alert state |
| `link` | Appends one or more external references to the parent event |

For an incident-oriented event, `alert` can represent an active incident and `clear-alert` its resolution. This annotation state is independent of the phase-based logical-operation state described below. Dedicated star, alert, and link endpoints append the corresponding immutable meta-events; the generic create-event endpoint remains available to automation.

### Open and resolve an alert

After creating a top-level event, open its alert state:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "parent_id": "original-event-id",
  "event_type": "alert",
  "user_name": "on-call",
  "description": "SEV0 response started"
}'
```

Resolve it by appending the opposite transition:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "parent_id": "original-event-id",
  "event_type": "clear-alert",
  "user_name": "on-call",
  "description": "Incident resolved"
}'
```

### Current operations via linked phase events

A deployment lifecycle (or any multi-phase operation) is recorded as separate immutable top-level events sharing an identifier tag rather than as a single mutable event. Use `change_id` for new producers; `deploy_id` remains supported for existing deployment producers:

```bash
# Deploy started
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "event_type": "deployment",
  "user_name": "alice",
  "description": "deploy v1.2 started",
  "tags": {"change_id": "deploy-abc123", "phase": "start", "team": "payments", "scope": "service", "severity": "sev2", "env": "prod"}
}'

# Deploy completed (separate event, same change_id tag)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "event_type": "deployment",
  "user_name": "alice",
  "description": "deploy v1.2 completed",
  "tags": {"change_id": "deploy-abc123", "phase": "end"}
}'
```

Query by tag to see the full lifecycle:

```bash
pcr "http://localhost:8080/api/v1/events?tag=change_id:deploy-abc123"
```

History retains both rows. Current includes the start until a top-level end event has the same event type and correlation identifier. Exact lowercase `phase=start` and `phase=end` values participate. If both identifier tags exist, non-empty `change_id` takes precedence; an empty `change_id` falls back to non-empty `deploy_id`. Events without a usable identifier remain in History but cannot enter Current.

Each logical identifier represents one start/end cycle. Duplicate starts collapse to the earliest `timestamp`, then `id`; any matching end closes the logical operation. Give a restarted operation a new logical identifier. Retries should instead reuse a stable, phase-specific `external_id`.

Display and visibility tags come from the representative start event:

- `team` identifies the owning team. Missing or empty values are shown as unattributed and are visible to every team.
- `scope=site` marks site-wide work, which is visible to every team.
- `severity` conventionally uses lowercase `sev0` through `sev3`; reads are case-insensitive for compatibility with existing `SEV0` producers.

A tag key may appear at most once on an event. The database enforces this invariant. End events need only `phase`, the matching correlation identifier, and the same event type.

The close endpoint and dashboard action construct that end event automatically. They accept a correlated start while its logical operation remains open and use a deterministic `external_id`, so concurrent or retried closes do not create duplicate closure events.

## Idempotency

The API supports an optional `external_id` field on events, which acts as an idempotency key. This allows CI/CD pipelines and automation to safely retry requests without creating duplicate events.

### How it works

- `external_id` is an optional string field on the create-event request.
- A partial unique index enforces that no two events share the same non-null `external_id`.
- On the first POST with a given `external_id`, the server creates the event and returns **201 Created**.
- On a subsequent POST with the same `external_id`, the server returns the existing event with **200 OK** instead of creating a duplicate.
- If `external_id` is omitted (or null), no uniqueness check is performed and the event is always created.

### Generating an external_id

Callers should construct `external_id` from a combination of the source system and a unique operation identifier. The value must be globally unique across all events in the registry.

| Source | Pattern | Example |
|---|---|---|
| GitHub Actions | `github-actions-{run_id}-{job}` | `github-actions-12345-deploy` |
| GitLab CI | `gitlab-{pipeline_id}-{job_id}` | `gitlab-8901-deploy-prod` |
| ArgoCD | `argocd-{app}-{revision}` | `argocd-api-abc123f` |
| Terraform | `terraform-{workspace}-{run_id}` | `terraform-prod-run-567` |
| LLM agent | `agent-{session}-{action}` | `agent-sess-a1b2-deploy` |

### Example: create with external_id and retry

```bash
# First request -- creates the event (201 Created)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "github-actions-12345-deploy",
  "user_name": "ci-bot",
  "event_type": "deployment",
  "description": "Deploy api v3.1.0",
  "tags": {"service": "api", "env": "prod"}
}'

# Retry (network blip, webhook redelivery, etc.) -- returns the same event (200 OK)
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "github-actions-12345-deploy",
  "user_name": "ci-bot",
  "event_type": "deployment",
  "description": "Deploy api v3.1.0",
  "tags": {"service": "api", "env": "prod"}
}'
```

Both requests return the same event (same `id`, same `created_at`). The second request is a no-op.

## Dashboard

The built-in HTML dashboard is served at `/`. Authenticate through the single GitHub, Google, Authentik, or Beyond provider selected by the installer. OAuth providers establish the locally validated session through their callback; Beyond mode establishes it from trusted proxy headers. It provides:

- A Current view for active team, unattributed, and site-wide work with no time bound
- A Site-wide view for active `scope=site` work
- An independent banner for visible active `sev0` and `sev1` work
- Time range buttons to filter events by predefined windows (last 5 minutes, 30 minutes, 1 hour, and 24 hours)
- Clickable tags that filter the event list to matching events
- A star toggle on each event (creates star/unstar meta-events behind the scenes)
- Alert/clear-alert controls plus filtering and highlighting for active alerts
- A repeatable form for appending validated external links
- A close action for active correlated operations
- Event detail showing lifecycle state, aggregated links, and chronological activity
- Auto-refresh at a configurable interval (see `PCR_DASHBOARD_REFRESH_SEC`)

The landing page remains the 24-hour History view. Current and Site-wide are explicit, unbounded views. Alerts combines current alert annotation state with the selected History time range; use the API query shown above when an unbounded list of alerted events is required.

### Seeded interface preview

The shared functional fixture contains active, completed, duplicate-delivery, site-wide, unattributed, high-severity, starred, and alerted records. Its timestamps are relative to seed time, so the 24-hour History view remains useful whenever it is run.

Start a disposable server in one shell:

```bash
PCR_API_TOKENS=demo-token \
PCR_SESSION_SECRET=demo-session-secret-with-padding-123 \
PCR_COOKIE_SECURE=false \
PCR_REQUIRE_AUTH_READS=false \
PCR_HUMAN_AUTH_PROVIDER=github \
PCR_PUBLIC_URL=http://127.0.0.1:18082 \
PCR_OAUTH_CLIENT_ID=replace-with-local-github-client-id \
PCR_OAUTH_CLIENT_SECRET=replace-with-local-github-client-secret \
PCR_HUMAN_AUTH_ALLOW_ANY=true \
PCR_DATABASE_URL='postgres://pcr@127.0.0.1/pcr?sslmode=disable' \
PCR_ADDR=:18082 \
go run ./cmd/server
```

Seed it from another shell, then open `http://127.0.0.1:18082/?view=current&team=payments`:

```bash
make seed-demo
```

The fixture is [testdata/functional/phosphor-demo.json](testdata/functional/phosphor-demo.json). The three interface fonts are vendored under `web/static/fonts/` and the rendered pages do not contact a remote font host.

## Data Model

Events are immutable. There are no update or delete operations. The core `ChangeEvent` struct:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier (generated) |
| `external_id` | string (optional) | Caller-supplied idempotency key (unique when non-null) |
| `parent_id` | string (optional) | References another event's ID, making this a meta-event |
| `user_name` | string | Who made the change |
| `timestamp` | RFC 3339 | When the change happened |
| `event_type` | string | Category: `deployment`, `feature-flag`, `k8s-change`, or custom. Meta-events use `star`, `unstar`, `alert`, `clear-alert`, `link` |
| `description` | string | Short summary |
| `long_description` | string | Detailed description |
| `links` | array of `{label, url}` | Ordered external references. Labels are optional; URLs must be absolute HTTP(S) URLs |
| `tags` | map[string]string | Arbitrary key-value metadata for filtering and lifecycle linking; each key is unique within an event |
| `created_at` | RFC 3339 | Record creation time |

There are no mutable `timestamp_start`, `timestamp_end`, `starred`, `alerted`, or `updated_at` fields. Point-in-time lifecycle records use the single `timestamp` field and shared tags. Current logical-operation state is derived from top-level phase events; star and alert state are derived independently from meta-events. Events do not change after creation.

## Architecture

```
cmd/
  server/          HTTP server
  seed/            Fixture loader for a running server
  smoke/           End-to-end HTTP smoke client
internal/
  config/          Environment-based configuration
  fixture/         Shared JSON fixture loading
  model/           Domain types (ChangeEvent, ListParams, request/response structs)
  postgres/        PostgreSQL pool and migration lifecycle
  store/           PostgreSQL data access layer (ChangeStore interface)
  service/         Business logic
  handler/         HTTP handlers (API + dashboard)
  middleware/      Auth, request ID, logging
  router/          Route definitions (chi)
migrations/        SQL migration files
web/               Embedded static assets and HTML templates
testdata/           Functional fixtures
k8s/                Example Kubernetes manifests
docs/              Deployment and testing guides
```

## Deployment

Four deployment options, from simplest to most production-like:

| Method | Command | Best for |
|---|---|---|
| **Binary** | `make build` plus database, API-token, and OAuth environment | Local development |
| **Docker** | `make docker-run` | Containerized local testing |
| **Docker Compose** | Configure API and OAuth credentials, then `docker compose up -d --build` | Local dev with persistence |
| **Kubernetes (kind)** | `kind create cluster`, then follow the provider-specific manifest steps | Testing k8s deployment |

See [docs/deployment.md](docs/deployment.md) for full container and Kubernetes instructions. For direct binary setup, see the manual setup in [docs/testing.md](docs/testing.md).

### Production session secret

`PCR_SESSION_SECRET` is the HMAC key the server uses to sign dashboard session cookies and CSRF tokens. The server enforces a **32-byte minimum** and rejects shorter values at startup.

The shipped Docker artifacts ship with a clearly-named placeholder so the stack starts without env tweaks for local dev:

- `docker-compose.yml` and the `make docker-run` target both default `PCR_SESSION_SECRET` to `dev-default-please-override-in-production` (41 bytes).
- That string is **not safe for production** -- it's checked into source control. Anyone who reads the repo can forge session cookies against any deployment that accepts the default.

For any deployment beyond a developer laptop, override it. Two pieces:

1. **Generate a strong, random secret** (run once, store it where you store other secrets):

   ```bash
   openssl rand -base64 48      # >= 32 bytes after base64
   # or
   head -c 32 /dev/urandom | base64
   ```

2. **Inject it via the appropriate channel for your runtime:**

   - **Docker Compose** -- put it in a `.env` file next to `docker-compose.yml`; Compose loads it automatically:

     ```dotenv
     PCR_API_TOKENS=<your-tokens>
     PCR_SESSION_SECRET=<paste output of step 1>
     ```

     Or pass it inline:

     ```bash
     PCR_API_TOKENS=... PCR_SESSION_SECRET=... docker compose up -d
     ```

   - **`docker run`** -- load the value into the environment from your secret-management workflow, then pass its name with `-e` so the value is not repeated in the `docker run` command:

     ```bash
     export PCR_SESSION_SECRET=<paste output of step 1>
     docker run --rm -p 8080:8080 \
       -e PCR_API_TOKENS=... \
       -e PCR_SESSION_SECRET \
       -e PCR_DATABASE_URL pcr-server
     ```

   - **Kubernetes** -- store credentials in a `Secret` and reference them from the Deployment. The shipped `k8s/secret.yaml` template has placeholders for `api-tokens`, `session-secret`, and `database-url`; replace all three before applying it. See [docs/deployment.md](docs/deployment.md) for the full Kubernetes walkthrough.

   - **Other orchestrators / hosted environments** -- inject from your platform's secret manager (AWS SSM Parameter Store, GCP Secret Manager, HashiCorp Vault, GitHub Actions secrets, ...) into the container's environment.

Rotating the secret invalidates every existing session and CSRF token; users will need to re-login. Plan rotations during low-traffic windows or coordinate with users.

## Development

### Make targets

| Target | Description |
|---|---|
| `make build` | Compile to `bin/pcr-server` |
| `make test` | Run default (non-integration) tests with race detection and package coverage |
| `make test-short` | Run default tests with Go's short-test flag |
| `make coverage` | Write `coverage.out` and `coverage.html`, then open the report |
| `make lint` | Run `golangci-lint` |
| `make fmt` | Format with `gofmt` and `goimports` |
| `make run` | `go run ./cmd/server` |
| `make vet` | Run `go vet` |
| `make audit` | Run `go vet` + `govulncheck` |
| `make clean` | Remove build artifacts |
| `make smoke` | Run the HTTP smoke suite against an ephemeral local server |
| `make smoke-docker` | Run the HTTP smoke suite against an existing server |
| `make docker-build` | Build Docker image |
| `make docker-run` | Build and run Docker container |
| `make docker-compose-up` | Start with Docker Compose |
| `make docker-compose-down` | Stop Docker Compose |

### Integration tests

```bash
export PCR_TEST_POSTGRES_URL='postgres://pcr@127.0.0.1/pcr_test?sslmode=disable'
go test -race -tags=integration ./...
```

See [docs/testing.md](docs/testing.md) for the smoke-test commands and focused manual checks of derived alert state.

## Auth

The server follows a zero-trust-by-default model. Dashboard routes always require a human session. For API routes, the default `PCR_REQUIRE_AUTH_READS=true` requires authentication for reads and writes; setting it to `false` permits unauthenticated API `GET` and `HEAD` requests while writes still require an explicit API token. Health, login, and static-asset routes are public as listed below.

API and human authentication are deliberately separate:

1. **API clients:** `Authorization: Bearer <token>`, proxy-friendly `Authorization: Token <token>`, or the backwards-compatible `?token=` query parameter on `/api/v1/*`.
2. **Dashboard users:** GitHub, Google, Authentik, or trusted Beyond identity, selected once at startup, followed by a locally signed PCR session cookie.

API tokens cannot log into the dashboard, and provider OAuth tokens cannot authenticate PCR API routes.

### Dashboard login

Navigate to `/login` to see the single configured provider. PCR uses authorization-code flow with state and PKCE, plus an OIDC nonce for Google and Authentik. The callback retrieves the current GitHub login, verified Google email, or Authentik-issued username plus the provider's stable subject, applies organization/subject policy, and sets an HttpOnly `SameSite=Lax` PCR session cookie.

GitHub organization restriction verifies active membership with `read:org`. Google restriction requires a verified email and an exact signed `hd` Workspace-domain claim. `PCR_ALLOWED_ORGS` has OR semantics; stable subjects are explicit individual exceptions. Provider failures fail closed.

Authentik uses OpenID Connect discovery at the configured per-provider issuer. PCR validates the ID-token signature, issuer, audience, expiry, and nonce. It uses signed `preferred_username`, falling back only to a verified email, and treats exact case-sensitive values in the signed `groups` claim as `PCR_ALLOWED_ORGS`. The Authentik provider must include scope mappings that emit these claims for the requested `openid email profile groups` scopes.

With `PCR_HUMAN_AUTH_PROVIDER=beyond`, PCR performs no OAuth flow and requires `X-Beyond-Email`, with optional `X-Beyond-Name` and pipe-delimited `X-Beyond-Groups`, on every dashboard request. It lowercases the verified email and uses it as both the displayed user name and provider subject. The configured group check is exact and case-sensitive. This mode is safe only when PCR cannot be reached except through Beyond; otherwise callers can forge the headers. Kubernetes installers must apply the checked-in `k8s/networkpolicy-beyond.yaml` or an equivalent policy using their deployment's actual pod labels before exposing PCR. An email change intentionally creates a new PCR identity because the current Beyond header contract does not expose the OIDC `sub` claim.

The signed session contains the provider subject and a mutable profile snapshot. Normal dashboard requests make no provider calls. OAuth provider profiles and policy refresh after the absolute `PCR_HUMAN_SESSION_DURATION` window. In Beyond mode, PCR binds every request to the current verified email and rechecks the current groups, so a changed edge identity cannot reuse a stale PCR session. Historical events retain their original name and subject snapshot.

Set `PCR_SESSION_SECRET` to a stable string so sessions survive server restarts. If unset, a random secret is generated (sessions expire on restart). Set `PCR_COOKIE_SECURE=false` when running locally without TLS (default is `true`).

### CSRF protection

Dashboard POST forms (e.g., star toggle) include a CSRF token derived from the session nonce. The server validates this token on every POST request to dashboard endpoints. API clients using explicit `Bearer` or `Token` credentials are not affected.

### Security headers

All responses include `Referrer-Policy: no-referrer` and `X-Content-Type-Options: nosniff`.

### Public routes

Tokens are configured through the `PCR_API_TOKENS` environment variable (comma-separated). The following routes do not require authentication:

- `/livez` — dependency-free liveness check
- `/readyz` — PostgreSQL-backed readiness check
- `/api/v1/health` — backwards-compatible readiness check
- `/static/*` — CSS and static assets
- `/login`, `/auth/start`, `/auth/callback` — human login endpoints
