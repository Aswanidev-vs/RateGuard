# RateGuard Docker Guide

Practical, Windows-first instructions for building and running RateGuard with Docker Desktop. Commands are shown for **Git Bash** and **PowerShell** where they differ.

> **This guide does not run anything for you.** The commands below are for you to execute from the repository root (`G:\RateGuard`). Docker Desktop builds the image and starts the containers; Caddy terminates HTTP(S); the Go app enforces tenant limits and serves the dashboard.

---

## 1. What you run vs. what is automated

| You do manually | Docker / the app does automatically |
|---|---|
| Copy `.env.example` to `.env` and generate two secrets | Builds both Go binaries in a multi-stage build |
| Run `docker compose up -d --build` | Starts `api`, `demo`, and `caddy` on a private network |
| Create the first tenant (curl or dashboard) | Issues the one-time tenant API key and stores its hash |
| Send demo traffic | Applies the token bucket, returns `429` + `Retry-After` |
| Point a real domain at the host (optional) | Caddy obtains/renews TLS certificates and adds security headers |
| Tear down with `docker compose down` | Flushes usage and stops containers on `SIGTERM` |

The compose stack runs three services from two images:

```
Internet ──:80/:443──> caddy ──> api:8080 ──> demo:8090   (local demo upstream)
                              └─> DynamoDB / SSM         (EC2 mode, outbound)
```

`api` and `demo` use the same locally built `rateguard:<tag>` image. Only Caddy publishes host ports.

---

## 2. Prerequisites

- **Docker Desktop** installed and **running** (Linux containers). Verify:
  ```bash
  docker version
  docker compose version
  ```
  `docker compose` (v2, with a space) is required; `docker-compose` v1 is not supported by this guide.
- **Ports 80 and 443 free** on the host. Check:
  - PowerShell:
    ```powershell
    Get-NetTCPConnection -LocalPort 80,443 -State Listen -ErrorAction SilentlyContinue |
      Select-Object LocalAddress,LocalPort,OwningProcess
    ```
  - Git Bash:
    ```bash
    netstat -ano | grep -E ':(80|443)[[:space:]]'
    ```
- **Repository root** as the working directory:
  - PowerShell: `cd G:\RateGuard`
  - Git Bash: `cd /g/RateGuard`
  - Confirm `Dockerfile`, `docker-compose.yml`, and `.env.example` are present.
- About 1 GB of free RAM and internet access for the first build (Go modules and base images).
- `curl.exe` for smoke tests (included with Windows 10/11). In PowerShell, use `curl.exe`, **not** `curl` (which is an alias for `Invoke-WebRequest`).

---

## 3. Create `.env` and generate secrets

The app requires `ADMIN_API_KEY`; `SESSION_SECRET` is strongly recommended so dashboard sessions survive restarts. Both are read from `.env` (gitignored). Generate random values instead of inventing them.

### Git Bash

```bash
cd /g/RateGuard
cp .env.example .env

ADMIN_KEY="$(openssl rand -base64 48)"
SESSION_KEY="$(openssl rand -base64 48)"

sed -i "s|^ADMIN_API_KEY=.*|ADMIN_API_KEY=${ADMIN_KEY}|" .env
sed -i "s|^SESSION_SECRET=.*|SESSION_SECRET=${SESSION_KEY}|" .env

# Confirm they are no longer placeholders, without printing the values.
grep -E '^(ADMIN_API_KEY|SESSION_SECRET)=replace-with' .env || echo "Secrets replaced."
```

### PowerShell

```powershell
cd G:\RateGuard
Copy-Item .env.example .env

function New-RateGuardSecret {
    $rng   = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $bytes = New-Object byte[] 48
    $rng.GetBytes($bytes)
    [Convert]::ToBase64String($bytes)
}

$adminKey    = New-RateGuardSecret
$sessionKey  = New-RateGuardSecret
$content     = Get-Content .env
$content     = $content -replace '^ADMIN_API_KEY=.*',    "ADMIN_API_KEY=$adminKey"
$content     = $content -replace '^SESSION_SECRET=.*',  "SESSION_SECRET=$sessionKey"

# ASCII avoids a UTF-8 BOM, which can corrupt the first env key.
Set-Content -Path .env -Value $content -Encoding ascii
Write-Host "Secrets written to .env"
```

### Rules for `.env`

- `ADMIN_API_KEY` must be at least **16 characters**; use the 64-character base64 output above.
- `SESSION_SECRET` must be at least **32 characters** if set. Rotating it invalidates every dashboard session.
- Values must be on one line, with no spaces around `=` and no surrounding quotes. Base64 values (`A–Z a–z 0–9 + / =`) are safe in `.env`.
- Keep `COOKIE_SECURE=false` while using `http://localhost`. Set it to `true` only behind HTTPS.
- Never commit `.env`. Check:
  ```bash
  git status --short
  git check-ignore -v .env
  ```
  The second command should report `.env` as ignored.

`.env` is loaded twice by Compose: for `${...}` interpolation in `docker-compose.yml`, and as the container environment via `env_file`. The compose file therefore works unchanged for both the local memory backend and the EC2/DynamoDB deployment — edit `.env`, not the YAML.

---

## 4. Build, start, and verify

From the repository root:

```bash
docker compose up -d --build
docker compose ps
```

The first build takes a few minutes. Expected state:

| Service | State | Notes |
|---|---|---|
| `api` | `running (healthy)` | Waits for `demo` to start |
| `demo` | `running` | No healthcheck (distroless has no `wget`/`curl`) |
| `caddy` | `running` | Starts only after `api` is healthy |

Useful commands:

```bash
# All logs, following
docker compose logs -f --tail=100

# One service
docker compose logs -f --tail=100 api
docker compose logs --tail=200 caddy

# API health through Caddy (host -> caddy -> api)
curl -fsS http://localhost/healthz

# Docker's health status
docker inspect --format '{{.State.Health.Status}}' "$(docker compose ps -q api)"

# Run the binary's own probe inside the container (works without a shell)
docker compose exec api /app/rateguard -healthcheck
```

PowerShell equivalents for the last two:

```powershell
$apiId = docker compose ps -q api
docker inspect --format '{{.State.Health.Status}}' $apiId
docker compose exec api /app/rateguard -healthcheck
if ($LASTEXITCODE -eq 0) { "API healthcheck OK" } else { "API healthcheck FAILED" }
```

`/healthz` should return `{"status":"ok"}`. Do not publish `api:8080` or `demo:8090`; they are reachable only through Caddy and the Compose network.

---

## 5. Create the first tenant

Admin routes use the **admin key**. Tenant/proxy routes use the **tenant API key**. The tenant key is displayed only once — copy it immediately.

### Git Bash

```bash
ADMIN_KEY="$(grep '^ADMIN_API_KEY=' .env | cut -d= -f2- | tr -d '\r')"

curl -sS -X POST http://localhost/api/v1/tenants \
  -H "Authorization: Bearer ${ADMIN_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"name":"Demo Tenant","email":"demo@example.com","limit_rpm":60,"burst":10}'
```

Example response:

```json
{"tenant_id":"4f2c...","api_key":"ag_live_..."}
```

Save the returned key in the same terminal:

```bash
export TENANT_KEY='ag_live_...'
```

The `tr -d '\r'` matters in Git Bash: a `.env` written by PowerShell has CRLF line endings, and a stray carriage return makes the header invalid (symptom: `401`).

### PowerShell

```powershell
$AdminKey = (Get-Content .env |
  Where-Object { $_ -match '^ADMIN_API_KEY=' }) -replace '^ADMIN_API_KEY=', ''

$body = @{
    name      = 'Demo Tenant'
    email     = 'demo@example.com'
    limit_rpm = 60
    burst     = 10
} | ConvertTo-Json

$tenant = curl.exe -sS -X POST http://localhost/api/v1/tenants `
  -H "Authorization: Bearer $AdminKey" `
  -H "Content-Type: application/json" `
  -d $body | ConvertFrom-Json

$tenant
$TenantKey = $tenant.api_key
```

If the response is empty or HTML, re-run with `-i` to see the status and any error body. A `401` means the admin key is wrong/truncated; a `400` means the JSON body is malformed.
