# Testing Guide

The repository has unit tests, SQLite integration tests, and an HTTP smoke-test client. The commands below reflect the current append-only API.

## Automated tests

Run the default test suite with the race detector and package coverage:

```bash
make test
```

Run tests that exercise a real temporary SQLite database and apply the embedded migrations:

```bash
go test -race -tags=integration ./...
```

Run the end-to-end smoke suite against an ephemeral local server:

```bash
make smoke
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

## Verify recorded deployment phases

Deployment phases are a separate convention. They record lifecycle points but are not reduced into current annotation state:

```bash
pcr -X POST http://localhost:8080/api/v1/events -d '{
  "external_id": "manual-deploy-d-001-start",
  "user_name": "alice",
  "event_type": "deployment",
  "description": "Deploy api v2.4.1 started",
  "tags": {"deploy_id": "d-001", "phase": "start", "env": "prod"}
}' | jq

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

The result contains two top-level events. The server does not pair them or infer an active/done state from the `phase` tag.

## Dashboard checks

Open `http://localhost:8080/login`, enter `test-token`, and verify:

1. The default view contains top-level events timestamped within the last 24 hours.
2. Time-range, event-type, user, and tag filters change the displayed rows.
3. The star button changes the event's current star annotation.
4. An actively alerted event has alert styling and appears in the Alerts view if its parent event is within the selected time range.
5. The detail page displays the event and its current annotation state. It does not display the meta-event history.
6. The page refreshes at the configured `PCR_DASHBOARD_REFRESH_SEC` interval.

## Idempotency

`external_id` is globally unique when present. The first request returns `201 Created`; a retry with the same value returns the original event with `200 OK`, even if other request fields differ. Events without `external_id` are always newly created.

## Authentication checks

With the default `PCR_REQUIRE_AUTH_READS=true`:

- `/api/v1/health`, `/login`, and `/static/*` are public.
- Other reads and all writes require a Bearer token, a valid session cookie, or the backwards-compatible `token` query parameter.
- Setting `PCR_REQUIRE_AUTH_READS=false` makes `GET` and `HEAD` requests public; writes still require authentication.
