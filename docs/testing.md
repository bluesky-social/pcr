# Testing Guide

The repository has unit tests, real-PostgreSQL integration tests, and an HTTP smoke-test client. The commands below reflect the current append-only API.

## Automated tests

Run the default test suite with the race detector and package coverage:

```bash
make test
```

Set a PostgreSQL test URL. Integration tests create and remove isolated schemas in this database:

```bash
export PCR_TEST_POSTGRES_URL='postgres://pcr@127.0.0.1/pcr_test?sslmode=disable'
go test -race -tags=integration ./...
```

Run the end-to-end smoke suite against a local server using an isolated PostgreSQL schema:

```bash
export PCR_DATABASE_URL='postgres://pcr@127.0.0.1/pcr_test?sslmode=disable'
make smoke
```

Run the seeded real-PostgreSQL dashboard functional test:

```bash
PCR_TEST_POSTGRES_URL="$PCR_TEST_POSTGRES_URL" go test -tags=integration ./internal/handler -run TestSeededDashboardViews
```

It loads `testdata/functional/phosphor-demo.json` and exercises Current, Site-wide, History, Alerts, lifecycle reduction, link annotations, alert toggling, operation closure, safely escaped link labels, the activity trail, the severity banner, and locally embedded font delivery through the real router.

Fuzz the repeated link form fields and link security validator:

```bash
go test -run='^$' -fuzz=FuzzParseLinkForm -fuzztime=10s ./internal/handler
go test -run='^$' -fuzz=FuzzValidateLinks -fuzztime=10s ./internal/service
```

To test an already-running server, including the Docker Compose service:

```bash
make smoke-docker
```

Override `SMOKE_DOCKER_URL` and `SMOKE_DOCKER_TOKEN` when the target does not use the Makefile defaults.

## Manual setup

For local HTTP testing, disable secure-only cookies:

```bash
make build
export PCR_API_TOKENS=test-token
export PCR_DATABASE_URL='postgres://pcr@127.0.0.1/pcr?sslmode=disable'
export PCR_COOKIE_SECURE=false
./bin/pcr-server
```

In another shell:

```bash
export PCR_TOKEN=test-token
alias pcr='curl -sS -H "Authorization: Bearer $PCR_TOKEN" -H "Content-Type: application/json"'
```

The unauthenticated health check should return `{"status":"ok"}`:

```bash
curl -sS http://localhost:8080/api/v1/health | jq
```

## Verify current alert state

Create a top-level incident event and retain its ID:

```bash
EVENT_ID=$(pcr -X POST http://localhost:8080/api/v1/events -d '{
  "user_name": "on-call",
  "event_type": "incident",
  "description": "Investigating elevated error rate",
  "tags": {"severity": "SEV0", "service": "api"}
}' | jq -r .id)
```

Open its alert state by appending an `alert` meta-event:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d "{
  \"parent_id\": \"$EVENT_ID\",
  \"user_name\": \"on-call\",
  \"event_type\": \"alert\",
  \"description\": \"Incident response underway\"
}" | jq
```

The derived state should now be alerted:

```bash
pcr "http://localhost:8080/api/v1/events/$EVENT_ID/annotations" | jq
# {"starred":false,"alerted":true}

pcr "http://localhost:8080/api/v1/events?alerted=true&top_level=true" \
  | jq --arg id "$EVENT_ID" '.events[] | select(.id == $id)'
```

Close the state by appending a `clear-alert` meta-event:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d "{
  \"parent_id\": \"$EVENT_ID\",
  \"user_name\": \"on-call\",
  \"event_type\": \"clear-alert\",
  \"description\": \"Incident resolved\"
}" | jq

pcr "http://localhost:8080/api/v1/events/$EVENT_ID/annotations" | jq
# {"starred":false,"alerted":false}
```

Current annotation state follows meta-event creation order, not a caller-supplied event timestamp. The newest `alert` or `clear-alert` transition wins; star state is reduced independently from `star` and `unstar` transitions.

## Verify current production changes

Create a deployment start. This example uses the legacy `deploy_id`; new integrations should prefer `change_id`:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "manual-deploy-d-001-start",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 started",
  "tags": {"deploy_id": "d-001", "phase": "start", "team": "payments", "scope": "service", "severity": "sev1", "env": "prod"}
}' | jq
```

It appears in Current without a time bound:

```bash
pcr "http://localhost:8080/api/v1/current?for_team=payments" \
  | jq '.events[] | select(.tags.deploy_id == "d-001")'
```

Append the matching end event:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "manual-deploy-d-001-end",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 completed",
  "tags": {"deploy_id": "d-001", "phase": "end", "env": "prod"}
}' | jq

pcr "http://localhost:8080/api/v1/events?tag=deploy_id:d-001" \
  | jq '.events[] | {timestamp, description, tags}'
```

History still contains both immutable rows, while Current no longer includes `d-001`:

```bash
pcr "http://localhost:8080/api/v1/current?for_team=payments" \
  | jq '.events[] | select(.tags.deploy_id == "d-001")'
# no output
```

Use a new logical identifier for a restarted operation. A retry of the same phase should reuse its phase-specific `external_id`. Exact lowercase phase values participate; display tag values should also be lowercase, although severity reads accept existing uppercase values such as `SEV0`.

## Dashboard checks

Open `http://localhost:8080/login`, enter `test-token`, and verify:

1. The default view contains top-level events timestamped within the last 24 hours.
2. Current shows the selected team's active work, unattributed active work, and active site-wide work regardless of age.
3. Site-wide shows only active `scope=site` work, and site rows are visually distinct.
4. Active `sev0` and `sev1` work appears in the banner independently of table pagination.
5. Time-range, event-type, user, and tag filters change History rows.
6. The star button changes the event's current star annotation.
7. An actively alerted event has alert styling and appears in the Alerts view if its parent event is within the selected time range.
8. The detail page displays the event and its current annotation state. It does not display the meta-event history.
9. The page refreshes at the configured `PCR_DASHBOARD_REFRESH_SEC` interval.

## Idempotency

`external_id` is globally unique when present. The first request returns `201 Created`; a retry with the same value returns the original event with `200 OK`, even if other request fields differ. Events without `external_id` are always newly created.

## Authentication checks

With the default `PCR_REQUIRE_AUTH_READS=true`:

- `/api/v1/health`, `/login`, and `/static/*` are public.
- API reads and writes require a Bearer token or the backwards-compatible `token` query parameter. Dashboard routes also accept a valid session cookie.
- Setting `PCR_REQUIRE_AUTH_READS=false` makes `GET` and `HEAD` requests public; writes still require authentication.
