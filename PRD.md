# PRD — RateGuard (Rate Limiting + AWS Cost Anomaly, SaaS)

## 1. What is IaC (Infrastructure as Code)?
IaC = defining your cloud resources (EC2, DynamoDB, S3, IAM, VPC, alarms) in **code files** (e.g., Terraform `.tf`) instead of clicking through the AWS console. Benefits: reproducible, version-controlled in git, reviewable in PRs, easy to tear down (destroy all resources in one command), and it's a strong resume signal ("I provisioned infra via Terraform"). **Tool: Terraform**, run via the official Docker image `hashicorp/terraform` (no local install needed — saves C: drive space; Docker Desktop already installed).

### Terraform via Docker (no local install)
```
docker run --rm -v "%cd%:/workspace" -w /workspace hashicorp/terraform:latest init
docker run --rm -v "%cd%:/workspace" -w /workspace hashicorp/terraform:latest plan
docker run --rm -v "%cd%:/workspace" -w /workspace hashicorp/terraform:latest apply
docker run --rm -v "%cd%:/workspace" -w /workspace hashicorp/terraform:latest destroy
```
State stored in S3 + DynamoDB lock table (created once, via a bootstrap script or console).
## 2. Product
**Name:** RateGuard
**One-liner:** Multi-tenant SaaS that rate-limits API traffic (token bucket) and detects AWS spend anomalies per tenant, with a unified dashboard + alerts.
**Goal:** Learn AWS + Docker + Go; build something resume-worthy and free-tier ($0).

## 2b. Why Rate-Limiter-as-a-Service? (Real-life usefulness)
**Yes — it's genuinely useful**, not a toy. Rate limiting is a core API-platform need:
- **Abuse protection:** stops a single client from overwhelming your API (DDoS-ish, accidental loops, scraping).
- **Fairness / multi-tenancy:** per-tenant quotas so one heavy user doesn't starve others — essential for any SaaS.
- **Tiered pricing:** free tier (100 req/min) vs pro (10k req/min) — billing-driven limits are a real revenue lever.
- **Downstream protection:** shields databases/LLMs/payment providers from burst traffic (e.g., protecting an LLM API's token budget).
- **Compliance/SLA:** enforce contractual rate limits per customer.

**How it helps developers:** instead of building token-bucket + Redis + retry logic into every service, they drop in one proxy/gateway with an API key and get limits, analytics, and alerts out of the box. Similar to Upstash Rate Limit, Redis Cloud, Kong, or AWS API Gateway throttling — but self-hosted/owned and customizable.

**Differentiation vs alternatives:** AWS API Gateway throttling is coarse and AWS-locked; Kong/Envoy are heavy. RateGuard is lightweight, Go-based, self-hosted, multi-tenant, and adds AWS cost anomaly monitoring (FinOps) — a niche combo.

## 2c. API Key Model
- Each tenant receives an **API key** (e.g., `ag_live_...`) on signup.
- Client sends key via `Authorization: Bearer <key>` or `X-API-Key` header.
- Proxy validates key against DynamoDB (cached in-memory with TTL), applies the tenant's token-bucket limit, returns `429 Too Many Requests` + `Retry-After` header when exceeded.
- Keys are stored **hashed** (SHA-256) in DynamoDB; raw key shown once at creation.
- Per-tenant config: limit (req/min), burst, enabled endpoints, alert thresholds.
- Usage (allowed/blocked/latency) recorded per key for the dashboard.

## 3. Stack Decisions
- Language: Go 1.27+
- HTTP router: chi
- Rate-limit state: in-process token buckets + DynamoDB (config/audit); no ElastiCache (not free tier)
- IaC: Terraform (region: ap-south-1 Mumbai — lower latency for India; free tier applies)
- CI/CD: GitHub Actions (OIDC to AWS, no long-lived keys)
- Containers: Docker multi-stage → distroless/scratch final image; docker-compose on EC2 free tier
- AWS: API Gateway, Lambda, DynamoDB, S3, CloudWatch, SNS, EventBridge, SSM Parameter Store, ECR, EC2 (t3.micro free tier)

## 4. Free-Tier Constraints
- EC2 t3.micro: 750 hrs/mo (stop when idle)
- DynamoDB: 25 GB + 25 RCU/WCU
- Lambda: 1M req/mo
- API Gateway: 1M calls/mo
- S3: 5 GB
- CloudWatch: basic metrics/logs
- SNS: 1M publishes
- SSM Parameter Store: free (use instead of Secrets Manager)
- Billing alarm at $1/mo; terminate after portfolio period

## 5. Core Features (v1, merged)
1. Tenant onboarding (Cognito or API-key based) — keep simple: API keys in DynamoDB
2. Rate limiting proxy: token bucket per tenant, configurable limits, returns 429 + Retry-After
3. Per-tenant usage analytics dashboard (requests, blocked, latency)
4. AWS cost anomaly detection: EventBridge → Lambda reads CloudWatch billing metrics → anomaly check → SNS alert + dashboard widget
5. Admin UI: manage tenants, limits, view usage & cost anomalies
6. Dockerized: app + worker containers via docker-compose on EC2
7. IaC: Terraform provisions VPC (optional minimal), EC2, DynamoDB, S3, ECR, IAM, alarms
8. CI/CD: GitHub Actions builds Go binary + Docker image → push ECR → deploy to EC2

## 5b. Project Structure (Go + Docker + Terraform)
```
api-guard/
├── cmd/server/main.go            # entrypoint
├── internal/
│   ├── config/                   # env/SSM config
│   ├── limiter/                  # token bucket + tenant limits
│   ├── proxy/                    # reverse proxy + middleware
│   ├── handlers/                 # admin API + dashboard data
│   ├── aws/                      # DynamoDB, CloudWatch, SNS, SSM helpers
│   └── cost/                     # cost anomaly detection logic
├── web/                          # dashboard (HTML/HTMX or React)
├── Dockerfile                    # multi-stage: Go build -> distroless
├── docker-compose.yml            # proxy + dashboard on EC2
├── .dockerignore
├── go.mod / go.sum
├── terraform/
│   ├── main.tf                   # EC2, DynamoDB, S3, ECR, IAM, alarms
│   ├── variables.tf
│   ├── outputs.tf
│   └── bootstrap/                # S3 state bucket + DynamoDB lock (run once)
├── .github/workflows/ci.yml      # build, test, scan, push ECR, deploy
└── README.md
```

## 5c. Full Stack
- **Language:** Go 1.22+ (chi router)
- **State:** in-process token buckets + DynamoDB (no ElastiCache — not free tier)
- **AWS services:** API Gateway (optional), EC2 t3.micro (free tier), DynamoDB, S3, ECR, Lambda (cost anomaly), EventBridge, SNS, CloudWatch, SSM Parameter Store
- **Containers:** Docker multi-stage → distroless final image; docker-compose runs proxy + dashboard on EC2
- **IaC:** Terraform via `hashicorp/terraform` Docker image (state in S3 + DynamoDB lock)
- **CI/CD:** GitHub Actions with OIDC to AWS (build Go binary, scan with Trivy, push to ECR, deploy to EC2)
- **Region:** ap-south-1 (Mumbai) — lower latency for India; free tier applies

## 6. Architecture
```
Client -> API Gateway (optional) / EC2 (Caddy reverse proxy) -> Go proxy (chi)
  - token bucket (in-process) -> DynamoDB (tenant config, audit)
  - usage metrics -> CloudWatch
  - cost anomaly worker (Lambda, EventBridge schedule) -> CloudWatch billing metrics -> SNS alert -> dashboard
EC2 (t3.micro, free tier): docker-compose runs proxy + dashboard containers
ECR: Docker images (tagged by commit SHA)
Terraform: provisions all infra (remote state in S3 + DynamoDB lock)
```

## 7. Harness-Ready Spec (for coding agents)

### 7.1 API Contract (base URL: /api/v1)

Auth: `Authorization: Bearer <api_key>` on all tenant routes.

| Method | Path | Auth | Request | Response | Notes |
|---|---|---|---|---|---|
| POST | /tenants | admin key | `{"name","email","limit_rpm","burst"}` | `{"tenant_id","api_key"}` | key shown once |
| GET | /tenants/{id} | admin | — | tenant object | — |
| PATCH | /tenants/{id} | admin | `{"limit_rpm?","burst?","enabled?"}` | updated tenant | — |
| DELETE | /tenants/{id} | admin | — | 204 | soft delete (enabled=false) |
| GET | /tenants/{id}/usage | admin or tenant | `?from=&to=` | `{"allowed","blocked","avg_latency_ms","p95_ms"}` | — |
| ANY | /proxy/* | tenant key | passthrough | upstream response | rate-limited; 429 + Retry-After on exceed |
| GET | /healthz | none | — | `{"status":"ok"}` | liveness |
| GET | /readyz | none | — | `{"status":"ready"}` | checks DynamoDB |

Error shape (all errors): `{"error":"<code>","message":"<human>"}` with appropriate HTTP status. Codes: `unauthorized`, `not_found`, `rate_limited`, `invalid_input`, `internal`.

Rate-limit response on exceed: `429` with headers `Retry-After: <seconds>`, `X-RateLimit-Limit`, `X-RateLimit-Remaining: 0`, `X-RateLimit-Reset`.

### 7.2 DynamoDB Schemas

Table `api_guard_tenants` (PK: `tenant_id` string):
- `tenant_id` (S, PK)
- `name` (S)
- `email` (S)
- `api_key_hash` (S) — SHA-256 of raw key
- `api_key_prefix` (S) — first 8 chars for lookup/display
- `limit_rpm` (N)
- `burst` (N)
- `enabled` (BOOL)
- `created_at`, `updated_at` (S, ISO8601)
GSI: `api_key_prefix-index` (PK: api_key_prefix) for key→tenant lookup.

Table `api_guard_usage` (PK: `tenant_id`, SK: `bucket` string `YYYY-MM-DDTHH`):
- `tenant_id` (S, PK)
- `bucket` (S, SK)
- `allowed` (N)
- `blocked` (N)
- `sum_latency_ms` (N)
- `count` (N)
- `updated_at` (S)
TTL on a `ttl` attribute (30 days) to auto-expire.

Table `api_guard_cost_anomalies` (PK: `tenant_id`, SK: `detected_at`):
- `tenant_id`, `detected_at`, `metric` (S), `value` (N), `baseline` (N), `threshold` (N), `severity` (S: low/med/high), `alerted` (BOOL).

### 7.3 Token Bucket Algorithm
- Per tenant: capacity = `burst`, refill rate = `limit_rpm / 60` tokens/sec.
- On request: if tokens >= 1, consume 1 and allow; else reject 429.
- State: in-memory map `map[tenantID]*bucket` with mutex; sync to DynamoDB usage table asynchronously (batched, every 10s or 100 events).
- On instance restart, buckets reset (acceptable for v1; document this).

### 7.4 Cost Anomaly Detection (Lambda)
- Trigger: EventBridge schedule every 1 hour.
- Input: tenant list from DynamoDB; for each, fetch CloudWatch metric `EstimatedCharges` (or `AWS/Billing` `EstimatedCharges` with `Currency=USD`) for last 24h vs 7-day rolling mean.
- Anomaly rule: if current > mean + 2*stddev OR current > 1.5*mean → anomaly.
- On anomaly: write to `api_guard_cost_anomalies`, publish SNS message, include in dashboard.
- Idempotency: dedupe by (tenant, metric, hour bucket).

### 7.5 Config / Env Vars & SSM Parameters
Env (container):
- `AWS_REGION` (default ap-south-1)
- `DYNAMODB_TENANTS_TABLE`, `DYNAMODB_USAGE_TABLE`, `DYNAMODB_ANOMALIES_TABLE`
- `SSM_PARAM_PREFIX` (default /api-guard)
- `PROXY_UPSTREAM_URL` (target behind the proxy)
- `PORT` (default 8080)

SSM Parameter Store (free, secure strings where needed):
- `/api-guard/admin-api-key` (SecureString)
- `/api-guard/upstream-url`
- `/api-guard/sns-topic-arn`

### 7.6 Dashboard Tech
- Server-rendered HTML with HTMX + Tailwind (no separate frontend build; keeps Docker image tiny and simple). Go templates in `web/templates/`.

### 7.7 Acceptance Criteria per Milestone
- **M1:** `go build` succeeds; `docker compose up` runs proxy; a request with valid key returns upstream response; exceeded limit returns 429 with Retry-After; unit tests for token bucket pass.
- **M2:** `terraform apply` (via Docker) provisions EC2/DynamoDB/ECR/IAM; proxy reachable on EC2 public IP; `/healthz` returns 200.
- **M3:** Push to main triggers GitHub Actions: tests pass, image built+scanned+pushed to ECR, EC2 updated, `/healthz` 200 post-deploy; rollback by re-running with previous SHA works.
- **M4:** Lambda runs hourly; anomaly injected (fake metric) produces SNS alert + DynamoDB record + dashboard entry.
- **M5:** README with setup, demo curl commands, teardown instructions; `terraform destroy` removes all resources.

### 7.8 Test Plan
- Unit: token bucket (allow/burst/refill/reject), key hashing, anomaly threshold logic.
- Integration (local): docker-compose + DynamoDB Local; create tenant, hit proxy, verify 429.
- Infra: `terraform validate` + `terraform plan` in CI (no apply on PR).
- Smoke (post-deploy): curl /healthz, /readyz, one proxied request.

### 7.9 Non-Functional
- p99 proxy latency < 50ms (in-process limiting).
- Image size < 20 MB (distroless).
- No secrets in code or env files; SSM only.
- Graceful shutdown on SIGTERM (drain in-flight, flush usage).

## 7. Docker Plan
- Multi-stage Dockerfile: build Go binary → copy to scratch/distroless
- .dockerignore, non-root user, pinned base tags
- docker-compose.yml: proxy + dashboard services, restart policies, health checks
- Image scanning via Trivy in CI

## 8. Resume Signals Targeted
- Go backend + reverse proxy, multi-tenancy, token bucket rate limiting
- AWS: EC2, DynamoDB, Lambda, EventBridge, SNS, CloudWatch, ECR, SSM
- Docker: multi-stage, distroless, compose, health checks
- IaC: Terraform modules, remote state, env separation
- CI/CD: GitHub Actions OIDC, build/scan/push/deploy, rollback via previous image

## 9. Milestones
- M1 (weekend 1): Go proxy + token bucket + DynamoDB tenant config + Dockerfile + local docker-compose
- M2 (weekend 1): Terraform infra (EC2, DynamoDB, ECR, IAM) + deploy to EC2
- M3 (weekend 2): GitHub Actions CI/CD (build/scan/push/deploy)
- M4 (weekend 2): Cost anomaly Lambda + SNS alerts + dashboard widget
- M5: Polish, README, demo video, teardown

## 11. Post-Build Demo App (trigger after build complete)
Purpose: a tiny, real consumer of RateGuard so you can demo it confidently (show allowed → blocked → alert).

**Demo app: "Quote of the Day" microservice behind the proxy**
- A minimal Go/Node service returning a random quote (`GET /quote`), deployed as the `PROXY_UPSTREAM_URL`.
- Exposed ONLY through RateGuard with a tenant API key.
- Includes a small HTML page (served by the dashboard) with:
  - "Send 1 request" button → shows quote + remaining quota.
  - "Flood 200 requests" button → triggers 429s; displays `Retry-After`.
  - Live counters: allowed / blocked / p95 latency.
- Curl smoke test:
```
KEY=ag_live_xxx
for i in $(seq 1 120); do curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $KEY" http://<ec2-ip>/proxy/quote; done
# expect first ~burst allowed, then 429s
```
- Presentation script (2 min):
  1. Show tenant creation + API key issuance.
  2. Send requests, show quota headers.
  3. Flood → 429 + Retry-After.
  4. Show dashboard: usage + cost anomaly widget.
  5. Show SNS email alert (triggered by injected fake spend spike).
  6. `terraform destroy` → clean teardown.

## 10. Out of Scope (v1)
- ElastiCache (not free), EKS, Fargate, multi-region, billing beyond CloudWatch metrics
file_path">
E:/possible/aws-docker/PRD.md