# RateGuard Docker Config, Line by Line

Companion to [`DOCKER-GUIDE.md`](DOCKER-GUIDE.md). That guide tells you which commands to run. This one explains **why each file is written the way it is**, so you can change it yourself without breaking the build.

Read this file next to the three real files:

| File | Lines | Job |
|---|---|---|
| [`.dockerignore`](../../.dockerignore) | 44 | Filters what Docker sends to the build |
| [`Dockerfile`](../../Dockerfile) | 67 | Recipe that turns source into a runnable image |
| [`docker-compose.yml`](../../docker-compose.yml) | 112 | Describes the three containers and how they talk |

---

## Part 1 — The mental model

Before any line-by-line detail, three ideas carry most of the weight.

**1. An image is built in layers, and layers are cached.**
Every instruction in a `Dockerfile` creates a layer. Docker reuses a cached layer as long as that instruction *and everything that produced it* is unchanged. Change line 30 and lines 31+ rebuild; lines 1–29 come from cache. This is why the `Dockerfile` copies `go.mod` before copying the rest of the source — dependency downloads are slow and rarely change, so they get their own early layer.

**2. The build context is uploaded to the daemon first.**
`docker build .` tars up the current directory (minus `.dockerignore` entries) and ships it to the Docker daemon. That upload is often the slowest part of a build on Windows. `.dockerignore` is how you keep it small and keep secrets out of the image.

**3. Compose is a local orchestrator, not a production one.**
`docker-compose.yml` wires containers together on a private network. Containers on the same Compose network reach each other by **service name** as a hostname — `api` can call `http://demo:8090` because Compose runs a DNS resolver inside the network. That is why `expose` (network-internal) differs from `ports` (published to your machine).

---

## Part 2 — `.dockerignore`, line by line

### What it actually does

`.dockerignore` sits next to the `Dockerfile` and uses the same syntax as `.gitignore`. Docker reads it **before** building, so excluded files never enter the context. Two consequences:

- They cannot be copied in by `COPY . .`, so they never reach the image.
- They are not uploaded, so the build starts faster.

It does **not** shrink your image by itself — only `COPY` and multi-stage builds do that. Think of it as a gate on what the daemon is allowed to see.

### Lines 1–6 — infrastructure that the compiler never needs

```dockerignore
# VCS / CI / infra: never needed to compile the app.
.git
.gitignore
.github
terraform
deploy
```

`.git` is the big one. A repository's history can dwarf its working tree, and the Go compiler wants none of it.

`terraform` and `deploy` are excluded even though the EC2 deployment needs them. That is deliberate: the EC2 host gets its Compose file and Caddyfile from `user_data.sh.tftpl` written at boot, not from inside the image. The image contains *only* the two Go binaries.

`.github` holds CI workflows. Irrelevant to `go build`.

> **Why not exclude `.git` by default?** Docker does not. Many people ship 100 MB of git history into an image without noticing.

### Lines 8–13 — docs and container files

```dockerignore
# Docs and container orchestration files.
docs
*.md
Dockerfile
.dockerignore
docker-compose*.yml
```

`docs` is the whole site including this file — pure weight. `*.md` catches any README at the root.

`Dockerfile`, `.dockerignore`, and `docker-compose*.yml` are excluded because the daemon already has them separately; there is no reason to copy the build recipe *into* the thing it builds. The glob `docker-compose*.yml` covers `docker-compose.override.yml` too.

### Lines 15–19 — secrets

```dockerignore
# Local secrets and generated material.
.env
.env.*
*.pem
*.key
```

**This is the most important block in the file.** `.env` holds `ADMIN_API_KEY` and `SESSION_SECRET`. Because `COPY . .` on line 23 of the `Dockerfile` copies everything not excluded, an unlisted `.env` would be **baked into the image layer** — readable by anyone who runs `docker history` or pulls the image. Excluding it here means secrets arrive at runtime through `env_file` or SSM instead.

`.env.*` covers `.env.local`, `.env.production`, and similar. `*.pem` and `*.key` catch TLS material and private keys if you ever generate them locally.

> **Rule of thumb:** if a file can authenticate anything, it does not belong in an image. Images are copied, cached, pushed to registries, and scanned. Secrets belong in environment variables or a parameter store.

### Lines 21–32 — build output and local caches

```dockerignore
# Build output and local caches.
bin
build
dist
coverage
coverage.*
*.test
*.out
*.prof
*.log
tmp
node_modules
```

`bin` matters here specifically: the Terraform guide tells you to build the Lambda to `build/bootstrap`. Without `build` in this list, a stale local binary would be shipped into the context and then overwritten inside the image anyway — wasted upload.

`*.test` and `*.out` are Go test binaries and coverage output. `*.prof` is a CPU profile. `coverage.*` catches `coverage.out` and `coverage.html`. `node_modules` is defensive; this project has no JS toolchain, but the entry costs nothing.

`*.log` prevents your terminal history from ending up in an image layer.

### Lines 34–35 — tests

```dockerignore
# Tests are not needed for `go build`.
**/*_test.go
```

The pattern `**/` matches at any depth, so this catches `internal/limiter/limiter_test.go` and every other `_test.go` file.

Tests are excluded because the build stage runs `go build`, not `go test`. Note the trade-off: the Docker build will **not** compile your tests, so a test file with a syntax error builds a fine image. CI runs the tests separately — that is where they belong.

### Lines 37–42 — editor and OS noise

```dockerignore
# Editor / OS noise (including the stray Windows `nul` file).
.idea
.vscode
.DS_Store
Thumbs.db
nul
```

`.idea` and `.vscode` are JetBrains and VS Code project directories. `.DS_Store` is macOS Finder metadata. `Thumbs.db` is the Windows thumbnail cache.

`nul` is a Windows-specific curiosity: `nul` is a reserved device name, so `something > nul` creates a file that normal tools struggle to delete, and it can end up in a repo. The comment explains why it is listed explicitly rather than by a pattern.

### Line 44 — the one that is a comment, not a rule

```dockerignore
# Keep: go.mod, go.sum, cmd/, internal/, web/ (embedded assets).
```

This is documentation. It states the allow-list that remains after everything above is removed: the module files, the two command packages, the internal packages, and `web/`.

`web/` is the one people trip over. `web/static/app.css` and `web/templates/*.html` are compiled into the binary with Go's `embed` package, so they must exist at build time. Exclude `web/` and the dashboard ships with no CSS.

### Try it yourself

See what Docker would actually send:

```bash
# Total context size after .dockerignore is applied
docker build -f Dockerfile --target builder -t rateguard:ctx-test . 2>&1 | grep -i "transferring context"

# Or inspect without building
docker run --rm -v "/$PWD:/src:ro" alpine sh -c \
  'cd /src && tar cf - --exclude-from=.dockerignore . | wc -c'
```

---

## Part 3 — `Dockerfile`, line by line

### Lines 1–8 — syntax directive and global build arguments

```dockerfile
# syntax=docker/dockerfile:1

ARG VERSION=dev
ARG COMMIT=none
```

Line 1 is the **syntax directive**. It tells BuildKit to pull `docker/dockerfile:1` from Docker Hub and use that parser. It must be the first line (comments aside). You get newer Dockerfile features and better caching than the parser built into old Docker daemons.

Lines 4 and 8 declare **global** build arguments with defaults. They are not available inside a build stage until re-declared there — see line 13.

These two values exist so a built image can report which code it contains. The `Dockerfile` injects them into the Go binary at link time, and the dashboard footer shows `v{{.Version}}`.

### Line 11 — the builder stage

```dockerfile
FROM golang:1.27-bookworm AS builder
```

`FROM` starts a stage. `AS builder` names it so a later stage can copy from it.

`golang:1.27-bookworm` is the official Go image on Debian 12 "bookworm". The full toolchain lives here — compiler, standard library. This image is roughly 800 MB, which is exactly why the next stage throws it away.

> **If the tag does not resolve:** check `go.mod` for the actual Go version and match it. A Go toolchain older than the `go` directive refuses to build.

### Lines 13–14 — re-declaring arguments inside the stage

```dockerfile
ARG VERSION
ARG COMMIT
```

Build arguments declared before the first `FROM` have **global scope**. To use one inside a stage you redeclare it there without a default, which inherits the global value (or the `--build-arg` override).

Forgetting this is the classic bug: the build succeeds but the version string stays `dev`.

### Line 16 — working directory

```dockerfile
WORKDIR /src
```

`WORKDIR` sets the directory for all following `RUN`, `COPY`, and `ENTRYPOINT` instructions, and creates it if missing. Prefer `WORKDIR` over `RUN cd`, because `cd` inside one `RUN` does not persist to the next layer.

### Lines 18–20 — the dependency layer

```dockerfile
# Dependency layer: only invalidated when the module files change.
COPY go.mod go.sum ./
RUN go mod download
```

This is the single most important performance decision in the file.

Only two files are copied, then dependencies are downloaded. Because `go.mod` and `go.sum` change far less often than source code, this layer stays cached across most builds. A one-line code change reuses the download layer entirely.

Reverse the order — copy all source first, then `go mod download` — and every source edit re-downloads every module. On a slow connection that turns a 5-second rebuild into two minutes.

`go.sum` is copied alongside because it holds the expected hashes; `go mod download` verifies against it.

### Line 23 — the source layer

```dockerfile
COPY . .
```

Copies the build context (already filtered by `.dockerignore`) into `/src`. This is the layer that changes on nearly every build, which is correct — it is last before the compile.

### Lines 25–35 — compiling both binaries

```dockerfile
RUN mkdir -p /out \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/rateguard ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build \
      ... \
      -o /out/demo ./examples/quote
```

One `RUN` with `&&`-chained commands, so both binaries land in a single layer. Splitting them into two `RUN` instructions would add a layer and an extra filesystem snapshot for no benefit.

| Piece | What it does |
|---|---|
| `mkdir -p /out` | Creates the output directory; `-p` means "don't fail if it exists" |
| `CGO_ENABLED=0` | Disables cgo. The binary becomes fully static with no libc dependency, which is what makes the distroless base possible |
| `GOOS=linux` | Target OS. Explicit, so building on a Windows or macOS host still produces a Linux binary |
| `-trimpath` | Strips absolute filesystem paths from the binary. Removes build-machine paths, slightly smaller output, reproducible builds |
| `-buildvcs=false` | Skips embedding VCS metadata. Go 1.18+ tries to stamp git state, which fails or bloats builds when `.git` is absent — and `.dockerignore` excludes it |
| `-ldflags "-s -w"` | `-s` omits the symbol table, `-w` omits DWARF debug info. Smaller binary, no debugger support |
| `-X main.version=...` | Link-time variable assignment. Sets `version` and `commit` in the `var` block at `cmd/server/main.go:26-29` |
| `-o /out/rateguard` | Output path |
| `./cmd/server` | Package to build — the API, proxy, and dashboard |

`examples/quote` is built the same way into `/out/demo`: a tiny upstream that returns a random quote, used as the rate-limit target in local demos.

Note that no `go mod tidy` or `go get` runs here. `go.mod` and `go.sum` are treated as authoritative, which keeps builds reproducible.

### Line 38 — the runtime stage

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
```

A second `FROM` starts a **new stage and discards the builder**. Nothing from `golang:1.27-bookworm` carries over except what you explicitly `COPY --from=builder`.

`distroless/static-debian12` contains a static-linked binary's entire runtime needs: glibc, `ca-certificates`, a `tzdata` set, `/etc/passwd` entries. It has **no shell, no package manager, no `curl`, no `wget`**. The `:nonroot` tag sets the default user to UID 65532.

Why this matters:

- **Attack surface.** No shell means no `docker exec -it api sh` for an attacker to explore.
- **Size.** Roughly 2 MB on top of your binaries, versus 800 MB for the Go image.
- **Compliance.** Fewer packages means fewer CVEs to scan and patch.

The cost: you cannot debug inside the container with shell tools. That trade-off is why the `Dockerfile` adds a health check the binary can run itself — see line 64.

### Lines 43–51 — image labels

```dockerfile
LABEL org.opencontainers.image.title="RateGuard" \
      org.opencontainers.image.source="https://github.com/..." \
      org.opencontainers.image.version="${VERSION}" \
      ...
```

`LABEL` writes key-value metadata into the image config. `org.opencontainers.image.*` is the standard OCI namespace, so registries and tools like `docker scout` or GitHub Container Registry can display them.

Each label is one line joined by `\`, which is style only — they all become one layer instruction. These labels do not affect runtime at all.

### Line 53 — runtime working directory

```dockerfile
WORKDIR /app
```

The binaries land in `/app`, so this makes relative paths and the default working directory predictable. Note the Go app reads no files from disk at runtime: `web/` assets are embedded.

### Lines 55–56 — copying the compiled binaries

```dockerfile
COPY --from=builder --chown=65532:65532 /out/rateguard /app/rateguard
COPY --from=builder --chown=65532:65532 /out/demo /app/demo
```

`--from=builder` reads from the earlier stage's filesystem, not from the build context.

`--chown=65532:65532` sets owner and group to the nonroot UID/GID, matching the base image. Without it the files would be owned by root, and a nonroot process could not overwrite them if it ever needed to.

Only the two binaries cross over. The Go toolchain, module cache, and all source are left behind in the discarded stage.

### Line 59 — dropping privileges

```dockerfile
USER 65532:65532
```

Sets the user and group for `RUN`, `ENTRYPOINT`, and `CMD`. Because the base image is already `nonroot`, this is explicit rather than strictly required — but explicit is better here, because the Compose file also sets `security_opt: no-new-privileges` and `cap_drop: ALL`, and all three should agree.

### Line 61 — declaring the port

```dockerfile
EXPOSE 8080
```

Documentation, not enforcement. `EXPOSE` does not publish anything; it tells tools and readers which port the container listens on. `docker run -P` uses it to pick a random host port, and Compose uses it for `expose` semantics.

### Lines 64–65 — the self-probe health check

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/app/rateguard", "-healthcheck"]
```

| Flag | Meaning |
|---|---|
| `--interval=30s` | Run the probe every 30 seconds |
| `--timeout=5s` | Kill it if it takes longer than 5 seconds |
| `--start-period=20s` | Failures during the first 20 seconds don't count against `retries` — grace for startup |
| `--retries=3` | Three consecutive failures mark the container `unhealthy` |

The command is `rateguard -healthcheck`, which `cmd/server/main.go:32` defines. It probes the local `/healthz` and exits 0 or 1.

**Why not `CMD curl localhost:8080/healthz`?** Distroless has no `curl` and no `wget`. Shipping a shell and a HTTP client just to run a health check would undo the entire point of the minimal base image. Letting the binary probe itself costs a few lines of Go and keeps the image clean.

The JSON-array form (`CMD ["...", "..."]`) matters: it runs the binary directly with no shell involved, so no shell parsing and no extra process to hang.

### Line 67 — the entrypoint

```dockerfile
ENTRYPOINT ["/app/rateguard"]
```

The default executable. `docker run image -healthcheck` appends arguments to it. There is no `CMD`, so no default arguments are baked in — the container runs the server on `PORT` (default `8080`).

The Compose `demo` service overrides this with `entrypoint: ["/app/demo"]`, reusing the same image for a different program. That is why one image serves both containers.

---

## Part 4 — `docker-compose.yml`, line by line

### Lines 1–8 — the header comment

```yaml
# RateGuard local stack: Caddy -> api -> demo.
#
# Only Caddy publishes ports. api:8080 and demo:8090 stay on the private
# Compose network. Secrets come from .env via env_file; the environment
# block below pins local defaults but allows .env to override them...
```

Not decoration — this records three non-obvious decisions you would otherwise rediscover the hard way: the traffic path, the port policy, and the precedence rule between `env_file` and `environment`.

### Line 10 — the top-level key

```yaml
services:
```

Compose's top level has `services`, `volumes`, `networks`, `configs`, and `secrets`. Note there is **no `version:` key**. Compose v2 dropped it as obsolete; including it prints a warning.

### Lines 11–19 — building the API image

```yaml
  api:
    build:
      context: .
      args:
        VERSION: ${VERSION:-dev}
        COMMIT: ${COMMIT:-none}
    image: rateguard:${IMAGE_TAG:-local}
    env_file:
      - .env
```

`build.context: .` means "use the current directory as the build context", which is where `.dockerignore` applies. `args` feeds the two `ARG`s declared in the `Dockerfile`.

`${VERSION:-dev}` is Compose interpolation with a default: use the shell/`.env` value of `VERSION`, or `dev` if unset or empty. The `:-` form treats empty as unset; a bare `-` would only apply when the variable is missing entirely.

`image:` names the result. With both `build` and `image` present, Compose builds and then tags as `rateguard:local` (unless `IMAGE_TAG` overrides).

`env_file: - .env` reads `.env` and injects every line as an environment variable in the container. This is separate from interpolation: interpolation happens on your machine while parsing the YAML; `env_file` happens inside the container.

### Lines 20–27 — the environment block

```yaml
    environment:
      PORT: "8080"
      STORE_BACKEND: ${STORE_BACKEND:-memory}
      PROXY_UPSTREAM_URL: ${PROXY_UPSTREAM_URL:-http://demo:8090}
      DEMO_ENABLED: ${DEMO_ENABLED:-true}
      COOKIE_SECURE: ${COOKIE_SECURE:-false}
      TRUST_PROXY_HEADERS: ${TRUST_PROXY_HEADERS:-true}
```

**Precedence:** when a variable appears in both `env_file` and `environment`, `environment` wins. So this block does not override `.env` — it uses `${VAR:-default}` to supply a value *only when `.env` did not provide one*. That is exactly what the header comment describes, and it is why the same file works for a local run and for EC2: on EC2, `.env` sets `STORE_BACKEND=dynamodb` and that value flows through.

`PORT` is quoted because YAML would otherwise read `8080` as a number, and environment values must be strings.

`PROXY_UPSTREAM_URL` defaults to `http://demo:8090` — the Compose DNS name for the demo service. This is the clearest illustration of service-name resolution.

`COOKIE_SECURE=false` because you are on `http://localhost`. Behind HTTPS it must be `true`, or the browser drops the session cookie.

`TRUST_PROXY_HEADERS=true` because Caddy is the only ingress. It makes the app believe `X-Forwarded-For` instead of rewriting every client IP to Caddy's container address. **Only safe behind a proxy you control** — enabling it on a directly exposed app lets clients spoof their IP.

### Lines 28–33 — expose and dependency ordering

```yaml
    expose:
      - "8080"
    depends_on:
      demo:
        condition: service_started
```

`expose` keeps port 8080 reachable **inside the Compose network only**. Nothing is published to your machine. Compare with Caddy's `ports`, which does publish.

`depends_on` with `condition: service_started` waits until the `demo` container has started — not until it is healthy. The comment on line 32 explains why: `demo` has no health check because distroless has no `curl`, so there is no health status to wait for. Without an explicit condition, Compose only guarantees start order.

> `depends_on` is about **start order**, not about the dependency staying alive. If `demo` crashes later, `api` keeps running and returns 502s.

### Lines 34–39 — the health check

```yaml
    healthcheck:
      test: ["CMD", "/app/rateguard", "-healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 20s
```

Duplicates the `Dockerfile`'s `HEALTHCHECK` so the policy is visible in one place and can be tuned per environment without a rebuild. Compose overrides the image's value here.

The `CMD` form runs the command inside the container. Again, no shell — which is required, because there isn't one.

Caddy's `depends_on: api: condition: service_healthy` depends on this reporting healthy.

### Line 40 — restart policy

```yaml
    restart: unless-stopped
```

Restart if the process exits for any reason, **unless a human stopped it**. That last clause distinguishes it from `always`: `docker stop` or `docker compose down` keeps the container down. This is what you want for a local stack you intend to leave running.

### Lines 41–42 — shutdown grace

```yaml
    # Give the usage flusher time to drain on SIGTERM.
    stop_grace_period: 30s
```

On `docker compose down`, Docker sends `SIGTERM` and waits this long before escalating to `SIGKILL`. RateGuard batches usage writes and flushes on shutdown; the default 10-second grace can cut a large batch off mid-write. Thirty seconds covers it.

Getting a `SIGKILL` here means lost usage records, not corruption — the buckets are in memory anyway.

### Lines 43–49 — hardening

```yaml
    read_only: true
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=16m
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
```

Four independent controls, each closing a different door:

- **`read_only: true`** mounts the container's root filesystem read-only. A process that gets compromised cannot write to `/app`, cannot drop a binary somewhere and execute it, cannot tamper with files.
- **`tmpfs: /tmp:rw,noexec,nosuid,size=16m`** carves out the one directory that must be writable, in RAM, never touching disk. `noexec` blocks executing anything placed there; `nosuid` blocks setuid binaries; `size=16m` caps it so a runaway process cannot exhaust host memory. Go's runtime needs a writable temp dir, which is why this exists at all.
- **`no-new-privileges:true`** blocks `setuid`/`file capabilities` from granting new privileges. Even root inside the container cannot escalate further.
- **`cap_drop: ALL`** removes every Linux capability. The container keeps no special kernel permissions — no `NET_ADMIN`, no `SYS_ADMIN`, no ability to mount or change network config.

Together these mean a container breakout requires defeating the kernel with no capabilities and no writable filesystem. For a local demo that is arguably thorough; the settings are copied from the EC2 user data, which faces the internet.

### Lines 50–54 — resource limits

```yaml
    deploy:
      resources:
        limits:
          cpus: "0.75"
          memory: 384M
```

`deploy.resources.limits` is honored by Compose v2 (it migrated from the older `mem_limit`/`cpus` keys). Three quarters of a CPU and 384 MB is ample for a Go proxy and prevents one container from starving the rest of your machine.

The limits are deliberately uneven: `api` gets 0.75 CPU / 384 MB, while `demo` and `caddy` get 0.25 / 128 MB each. The API is doing the work.

### Lines 56–84 — the demo service

```yaml
  demo:
    build:
      context: .
      args:
        VERSION: ${VERSION:-dev}
        COMMIT: ${COMMIT:-none}
    image: rateguard:${IMAGE_TAG:-local}
    entrypoint: ["/app/demo"]
```

Same `build` block and same resulting image tag as `api`. The only functional difference is `entrypoint`, which replaces the image's `ENTRYPOINT ["/app/rateguard"]` with `/app/demo`.

This is the payoff of building both binaries into one image: Compose builds once and starts two containers from it, instead of maintaining two Dockerfiles.

```yaml
    environment:
      PORT: "8090"
    expose:
      - "8090"
    healthcheck:
      disable: true
```

Port 8090 to match the upstream URL. No `env_file`, because the quote demo needs no secrets — it has no admin key, no session store, no AWS access. That is a nice property: the demo container cannot leak credentials it does not have.

`healthcheck: disable: true` overrides the image-level `HEALTHCHECK` (which points at `/app/rateguard` and would fail here, since this container runs `/app/demo`).

```yaml
    read_only: true
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=8m
```

Same hardening, smaller tmpfs because the demo does nothing but return strings.

### Lines 86–108 — Caddy, the only public service

```yaml
  caddy:
    image: caddy:2.8-alpine
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
```

A pinned upstream image rather than a local build. `ports` uses `HOST:CONTAINER` — these are the **only** ports reachable from your machine.

`443/udp` alongside `443/tcp` enables HTTP/3 (QUIC), which runs over UDP. Without this line HTTP/2 over TCP still works; you just don't get h3.

Pinning `caddy:2.8-alpine` instead of `caddy:latest` means a rebuild in six months gets the same Caddy, not a surprise major version.

```yaml
    volumes:
      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
```

Three mounts with different jobs:

- A **bind mount** of the repo's `deploy/Caddyfile` into the container, `:ro` read-only. Edit the file on your host, restart Caddy, config changes. No image rebuild.
- Two **named volumes** (`caddy_data`, `caddy_config`) declared at lines 110–112. These hold ACME certificates and account keys. Because they are named volumes rather than bind mounts, they survive `docker compose down` and even deletion of the containers — you do not re-request certificates (and risk Let's Encrypt rate limits) every time you tear down.

```yaml
    depends_on:
      api:
        condition: service_healthy
```

Caddy starts only once the API reports healthy. Prevents a window where Caddy accepts traffic and returns 502s because the backend is still booting.

### Line 107 — the deliberate exception

```yaml
    # Intentionally no cap_drop: the official image binds :80/:443.
```

Worth understanding rather than copying blindly. Caddy needs to bind privileged ports 80 and 443. Running as root inside its container, it binds them and then drops privileges for actual request handling.

An alternative would be `setcap 'cap_net_bind_service=+ep' /usr/bin/caddy` with `cap_drop: ALL` plus `cap_add: [NET_BIND_SERVICE]`, which is tighter. The comment marks this as a known, accepted gap instead of leaving a reader to wonder whether it was an oversight.

### Lines 110–112 — named volumes

```yaml
volumes:
  caddy_data:
  caddy_config:
```

Declaring the two volumes referenced above. Compose creates them with a project prefix, such as `rateguard_caddy_data`.

Leaving the definitions empty means "use the defaults" — the local driver, host storage managed by Docker.

---

## Part 5 — The whole request path

Putting the three files together:

```
your browser
    |
    |  localhost:80          <- docker-compose.yml `ports` (only published ports)
    v
+-------------------+
| caddy             |        <- image: caddy:2.8-alpine, config from ./deploy/Caddyfile
| TLS, HSTS, gzip   |
+--------+----------+
         |  api:8080              <- Compose network, service-name DNS
         v
+-------------------+               reads env from .env + environment block
| api               |               runs /app/rateguard   <- Dockerfile ENTRYPOINT
| proxy + dashboard |               health: /app/rateguard -healthcheck
+--------+----------+
         |  http://demo:8090      <- PROXY_UPSTREAM_URL default
         v
+-------------------+
| demo              |               runs /app/demo        <- same image, entrypoint overridden
| quote upstream    |
+-------------------+
```

Build order and what each file contributed:

| Step | File | Effect |
|---|---|---|
| 1 | `.dockerignore` | Context sent to the daemon excludes `.git`, `.env`, `docs`, `terraform`, tests |
| 2 | `Dockerfile` L11–20 | Builder stage; dependencies cached separately from source |
| 3 | `Dockerfile` L25–35 | Two static Linux binaries compiled into `/out` |
| 4 | `Dockerfile` L38–59 | New stage; only the binaries cross over, as UID 65532 |
| 5 | `docker-compose.yml` | Builds once, runs `api` and `demo` from it, fronts both with Caddy |

---

## Part 6 — Change it yourself

Common edits and where to make them.

**Give the API more memory.** `docker-compose.yml`, the `api` service's `deploy.resources.limits.memory`. No rebuild needed.

**Change the demo upstream.** `docker-compose.yml`, `api` service, `PROXY_UPSTREAM_URL`. Must be a name reachable on the Compose network — either a service name or a URL you can reach from inside a container.

**Add a build-time version stamp.**

```bash
VERSION="$(git describe --tags --always)" COMMIT="$(git rev-parse --short HEAD)" \
  docker compose up -d --build
```

Flows through `${VERSION}` → Compose `build.args` → `Dockerfile` `ARG` → `-ldflags -X` → `main.version` → the dashboard footer.

**Publish the API directly for debugging.** Add to the `api` service:

```yaml
    ports:
      - "8080:8080"
```

Understand the consequence first: this bypasses Caddy's TLS and security headers, and exposes the API to your whole network. Remove it before you finish.

**Run a shell inside the API container.** You cannot — distroless has no shell. Use the binary's own probe instead:

```bash
docker compose exec api /app/rateguard -healthcheck
```

To get an interactive debugging environment, add a temporary stage to the `Dockerfile`:

```dockerfile
FROM golang:1.27-bookworm AS debug
WORKDIR /src
COPY . .
RUN go build -o /app/rateguard ./cmd/server
ENTRYPOINT ["/bin/bash"]
```

Then `docker build --target debug -t rateguard:debug .` and `docker run -it rateguard:debug`. Keep this stage out of your production path — it exists only for poking around.

**Speed up repeated builds.** Make sure `.dockerignore` still excludes `.git` and `docs`, and never reorder the `COPY go.mod go.sum` pair below `COPY . .`. Those two account for most rebuild time.

---

## Part 7 — Things that will bite you

**Editing code and seeing no change.** You are looking at a stale image. `docker compose up -d` does not rebuild. Use `docker compose up -d --build`.

**`401` from the dashboard with the right key.** The admin key in `.env` may have a trailing `\r` if the file was written by PowerShell on Windows. Git Bash: pipe through `tr -d '\r'`. Better: rewrite `.env` with LF line endings.

**`could not create tenant` and an empty `.env`.** The API needs `ADMIN_API_KEY` of at least 16 characters; `SESSION_SECRET` needs 32 if set. Compose interpolates from `.env`, and `env_file` also loads it — a missing file fails loudly, but a placeholder value fails at startup.

**Container restarts in a loop.** Check the exit reason:

```bash
docker compose logs --tail=100 api
docker inspect --format '{{.State.ExitCode}} {{.State.Error}}' "$(docker compose ps -q api)"
```

Config validation failures exit immediately with a message. A crash loop with no output usually means the binary cannot bind `PORT` or the upstream URL is malformed.

**`port is already allocated`.** Something on your host holds port 80. Find it with `Get-NetTCPConnection -LocalPort 80 -State Listen` on PowerShell, or change Caddy's mapping to `"8080:80"` and use `http://localhost:8080`.

**Caddy serves HTTPS but the dashboard won't stay logged in.** `COOKIE_SECURE` must be `true` over HTTPS and `false` over plain HTTP. Mismatch in either direction breaks the session. Local `localhost` should stay `false` — browsers treat `localhost` as a secure context, so the cookie works anyway.

**Image too large after adding files.** Check what escaped `.dockerignore`:

```bash
docker history rateguard:local --no-trunc
```

A layer containing your `.env` or `.git` means an exclusion is missing.