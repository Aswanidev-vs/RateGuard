# RateGuard

Multi-tenant API rate limiting (token bucket) plus AWS cost anomaly detection, with a small operator dashboard. Built with Go, chi, DynamoDB, Docker, and Terraform. Target: a resume-worthy, free-tier-friendly system you can demo end to end.

> Status: implementation and configuration are complete in this repository. **No Docker build, Terraform apply, or AWS deployment has been run by the coding agent.** See the guides for the commands you run yourself.

## Architecture

```
Client ──> Caddy (:80/:443) ──> Go proxy (:8080)
                                ├─ token bucket (in-process) ──> DynamoDB (config/audit)
                                ├─ usage buffer ──────────────> DynamoDB (hourly, 30-day TTL)
                                └─ dashboard (HTMX, embedded templates)
                                      │
EventBridge (hourly) ──> Lambda ──> CloudWatch billing ──> DynamoDB ──> SNS + dashboard
```

- **Tenant keys:** `ag_live_<random>`; shown once, stored as SHA-256 + short prefix, verified in constant time with a bounded TTL cache.
- **Limiting:** per-tenant capacity = `burst`, refill = `limit_rpm / 60` tokens/s. Exceeded requests get `429` + `Retry-After` and `X-RateLimit-*` headers.
- **Usage:** buffered in memory, flushed in batches; avg/p95 are derived from a fixed latency histogram.
- **Cost anomalies:** account-wide `AWS/Billing EstimatedCharges` vs a 7-day baseline (`> baseline + 2σ` or `> 1.5 × baseline`), deduplicated per UTC hour.
- **Dashboard:** session-authenticated (HMAC cookie, CSRF-protected mutations), server-rendered HTML with vendored htmx. No CDN assets.

## Repository layout

| Path | Purpose |
|---|---|
| `cmd/server` | API, proxy, and dashboard entrypoint (`-healthcheck` flag for containers) |
| `cmd/lambda` | Scheduled cost anomaly detector (fake-mode via `COST_FAKE_*`) |
| `examples/quote` | "Quote of the day" example upstream used by the proxy demo |
| `internal/limiter`, `internal/auth` | Token bucket, API/admin keys, rate-limited login |
| `internal/store`, `internal/usage` | In-memory + DynamoDB persistence, batched usage writer |
| `internal/server` | chi router, middleware, admin API, proxy, dashboard handlers |
| `internal/cost` | Anomaly evaluation, CloudWatch/SNS adapters, worker |
| `web/` | Embedded templates and static assets |
| `terraform/` | Root stack + `bootstrap/` for remote state |
| `.github/workflows/ci.yml` | Test → validate → scan/push → SSM deploy |
| `docs/docker/`, `docs/terraform/` | Operator guides you execute manually |

## Quick start (local, Docker)

```bash
cp .env.example .env
# generate ADMIN_API_KEY and SESSION_SECRET (see docs/docker/DOCKER-GUIDE.md)
docker compose up -d --build

KEY=$(curl -s -X POST http://localhost/api/v1/tenants \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Demo","email":"demo@example.com","limit_rpm":60,"burst":5}' \
  | python -c 'import json,sys; print(json.load(sys.stdin)["api_key"])')

for i in $(seq 1 20); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -H "Authorization: Bearer $KEY" http://localhost/proxy/quote
done
```

Dashboard: `http://localhost/dashboard` — sign in with the admin key.

Full commands (PowerShell alternatives, TLS, EC2 variant, teardown): **[docs/docker/DOCKER-GUIDE.md](docs/docker/DOCKER-GUIDE.md)**.

## Deploy to AWS

1. Build the Lambda binary: `GOOS=linux GOARCH=arm64 go build -o build/bootstrap ./cmd/lambda`.
2. Bootstrap state and apply the stack via the Terraform Docker image.
3. Push the first image to ECR (CI does later pushes).
4. Set GitHub repository variables and push to `main`.

Full step-by-step, outputs, anomaly-injection test, rollback, and destroy: **[docs/terraform/TERRAFORM-GUIDE.md](docs/terraform/TERRAFORM-GUIDE.md)**.

## API summary

Base: `/api/v1`. Admin routes use `Authorization: Bearer <admin key>` (or `X-Admin-Key`).

| Method | Path | Auth | Notes |
|---|---|---|---|
| POST | `/tenants` | admin | `{"name","email","limit_rpm","burst"}` → `{"tenant_id","api_key"}` (key shown once) |
| GET | `/tenants` | admin | List tenants |
| GET | `/tenants/{id}` | admin | Tenant object (hash never returned) |
| PATCH | `/tenants/{id}` | admin | `limit_rpm`, `burst`, `enabled` |
| DELETE | `/tenants/{id}` | admin | Soft delete (`enabled=false`) |
| GET | `/tenants/{id}/usage` | admin or tenant | `?from=&to=`, summary with avg/p95 |
| ANY | `/proxy/*` | tenant | Forwards to upstream; `429` + headers on exceed |
| GET | `/healthz`, `/readyz` | none | Liveness / store readiness |

Errors use `{"error":"<code>","message":"<human>"}` with codes `unauthorized`, `forbidden`, `not_found`, `rate_limited`, `invalid_input`, `internal`.

## Verification (already run)

```
go build ./...
go vet ./...
go test -race ./...
```

`staticcheck` in this environment is too old for Go 1.27 standard-library export data and was not usable. All Go tests pass, including end-to-end proxy and dashboard flows.

## Known v1 boundaries

- Token buckets are per process and reset on restart; usage has a bounded flush queue and can lose the final batch on a hard kill.
- p95 is approximated from fixed histogram buckets.
- AWS billing is account-wide, so anomalies use the reserved `__account__` scope.
- DynamoDB writes are sequential within a flush; designed for the free-tier request volume, not high throughput.

## Teardown

- Local: `docker compose down -v` (see Docker guide).
- AWS: `terraform destroy`, then destroy the separate bootstrap stack (see Terraform guide).
