# Security Audit — RateGuard

Source-first audit of the RateGuard codebase, performed with the `security-audit` skill's trust-boundary and evidence discipline.

## Scope and limits

- **Reviewed:** all Go packages (`cmd/`, `internal/`, `web/`), Docker/compose/Caddy configs, Terraform root + bootstrap, GitHub Actions workflow, docs.
- **Not executed:** Docker builds, Terraform plan/apply, Trivy scans, or any call to a live AWS account. Those are operator steps by design (see the Docker and Terraform guides). No deployed endpoint was probed.
- **Method limitation:** the skill's delegated hunter/verifier agent waves could not run because the provider's free-usage limit stopped all six subagents. This is therefore a single-agent source audit with bounded local tests (`go test -race ./...`, `go vet ./...`), not the full multi-agent workflow. Findings below are grounded in source lines and local regression tests; deployment-dependent controls remain **needs-validation**.

## Trust boundaries

| Boundary | Untrusted input | Control | Evidence |
|---|---|---|---|
| Internet → app | Tenant/admin keys | Constant-time hash checks, bounded TTL cache, IP attempt limits | `internal/auth/*`, `internal/server/api.go`, `internal/server/proxy.go` |
| Browser → dashboard | Forms, cookies, Origin | HMAC session cookie, CSRF token, same-origin check, strict CSP | `internal/server/session.go`, `internal/server/dashboard.go`, `internal/server/server.go` |
| Proxy → upstream | Request path/headers | Fixed upstream from config, dot-segment guard, sensitive headers stripped | `internal/server/proxy.go` |
| App/Lambda → AWS | DynamoDB items, SSM params, SNS | IAM least privilege, SecureString params, conditional writes | `terraform/iam.tf`, `terraform/ssm.tf`, `internal/store/dynamodb.go` |
| GitHub Actions → AWS | OIDC token | Role trust restricted to `repo:Aswanidev-vs/RateGuard:ref:refs/heads/main` | `terraform/iam.tf`, `.github/workflows/ci.yml` |
| Terraform state → operator | Secret values | Private encrypted versioned S3 bucket, documented warning | `terraform/s3.tf`, `docs/terraform/TERRAFORM-GUIDE.md` |

## Findings

| ID | Severity | Finding | Fix |
|---|---|---|---|
| SEC-01 | High (integrity) | DynamoDB usage writes used `:sum_latency_ms` in the update expression but bound `:sum_latency`; DynamoDB would reject every usage write in AWS, so dashboard metrics would be empty while local memory-store tests passed. | Renamed the value key to `:sum_latency_ms` and added `internal/store/dynamodb_test.go`, which asserts every placeholder/name in the expression is bound and used. |
| SEC-02 | Medium | The proxy stripped `/proxy` but did not reject `..` segments; with a base path on `PROXY_UPSTREAM_URL`, a client could reach unintended upstream paths (`/proxy/../admin`). | Added dot-segment rejection in the proxy Director plus chi `CleanPath`, with a regression test (`TestProxyRejectsDotSegments`). |
| SEC-03 | Medium | The login/admin `AttemptLimiter` bounded expired entries but could grow without limit when every source IP still had a live window — a memory-exhaustion vector from spoofed or rotating `X-Forwarded-For` values. | Added eviction of an existing entry when the 8192-key cap is reached (`internal/auth/attempts.go`). |
| SEC-04 | Medium (IaC) | The GitHub OIDC deploy role allowed `ssm:SendCommand` against any EC2 instance ARN. | Scoped the resource to `aws_instance.app.arn` in `terraform/iam.tf`. |
| SEC-05 | Low | Rendered error pages could be cached by an intermediate proxy, potentially replaying a stale error or page shell. | `renderError` now sets `Cache-Control: no-store`. |
| SEC-06 | Low | The demo upstream set nosniff/frame/referrer headers but no CSP. | Added `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'; base-uri 'none'` to `cmd/demo`. |
| SEC-07 | Informational | Admin key, session secret, and generated session secret are stored in Terraform state as well as SSM. No source change can avoid this while Terraform creates the SSM parameters. | Documented: private encrypted versioned state bucket, gitignored `backend.hcl`/`*.tfvars`, option to pass secrets via `TF_VAR_*`. |

## Verified controls (no finding)

- Raw API keys are generated with `crypto/rand`, hashed with SHA-256, compared in constant time, never logged, and shown only in the one-time create response. Tenant keys are not forwarded to the upstream (asserted in `TestTenantLifecycleAndProxyRateLimit`).
- Admin login is behind a constant-time verifier, per-IP attempt limiting, signed `HttpOnly`/`SameSite=Strict` cookies, CSRF tokens on all mutating dashboard routes, and an Origin/`Sec-Fetch-Site` check (asserted in `TestDashboardLoginCSRFAndPages`).
- Tenant usage is strictly scoped: a tenant key can only read its own usage; other tenants get `403`. Disabled tenants are rejected immediately via cache invalidation.
- Proxy rate-limit headers cannot be spoofed by the upstream: upstream `X-RateLimit-*`/`Retry-After` values are removed and replaced by the proxy's own decision.
- API JSON bodies are capped (1 MiB), decoded strictly (unknown fields rejected), and errors use a fixed envelope without internal details.
- Docker image runs as UID 65532 on distroless, with read-only rootfs, no-new-privileges, dropped capabilities, tmpfs only, and no secrets in the image or build args.
- Terraform uses IMDSv2, no default SSH ingress, S3 block-public-access, DynamoDB SSE, and an OIDC role with no static AWS keys in CI.

## Needs validation by the operator

These depend on resources that intentionally were not created during coding:

1. Run `terraform plan`/`apply` and confirm IAM policies allow EC2 SSM parameter reads, ECR pull, and DynamoDB writes.
2. Run the Trivy scan in CI and confirm the distroless image passes the HIGH/CRITICAL gate.
3. Confirm the GitHub OIDC provider exists and the role trust condition matches the repository.
4. After deployment, verify `/readyz`, dashboard login, a 429 flood, and the fake-spend Lambda test from the Terraform guide.

## Verification performed

```
go build ./...
go vet ./...
go test -race ./...
```

All passed after the fixes. `staticcheck` in this environment cannot read Go 1.27 standard-library export data and was not usable.
