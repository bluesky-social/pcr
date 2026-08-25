# Deployment Guide

Three deployment methods, in order of simplicity.

## 1. Docker (single container)

### Prerequisites
- Docker (tested with 29.x)
- Colima, Docker Desktop, or another Docker daemon
- A reachable PostgreSQL database and connection URL

### Build the image

```bash
make docker-build
# or: docker build -t pcr-server .
```

### Run the container

```bash
export PCR_SESSION_SECRET="$(openssl rand -base64 48)"
export PCR_DATABASE_URL="postgres://pcr:password@postgres.example.internal:5432/pcr?sslmode=require"
docker run -d --name pcr-server \
  -p 8080:8080 \
  -e PCR_API_TOKENS=my-secret-token \
  -e PCR_SESSION_SECRET \
  -e PCR_DATABASE_URL \
  -e PCR_COOKIE_SECURE=false \
  pcr-server
```

The server applies PostgreSQL migrations on startup by default. `PCR_COOKIE_SECURE=false` is appropriate only for this local HTTP example; deployments served over HTTPS should keep the default `true`.

### Sanity check

```bash
# Health check (no auth required)
curl -s http://localhost:8080/api/v1/health | jq
# Expected: {"status":"ok"}

# Create an event
curl -s -X POST -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "alice",
  "event_type": "deployment",
  "description": "docker sanity check",
  "tags": {"env": "prod"}
}' | jq

# List events
curl -s -H "Authorization: Bearer my-secret-token" \
  "http://localhost:8080/api/v1/events?top_level=true" | jq '.total_count'

# Open dashboard in browser
# Open the login form and enter the token
open "http://localhost:8080/login"
```

### Tear down

```bash
docker stop pcr-server && docker rm pcr-server
```

## 2. Docker Compose

### Prerequisites
- Docker with Compose plugin (`docker compose version`)

### Start

Compose starts PostgreSQL and persists it in the `postgres-data` volume.

```bash
PCR_API_TOKENS=my-secret-token docker compose up -d --build
```

Or set the env var in a `.env` file (not committed to git):
```
PCR_API_TOKENS=my-secret-token
PCR_SESSION_SECRET=replace-with-32-byte-secret-from-openssl-rand
```

Then just:
```bash
docker compose up -d --build
```

### Sanity check

Same as Docker above -- the service is available at `http://localhost:8080`.

```bash
# Quick end-to-end check
curl -s http://localhost:8080/api/v1/health | jq
curl -s -X POST -H "Authorization: Bearer my-secret-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "bob",
  "event_type": "feature-flag",
  "description": "compose sanity check"
}' | jq '{id, description}'
# Open the login form and enter the token
open "http://localhost:8080/login"
```

### View logs

```bash
docker compose logs -f
```

### Tear down

```bash
docker compose down
# To also remove the data volume:
docker compose down -v
```

## 3. Kubernetes (kind)

### Prerequisites
- Docker (running)
- kind (`brew install kind`)
- kubectl (`brew install kubectl`)

### Create a cluster

```bash
kind create cluster --name pcr
```

### Build and load the image

kind runs its own containerd runtime, so images must be loaded explicitly:

```bash
docker build -t pcr-server:latest .
kind load docker-image pcr-server:latest --name pcr
```

### Edit secrets

Before applying, edit `k8s/secret.yaml` with your actual values:

```yaml
stringData:
  api-tokens: "your-actual-token"
  session-secret: "a-random-secret-of-at-least-32-bytes"
  database-url: "postgres://pcr:password@postgres.example.internal:5432/pcr?sslmode=require"
```

The checked-in placeholders are not deployment credentials, and the placeholder session secret is too short for the server to accept. Replace all values before applying the manifests. The PostgreSQL host must be reachable from the `pcr` namespace.

### Apply manifests

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
```

### Verify the pod is running

```bash
kubectl -n pcr get pods
# Wait for both replicas to report STATUS: Running and READY: 1/1

kubectl -n pcr logs deploy/pcr-server
# Should show: "starting server addr=:8080"
```

### Port-forward for local access

```bash
kubectl -n pcr port-forward svc/pcr-server 8080:8080
```

### Sanity check

With port-forward running (in another terminal or backgrounded):

```bash
# Health
curl -s http://localhost:8080/api/v1/health | jq

# Create event
curl -s -X POST -H "Authorization: Bearer your-actual-token" \
  -H "Content-Type: application/json" \
  http://localhost:8080/api/v1/events -d '{
  "user_name": "charlie",
  "event_type": "k8s-change",
  "description": "kind sanity check",
  "tags": {"cluster": "local", "env": "dev"}
}' | jq '{id, description}'

# List
curl -s -H "Authorization: Bearer your-actual-token" \
  "http://localhost:8080/api/v1/events?top_level=true" | jq '.total_count'

# Dashboard through the backwards-compatible query-token flow. This is suitable
# only for local testing; prefer HTTPS and the /login form in a real deployment.
open "http://localhost:8080/?token=your-actual-token"
```

### Tear down

```bash
kind delete cluster --name pcr
```

## Environment Variables Reference

All methods use the same environment variables. Key settings:

| Variable | Required | Default | Description |
|---|---|---|---|
| `PCR_API_TOKENS` | Yes | -- | Comma-separated API tokens |
| `PCR_SESSION_SECRET` | No | (random 32-byte) | HMAC key for session cookies. Must be at least 32 bytes when set; generate via `openssl rand -base64 48`. Set for persistent sessions across restarts. |
| `PCR_DATABASE_URL` | Yes | -- | PostgreSQL connection URL; require TLS outside local development |
| `PCR_AUTO_MIGRATE` | No | `true` | Run schema migrations on startup |
| `PCR_ADDR` | No | `:8080` | Listen address |
| `PCR_REQUIRE_AUTH_READS` | No | `true` | Require auth for read endpoints |
| `PCR_DASHBOARD_REFRESH_SEC` | No | `60` | Dashboard auto-refresh interval in seconds |
| `PCR_READ_TIMEOUT` | No | `5s` | HTTP server read timeout (Go duration) |
| `PCR_WRITE_TIMEOUT` | No | `10s` | HTTP server write timeout (Go duration) |
| `PCR_SHUTDOWN_TIMEOUT` | No | `15s` | Graceful shutdown timeout (Go duration) |
| `PCR_DB_CONNECT_TIMEOUT` | No | `5s` | PostgreSQL startup connection timeout |
| `PCR_DB_MAX_CONNECTIONS` | No | `10` | Maximum PostgreSQL connections per replica |
| `PCR_DB_SLOW_QUERY_THRESHOLD` | No | `100ms` | Log a warning when a store operation exceeds this |
| `PCR_COOKIE_SECURE` | No | `true` | Set the `Secure` flag on session cookies. Set to `false` for local dev without TLS |

See the README for the full configuration reference.

## Notes

- **PostgreSQL:** Use a managed PostgreSQL service with TLS, automated backups, and point-in-time recovery for production.
- **Connection budget:** Ensure replica count multiplied by `PCR_DB_MAX_CONNECTIONS` remains below the database connection limit.
- **Migrations:** Startup migrations use a PostgreSQL advisory lock, so concurrent replicas serialize schema changes.
- **Image size:** The production image is based on Alpine 3.21 with a statically-linked Go binary.
- **Rollouts:** The Kubernetes Deployment uses rolling updates with two replicas.
