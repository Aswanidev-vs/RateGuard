# RateGuard Terraform, File by File

Companion to [`TERRAFORM-GUIDE.md`](TERRAFORM-GUIDE.md). That guide tells you which commands to run. This one explains **what each file and each line actually does**, so you can change the infrastructure yourself.

Read this next to the real files in [`terraform/`](../../terraform/):

| File | Lines | Owns |
|---|---|---|
| `versions.tf` | 36 | Terraform version, providers, the second region |
| `backend.tf` | 6 | Where state lives (filled in at `init`) |
| `main.tf` | 49 | Data sources, generated secrets, `locals` |
| `variables.tf` | 106 | Every input and its validation |
| `network.tf` | 84 | VPC, subnet, routing, security group |
| `ec2.tf` | 34 | The instance and its boot script |
| `user_data.sh.tftpl` | 136 | What the instance does on first boot |
| `iam.tf` | 243 | Three roles: EC2, Lambda, GitHub Actions |
| `dynamodb.tf` | 81 | Three tables and one index |
| `ecr.tf` | 32 | Image registry and retention |
| `s3.tf` | 32 | Artifacts bucket |
| `ssm.tf` | 37 | Secrets and config parameters |
| `sns.tf` | 45 | Alert topics and the billing alarm |
| `lambda.tf` | 51 | Cost detector and its hourly trigger |
| `observability.tf` | 4 | Lambda log group |
| `outputs.tf` | 43 | Values printed after `apply` |
| `bootstrap/` | 111 | The one-time state bucket and lock table |

---

## Part 1 — The mental model

Six ideas explain almost every line you will read.

**1. Terraform is a state machine, not a script.**
It keeps a `terraform.tfstate` file mapping your configuration to real AWS resource IDs. On each run it compares three things: what you wrote, what the state says exists, and what AWS actually has. Then it proposes a plan to reconcile them. This is why you always `plan` before `apply`, and why editing the state by hand is a bad idea.

**2. A `resource` block declares one thing to create.**

```hcl
resource "aws_vpc" "main" {
  cidr_block = "10.20.0.0/16"
}
```

`aws_vpc` is the **resource type**, `main` is the **local name**. Together they form the address `aws_vpc.main`, which is how other blocks refer to it. The name `main` has no effect on AWS — the real name comes from `tags` or from the resource's own naming rules.

**3. A `data` block reads something that already exists.**

```hcl
data "aws_ami" "al2023" { ... }
```

No creation, no destruction. It fetches a value at plan time and makes it available as `data.aws_ami.al2023.id`. Used for things you don't manage: the Amazon Linux AMI, your own AWS account ID, the list of availability zones.

**4. References create implicit dependencies.**
When one block mentions another's attribute — `vpc_id = aws_vpc.main.id` — Terraform infers an ordering edge. That is why `terraform graph` produces a DAG rather than a list. `depends_on` exists for the cases where the reference is not visible in the arguments, such as "the Lambda needs its log group to exist before it starts writing."

**5. Providers are per-region, and you can have several.**
The `aws` provider defaults to `var.aws_region` (`ap-south-1`). A second aliased provider pins `us-east-1` because AWS billing metrics exist **only** there. Any resource that must live in us-east-1 carries `provider = aws.us_east_1`.

**6. `locals` compute values once; `variables` accept them.**
`variable` is an input with a type and optional validation. `locals` is a named expression evaluated during plan — no input, no validation, just a place to avoid repeating yourself.

---

## Part 2 — `versions.tf`: constraints and providers

```hcl
terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws     = { source = "hashicorp/aws",     version = "~> 5.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.4" }
    random  = { source = "hashicorp/random",  version = "~> 3.5" }
  }
}
```

`required_version` is the Terraform **CLI** version. `>= 1.7.0` is a floor, not a pin — newer CLIs work.

`required_providers` pins the plugins:

- `hashicorp/aws` — every `aws_*` resource. `~> 5.0` means **≥ 5.0, < 6.0**. The pessimistic constraint lets AWS release 5.x patches and features without breaking you, but blocks a breaking 6.0.
- `hashicorp/archive` — used once, to zip the Lambda binary (`data.archive_file`).
- `hashicorp/random` — generates the session secret and the S3 name suffix.

`source` is the registry address: `hashicorp/aws` means "the `aws` provider from the `hashicorp` namespace". Omitting `source` and writing just `aws` still works but is deprecated.

### The two providers

```hcl
provider "aws" {
  region = var.aws_region

  default_tags {
    tags = local.tags
  }
}
```

The primary provider. `default_tags` applies `local.tags` to **every** resource this provider creates, so you never tag things individually for the common keys. It merges with per-resource `tags`, which win on conflict.

```hcl
# AWS billing metrics and billing alarms exist only in us-east-1.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"

  default_tags {
    tags = local.tags
  }
}
```

The same provider with `alias = "us_east_1"`. Now two configurations exist: the default, and `aws.us_east_1`.

Without the alias, Terraform would error — a provider block without an alias is the default, and you can only have one. Resources opt in with `provider = aws.us_east_1`.

**Why us-east-1 at all?** `AWS/Billing/EstimatedCharges` is published only in that region. It is an AWS quirk, not a choice. The comment on line 22 records the reason so nobody "fixes" it later.

---

## Part 3 — `backend.tf`: where state lives

```hcl
terraform {
  backend "s3" {}
}
```

Deliberately empty. The real values arrive at `init` time:

```bash
terraform init -backend-config=backend.hcl
```

`backend.hcl` (gitignored) supplies `bucket`, `key`, `region`, `encrypt`, and `dynamodb_table`.

**Why the empty block?** Two reasons. First, the bucket name is generated with a random suffix by the bootstrap stack, so it cannot be written into the config ahead of time. Second, keeping the values in a gitignored file means the state location is not committed to the repo.

State is not optional documentation — it is the record of what exists. Losing it means Terraform offers to recreate everything, which for DynamoDB tables with data is a disaster. The S3 bucket from `bootstrap/` has versioning enabled, so a corrupted or deleted state file can be recovered from a previous version.

---

## Part 4 — `main.tf`: data sources, generated secrets, locals

### Lines 1–4 — asking AWS about itself

```hcl
data "aws_caller_identity" "current" {}
data "aws_availability_zones" "available" {
  state = "available"
}
```

`aws_caller_identity.current.account_id` returns the 12-digit account number. Used later to build IAM ARNs without hard-coding an account ID that differs per environment.

`aws_availability_zones.available` lists AZs in the region, filtered to `state = "available"` so Terraform never picks one that is still initializing. The subnet uses `names[0]` — the first available AZ.

### Lines 6–14 — the Amazon Linux AMI

```hcl
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-x86_64"]
  }
}
```

`most_recent = true` picks the newest match. `owners = ["amazon"]` restricts to official Amazon images — without it, any account could publish an AMI matching the name pattern and you would boot someone else's machine image.

The filter matches names like `al2023-ami-2023.6.20250218-kernel-6.1-x86_64`. `x86_64` is deliberate: `t3.micro` is Intel/AMD, so a Graviton AMI would not boot.

### Lines 16–20 — the generated session secret

```hcl
resource "random_password" "session" {
  count   = var.session_secret == null ? 1 : 0
  length  = 48
  special = false
}
```

The `count` trick: create this resource **only if** the operator did not supply a secret. `var.session_secret == null ? 1 : 0` evaluates to 1 or 0, and `count = 0` means the resource does not exist.

`special = false` restricts output to alphanumerics. That matters because this value travels into an SSM `SecureString`, into a `.env` heredoc on EC2, and into an HMAC key. Quotes or `$` would risk breaking one of those handoffs.

### Line 18 in `locals` — resolving which secret to use

```hcl
session_secret = coalesce(var.session_secret, one(random_password.session[*].result))
```

`coalesce` returns its first non-null argument. If the operator provided one, use it; otherwise use the generated one.

`random_password.session[*].result` uses the splat operator on a resource with `count`, producing a list — `["Ab3x..."]` or `[]`. `one()` asserts the list has exactly one element and unwraps it. When `count = 0`, `one()` returns `null`, so `coalesce` never reaches it.

Without `one()`, you would have to index `[0]` and crash on the empty list.

### Lines 20–27 — tags

```hcl
locals {
  name = "${var.project}-${var.environment}"

  tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
    Repository  = "Aswanidev-vs/RateGuard"
  }
  ...
}
```

`local.name` becomes `rateguard-dev`, the prefix for most resource names. Changing `var.environment` to `prod` gives you `rateguard-prod` everywhere with one edit.

`local.tags` feeds `default_tags` in the provider, so these four keys land on every taggable resource. `ManagedBy = "terraform"` is the standard signal to a future operator that this should not be hand-edited.

### Lines 29–31 — table names

```hcl
  tenants_table   = "api_guard_tenants"
  usage_table     = "api_guard_usage"
  anomalies_table = "api_guard_cost_anomalies"
```

Declared once and referenced by the DynamoDB resources, the EC2 user data, the Lambda environment, and the IAM policies. Rename a table here and all five places follow.

The names are hard-coded rather than derived from `local.name` because the Lambda, the running app, and IAM all need to agree, and a per-environment prefix would multiply the places that must stay in sync.

### Lines 33–35 — HTTPS implied by a domain name

```hcl
  cookie_secure = var.domain_name != "" ? "true" : "false"
  caddy_domain  = var.domain_name != "" ? var.domain_name : "localhost"
```

Two consequences of one decision. Set `domain_name` and you get HTTPS, `COOKIE_SECURE=true`, and Caddy requests a real certificate. Leave it empty and you get plain HTTP on the IP with `COOKIE_SECURE=false`.

These are strings, not bools, because they are interpolated into the `.env` heredoc in the user data template, where everything is text.

### Lines 37–40 — the OIDC provider ARN

```hcl
  github_oidc_provider_arn = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:oidc-provider/token.actions.githubusercontent.com"
  github_repo              = "Aswanidev-vs/RateGuard"
```

Constructed rather than looked up, because the GitHub OIDC provider is created **once per AWS account** by the operator following the guide — not by this stack. Building the ARN from the caller identity means the config works in any account without editing.

`local.github_repo` is used in the trust policy condition. Change both this and the repo name together if you fork.

---

## Part 5 — `variables.tf`: inputs and guardrails

Every input has a `description` (surfaced by `terraform-docs` and in `terraform plan` errors), a `type`, and usually a `default`. Several add `validation` blocks.

### The two secrets

```hcl
variable "admin_api_key" {
  type      = string
  sensitive = true

  validation {
    condition     = length(var.admin_api_key) >= 16
    error_message = "admin_api_key must be at least 16 characters."
  }
}
```

`sensitive = true` does two things: Terraform redacts the value in plan output (`(sensitive value)`), and it must be explicitly unwrapped to use in another output. Without it, `terraform plan` would print your admin key to the terminal — and terminals get logged, screenshotted, and pasted into tickets.

The validation is not arbitrary. `internal/auth` rejects keys shorter than 16 characters at startup, so this fails at `plan` with a clear message instead of producing a stack whose app crash-loops.

```hcl
variable "session_secret" {
  type    = string
  default = null

  validation {
    condition     = var.session_secret == null || length(var.session_secret) >= 32
    error_message = "session_secret must be at least 32 characters when supplied."
  }
}
```

`default = null` is the signal that triggers the `random_password` generation in `main.tf`. The condition allows null (meaning "generate one") or a string of at least 32 characters.

### The sizing and naming inputs

| Variable | Default | Effect |
|---|---|---|
| `aws_region` | `ap-south-1` | Primary region for everything except billing |
| `project` | `rateguard` | First half of `local.name` |
| `environment` | `dev` | Second half; switch to `prod` for a parallel stack |
| `instance_type` | `t3.micro` | Free-tier eligible. `t3.small` if the dashboard feels slow |
| `ssm_prefix` | `/rateguard` | Where the app looks for its parameters |
| `app_image_tag` | `latest` | Tag pulled at first boot; `deploy.sh` overrides it |
| `log_retention_days` | `7` | Lambda log retention |
| `alarm_threshold_usd` | `1` | Billing alarm threshold |

### The three that change your security posture

```hcl
variable "allowed_ssh_cidr" {
  description = "Optional CIDR allowed to SSH. Empty disables inbound SSH entirely."
  type        = string
  default     = ""
}
```

Empty means no port 22 ingress rule at all — you reach the instance through SSM Session Manager using the instance profile, with no SSH keys and no open port. Set it to `"203.0.113.4/32"` to allow one IP.

```hcl
variable "deletion_protection" {
  description = "Enable DynamoDB deletion protection."
  type        = bool
  default     = false
}
```

Passed straight to `deletion_protection_enabled` on all three tables. `false` by default because this stack is designed to be torn down freely during learning. Set `true` before it holds anything you care about — then `terraform destroy` refuses until you disable it, which is exactly the point.

```hcl
variable "force_destroy_buckets" {
  description = "Allow Terraform to delete non-empty buckets on destroy."
  type        = bool
  default     = true
}
```

Normally `terraform destroy` fails on a non-empty S3 bucket. This lets it delete objects first. Convenient for a learning stack; dangerous in production, where a typo could erase artifacts you meant to keep.

---

## Part 6 — `network.tf`: the VPC

Four resources build a minimal public network.

### The VPC

```hcl
resource "aws_vpc" "main" {
  cidr_block           = "10.20.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags = { Name = "${local.name}-vpc" }
}
```

`10.20.0.0/16` gives 65 536 addresses — far more than one subnet needs, but a /16 is conventional and leaves room to add private subnets later.

`enable_dns_support` lets instances resolve the Route 53 private zone and, more importantly, the AWS-provided public DNS names. `enable_dns_hostnames` assigns instances a DNS name alongside their IP. Both are on by default in the default VPC; being explicit here matters because this is a **new** VPC, where neither is guaranteed.

### The internet gateway

```hcl
resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}
```

A one-to-one-per-VPC gateway that provides IPv4 internet access in both directions. It is the target of the public route table's default route.

### The subnet

```hcl
resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "10.20.1.0/24"
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = true
}
```

`10.20.1.0/24` is 256 addresses, carved from the VPC's /16.

`availability_zone = ... names[0]` pins it to one AZ. The data source guarantees the AZ exists and is available, so `apply` will not fail on an AZ that is not enabled in your account.

`map_public_ip_on_launch = true` auto-assigns a public IP to anything launched here. This is what makes the subnet "public" in practice — the CIDR alone does not do it. It is also what allows `associate_public_ip_address = true` on the instance to work.

### Routing

```hcl
resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}
```

`0.0.0.0/0` is "everything". Sending it to the internet gateway is the definition of a public subnet.

Note that creating a route table does **not** apply it. The separate `aws_route_table_association` binds it to the subnet. A VPC's route tables are not inherited; without this association the subnet has no default route and instances can neither reach the internet nor be reached.

### The security group

```hcl
resource "aws_security_group" "ec2" {
  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  ingress {
    description = "HTTPS"
    from_port   = 443
    ...
  }
```

A stateful virtual firewall. Inbound rules for 80 and 443 from anywhere, over both IPv4 and IPv6. `from_port`/`to_port` are a range; equal values mean a single port.

"Stateful" means return traffic is automatically allowed — you do not need an egress rule for responses to inbound connections.

### The conditional SSH rule

```hcl
  dynamic "ingress" {
    for_each = var.allowed_ssh_cidr == "" ? [] : [var.allowed_ssh_cidr]
    content {
      description = "Optional SSH"
      from_port   = 22
      to_port     = 22
      protocol    = "tcp"
      cidr_blocks = [ingress.value]
    }
  }
```

`dynamic` blocks generate repeated nested blocks at plan time. `for_each` iterates a collection: an empty list produces zero blocks, a one-element list produces one.

The expression `var.allowed_ssh_cidr == "" ? [] : [var.allowed_ssh_cidr]` converts an optional string into a list of zero or one. Inside `content`, `ingress.value` is the current element — the CIDR string itself.

This is the idiomatic way to express "this block exists only if the variable is set", and it produces **no port 22 rule at all** by default, rather than a rule with an empty CIDR that would mean "allow from nowhere" and read confusingly.

### Egress

```hcl
  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
```

`protocol = "-1"` means all protocols, and the 0–0 port range means all ports. Fully open outbound.

This is deliberate and necessary: the instance must reach ECR to pull images, SSM for parameters, DynamoDB, SNS, CloudWatch, and the Let's Encrypt ACME endpoints for TLS. Locking egress down would require enumerating all of those, including the AWS API address ranges, for very little gain on a single-instance demo.

---

## Part 7 — `ec2.tf` and `user_data.sh.tftpl`: the instance

### `ec2.tf` line by line

```hcl
resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.ec2.id]
  associate_public_ip_address = true
  iam_instance_profile   = aws_iam_instance_profile.ec2.name
```

Six arguments, six different inputs: a data source, a variable, two resource references, a bool, and one more resource reference. This block is the clearest example of how Terraform wires things together without explicit ordering.

`associate_public_ip_address = true` pairs with `map_public_ip_on_launch` on the subnet; both are set for clarity.

`iam_instance_profile` attaches the role from `iam.tf`. Without it, the app cannot read SSM parameters or write to DynamoDB, and `/readyz` returns 503.

### Rendering the user data

```hcl
  user_data_base64 = base64encode(templatefile("${path.module}/user_data.sh.tftpl", {
    aws_region      = var.aws_region
    ecr_repo_url    = aws_ecr_repository.app.repository_url
    image_tag       = var.app_image_tag
    ssm_prefix      = var.ssm_prefix
    tenants_table   = local.tenants_table
    usage_table     = local.usage_table
    anomalies_table = local.anomalies_table
    cookie_secure   = local.cookie_secure
    caddy_domain    = local.caddy_domain
  }))
```

`path.module` is the directory of the current module, so the path resolves regardless of where you run Terraform from.

`templatefile()` reads the file and substitutes `${...}` placeholders. The result must be base64-encoded because the EC2 API expects `UserData` as base64, with a 16 KB limit.

Only 9 values are passed, and every one is a non-secret. That is the key security property: the admin key and session secret are **not** in the user data. They are in SSM, read by the app at startup. User data is visible to anyone with `ec2:DescribeInstanceAttribute` and is stored unencrypted — so nothing sensitive belongs there.

### The root disk

```hcl
  root_block_device {
    volume_size = 10
    volume_type = "gp3"
    encrypted   = true
  }
```

10 GB of gp3. Amazon Linux 2023 plus Docker plus the Compose plugin fits comfortably. `encrypted = true` encrypts at rest with an AWS-managed key — no KMS key to manage, no cost.

### IMDSv2, enforced

```hcl
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
```

This is the single most valuable block in the file for security.

`http_tokens = "required"` forces **IMDSv2**, which is session-oriented. A caller must first request a token from a PUT endpoint and then present it. IMDSv1 just needed a GET to `169.254.169.254`.

That difference defeats Server-Side Request Forgery. Under v1, any code on the instance that can be tricked into fetching a URL can steal the role credentials. Under v2, an SSRF attacker would need to make a PUT with the right header, which most SSRF sinks cannot do.

`http_put_response_hop_limit = 1` is required for containers: the Docker daemon forwards requests, adding a network hop, and the default limit of 1 rejects anything from beyond the instance itself. Setting it to 1 allows exactly one hop — the container's request through the Docker bridge. Higher values would let nested containers reach the metadata service, widening exposure.

### What the boot script does

`user_data.sh.tftpl` runs as root on first boot. Reading it in order:

```bash
set -euo pipefail
exec > >(tee /var/log/rateguard-userdata.log) 2>&1
```

`-e` exits on any error, `-u` treats unset variables as errors, `-o pipefail` propagates failures through pipes. Together they stop the script from continuing after a broken step and leaving a half-configured host.

`exec > >(tee ...)` redirects all output through `tee` to a log file. This is why the guide can tell you to read `/var/log/rateguard-userdata.log` to debug a failed boot — without it, the output would exist only in the instance console.

```bash
dnf install -y docker jq curl
```

`dnf` is the AL2023 package manager. `-y` assumes yes, since no human is present.

```bash
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fsSL https://github.com/docker/compose/releases/download/v2.27.0/docker-compose-linux-x86_64 \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
systemctl enable --now docker
```

The Compose **plugin** is installed rather than the old standalone `docker-compose` v1. Placing the binary in `cli-plugins` makes it available as `docker compose` (with a space), which is what the compose file and guide assume. The version is pinned rather than pulled from a repo, so AL2023's package updates cannot change your Compose behaviour.

`curl -fsSL`: `-f` fails on HTTP errors instead of writing an error page, `-s` silences progress, `-S` still shows errors, `-L` follows redirects — GitHub release URLs redirect.

`systemctl enable --now docker` does both jobs: `enable` starts it at every boot, `--now` also starts it immediately.

```bash
aws ecr get-login-password --region ${aws_region} \
  | docker login --username AWS --password-stdin ${ecr_repo_url}
```

ECR has no static password. This fetches a 12-hour token from the instance metadata credentials and pipes it to `docker login`. `--password-stdin` avoids the token appearing in the process list.

**Note the `${...}` syntax.** These are Terraform template substitutions, resolved at `plan` time — `${aws_region}` becomes `ap-south-1` in the rendered file. Contrast with `$${IMAGE_TAG}` in the heredocs below, which is Terraform's escape for a literal `${...}` that should reach the shell or Compose untouched.

```bash
cat > .env <<'EOF'
APP_ENV=production
PORT=8080
STORE_BACKEND=dynamodb
AWS_REGION=${aws_region}
SSM_ENABLED=true
SSM_PARAM_PREFIX=${ssm_prefix}
...
IMAGE_TAG=${image_tag}
EOF
```

The quoted `<<'EOF'` prevents **shell** expansion, so `$` inside is literal — but Terraform still substitutes `${...}` before the file ever reaches bash. That is why Terraform variables work while shell variables do not.

`STORE_BACKEND=dynamodb` and `SSM_ENABLED=true` are what make this the AWS deployment rather than the local in-memory one. The app reads its keys from SSM at startup; the `.env` contains only **where** to look, never the secrets themselves.

```bash
cat > docker-compose.yml <<'EOF'
services:
  api:
    image: "${ecr_repo_url}:$${IMAGE_TAG}"
```

Compare this with the repo's `docker-compose.yml`. Differences that matter:

- `image:` points at ECR instead of a local build.
- No `build:` block — the instance consumes images, it does not create them.
- No `deploy.resources.limits` — the EC2 instance is single-purpose, so limits add nothing.
- No `env_file` referencing local paths; `.env` sits next to it in `/opt/rateguard`.
- No `cap_drop` on `caddy`, same reasoning as the local file.

`$${IMAGE_TAG}` renders as `${IMAGE_TAG}` in the written file, which Compose then interpolates from `.env`. That indirection is what lets `deploy.sh` change the running version by rewriting one line in `.env` — no Terraform, no re-render of user data.

```bash
cat > Caddyfile <<'EOF'
${caddy_domain} {
	encode zstd gzip
	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		-Server
	}
	reverse_proxy api:8080
}
EOF
```

`${caddy_domain}` resolves to either your domain or `localhost`. With a real domain, Caddy automatically obtains and renews a Let's Encrypt certificate; with `localhost` it serves plain HTTP. `encode zstd gzip` compresses responses, `-Server` removes the identifying response header, and the HSTS header tells browsers to only use HTTPS in future.

```bash
cat > deploy.sh <<'EOF'
#!/bin/bash
set -euo pipefail
cd /opt/rateguard
TAG="$${1:?usage: deploy.sh IMAGE_TAG}"
sed -i "s|^IMAGE_TAG=.*|IMAGE_TAG=$${TAG}|" .env
aws ecr get-login-password --region ${aws_region} | docker login --username AWS --password-stdin ${ecr_repo_url}
docker compose pull api demo
docker compose up -d --no-deps api demo
EOF
chmod +x deploy.sh
```

This script is what GitHub Actions invokes over SSM. `$${1:?usage...}` becomes `${1:?usage...}` — a shell parameter expansion that errors with the message if no argument is given.

`sed -i` rewrites the `IMAGE_TAG` line in `.env`. Then `docker compose pull` and `up -d` apply it. `--no-deps` prevents Compose from also touching `caddy`, so a deploy does not briefly drop the proxy.

```bash
for attempt in $(seq 1 30); do
  if docker compose pull api demo; then
    break
  fi
  echo "Image not available yet (attempt ${attempt}/30); retrying in 20s"
  sleep 20
done
docker compose up -d
```

Thirty attempts, 20 seconds apart — up to 10 minutes. This exists because of a chicken-and-egg problem: Terraform creates the ECR **repository**, but the **image** is pushed by CI or by you afterwards. On a first `apply` with no image yet, the pull fails.

Rather than fail the boot and require a manual recovery, the script waits. If the image never arrives, `docker compose up -d` fails on the missing image and the log shows why.

---

## Part 8 — `iam.tf`: three roles, least privilege each

243 lines, and the most security-relevant file. Three roles, each with an assume-role policy (who may use it) and one or more resource policies (what it may do).

### The EC2 role

```hcl
data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}
```

The **assume role policy**, also called the trust policy. It answers "who can become this role?" Here: the EC2 service, when it needs to hand credentials to an instance.

```hcl
resource "aws_iam_role" "ec2" {
  name               = "${local.name}-ec2"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_instance_profile" "ec2" {
  name = "${local.name}-ec2"
  role = aws_iam_role.ec2.name
}
```

The instance profile is the container that EC2 attaches to an instance. It holds exactly one role. `ec2.tf` references this profile by name.

### The EC2 permission policy

Using a `data "aws_iam_policy_document"` rather than a hand-written JSON string gives you HCL interpolation for ARNs and automatic correct JSON. Five statements:

```hcl
  statement {
    sid     = "ECRPull"
    actions = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
```

`GetAuthorizationToken` **must** use `resources = ["*"]` — the AWS API takes no resource parameter for it. This is the one legitimate wildcard in the file. Note it grants only the ability to *get a token*, not to pull any particular image.

```hcl
  statement {
    sid     = "ECRImages"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:GetDownloadUrlForLayer",
      "ecr:BatchGetImage",
      "ecr:DescribeImages",
    ]
    resources = ["${aws_ecr_repository.app.arn}/*"]
  }
```

The pull actions, scoped to this one repository. `/*` covers all images within it.

```hcl
  statement {
    sid     = "SSMParameters"
    actions = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"]
    resources = ["arn:aws:ssm:${var.aws_region}:${data.aws_caller_identity.current.account_id}:parameter${var.ssm_prefix}/*"]
  }
```

Read access limited to `/rateguard/*`. This is the statement that lets the app fetch its own admin key at startup. Scoping it to the prefix means a compromised instance cannot read every other parameter in the account.

```hcl
  statement {
    sid     = "DynamoDBData"
    actions = [
      "dynamodb:DescribeTable",
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:Query",
      "dynamodb:Scan",
    ]
    resources = [
      aws_dynamodb_table.tenants.arn,
      "${aws_dynamodb_table.tenants.arn}/index/*",
      aws_dynamodb_table.usage.arn,
      aws_dynamodb_table.anomalies.arn,
    ]
  }
```

Named to the three tables the app touches. The `/index/*` entry covers the `api_key_prefix-index` GSI, which is how tenant keys are resolved — a query on an index needs permission on the index ARN, not just the table.

Note what is **absent**: no `DeleteItem`. The dashboard's delete is a soft disable (`UpdateItem` setting `enabled = false`), so hard deletion is not needed. No `CreateTable` or `DeleteTable` either — schema changes come from Terraform, never from the running app.

```hcl
  statement {
    sid       = "SNSPublish"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.alerts.arn]
  }
```

The app can publish to the cost-alert topic. Named to that one topic.

```hcl
  statement {
    sid     = "Logs"
    actions = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/ec2/${local.name}*"]
  }
```

Scoped to `/aws/ec2/rateguard-dev*`. The trailing `*` allows a new log stream under the group without a redeploy.

### Attaching it

```hcl
resource "aws_iam_role_policy" "ec2" {
  name   = "${local.name}-ec2-policy"
  role   = aws_iam_role.ec2.id
  policy = data.aws_iam_policy_document.ec2_policy.json
}

resource "aws_iam_role_policy_attachment" "ec2_ssm_core" {
  role       = aws_iam_role.ec2.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}
```

An **inline** policy for the custom statements, plus an **AWS-managed** policy attachment for Session Manager. `AmazonSSMManagedInstanceCore` is maintained by AWS and covers the agent's needs (`ssm:UpdateInstanceInformation`, `ssmmessages:*`, `ec2messages:*`). Writing those by hand is error-prone and would drift as AWS adds required actions.

Inline versus managed: inline policies are deleted with the role and cannot be reused. Managed policies can be shared across roles. The custom policy is inline because only this role uses it.

### The Lambda role

Same structure, fewer permissions:

```hcl
resource "aws_lambda_function" "cost" {
  ...
}
```

Its policy has four statements:

| `sid` | Actions | Resources | Why |
|---|---|---|---|
| `Logs` | CreateLogGroup/Stream, PutLogEvents | The Lambda's own log group | Write function output |
| `AnomalyWrites` | PutItem, UpdateItem, Scan | `anomalies` table only | Record and dedupe findings |
| `AlertPublish` | sns:Publish | `alerts` topic | Send the notification |
| `BillingMetrics` | cloudwatch:GetMetricStatistics | `*` | Read `EstimatedCharges` |

`AnomalyWrites` is narrower than the EC2 policy — the Lambda touches **one** table, so it can only touch one. `Scan` is there for the per-hour deduplication check: before writing a new anomaly, the worker looks for an existing record in the same UTC hour so re-runs do not alert twice.

`BillingMetrics` needs `resources = ["*"]` because `GetMetricStatistics` does not support resource-level permissions for CloudWatch metrics.

### The GitHub Actions role

This one is different: no long-lived AWS keys anywhere.

```hcl
data "aws_iam_policy_document" "ci_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [local.github_oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_repo}:ref:refs/heads/main"]
    }
  }
}
```

**OIDC federation.** GitHub Actions is an OIDC provider. When a workflow runs, GitHub issues a signed JWT describing exactly which repository, which ref, and which workflow produced it. AWS validates the signature against the provider you created once, then applies these conditions.

The two conditions are what make this safe:

- `aud` must be `sts.amazonaws.com` — the token was minted for AWS STS, not for some other service.
- `sub` must be `repo:Aswanidev-vs/RateGuard:ref:refs/heads/main` — **only** the `main` branch of **this** repo can assume the role.

Consequences worth internalising:

- **No `AWS_ACCESS_KEY_ID` secrets in GitHub.** Nothing to leak, nothing to rotate, nothing to accidentally print in a log.
- A fork cannot assume the role — the `sub` names your repo.
- A pull request from a branch cannot assume it — the `sub` requires `refs/heads/main`. The guide notes that PRs only run tests and Terraform checks, never touch AWS. That is enforced here, not just by convention.
- Anyone who can push to `main` can deploy. That is the remaining trust boundary, and it is the right one.

```hcl
  statement {
    sid       = "ECRAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid     = "ECRPush"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
      "ecr:PutImage",
      "ecr:DescribeImages",
    ]
    resources = ["${aws_ecr_repository.app.arn}/*"]
  }
```

Push access, mirroring the pull actions the instance has. The multipart upload trio (`InitiateLayerUpload`, `UploadLayerPart`, `CompleteLayerUpload`) is what `docker push` actually calls.

```hcl
  statement {
    sid     = "SSMDeploy"
    actions = ["ssm:SendCommand", "ssm:GetCommandInvocation"]
    resources = [
      "arn:aws:ssm:${var.aws_region}:*:document/AWS-RunShellScript",
      "arn:aws:ssm:${var.aws_region}:${data.aws_caller_identity.current.account_id}:managed-instance/*",
      "arn:aws:ssm:${var.aws_region}:${data.aws_caller_identity.current.account_id}:command/*",
      aws_instance.app.arn,
    ]
  }
```

The ability to run `/opt/rateguard/deploy.sh` on the instance. Scoped to the specific AWS-owned document, this account's managed instances, and this one instance's ARN.

`ssm:GetCommandInvocation` is needed to read the command's output back — without it, the deploy fires but CI cannot tell whether it worked.

```hcl
  statement {
    sid     = "SSMDescribe"
    actions = ["ssm:DescribeInstanceInformation"]
    resources = ["*"]
  }
```

`DescribeInstanceInformation` does not support resource-level permissions, so `*` is required. It is a read-only describe call.

What the CI role notably **cannot** do: touch DynamoDB, read the SSM secrets, modify IAM, create or destroy infrastructure. A compromised CI pipeline can deploy a bad image and disrupt service. It cannot exfiltrate your admin key or delete your tables.

---

## Part 9 — `dynamodb.tf`: three tables

DynamoDB is schema-light but not schema-free. You must declare the key attributes, their types, and any indexes.

### Tenants

```hcl
resource "aws_dynamodb_table" "tenants" {
  name                        = local.tenants_table
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "tenant_id"
  deletion_protection_enabled = var.deletion_protection

  attribute {
    name = "tenant_id"
    type = "S"
  }

  attribute {
    name = "api_key_prefix"
    type = "S"
  }

  global_secondary_index {
    name            = "api_key_prefix-index"
    hash_key        = "api_key_prefix"
    projection_type = "ALL"
  }

  server_side_encryption {
    enabled = true
  }
}
```

`billing_mode = "PAY_PER_REQUEST"` is on-demand: you pay per request, with no capacity planning and no hourly cost when idle. For a free-tier learning stack this is the only sensible choice — provisioned capacity bills continuously whether or not you use it.

`hash_key = "tenant_id"` is the partition key. `type = "S"` means string. The app generates IDs like `tn_` followed by 24 hex characters.

The GSI is the interesting part:

```hcl
  global_secondary_index {
    name            = "api_key_prefix-index"
    hash_key        = "api_key_prefix"
    projection_type = "ALL"
  }
```

This exists because of how authentication works. A request arrives with `ag_live_9f2c8b1d…`. The server cannot query by `tenant_id`, because it does not know it yet. It extracts the prefix (the literal `ag_live_` plus 8 secret characters) and queries this index.

`projection_type = "ALL"` copies every attribute into the index, so a query returns the full tenant record without a second read against the table. The cost is storage — roughly double for this table. The alternative, `KEYS_ONLY`, would halve storage but require a follow-up `GetItem` on every authenticated request, which is the hot path of the whole product.

`server_side_encryption { enabled = true }` encrypts at rest with an AWS-managed key.

### Usage

```hcl
  hash_key        = "tenant_id"
  range_key       = "bucket"
```

A **composite key**. `tenant_id` alone is not unique here — each tenant has one item per time bucket. The pair (`tenant_id`, `bucket`) is unique, where `bucket` is an hour-sized string like `2006-01-02T15`.

This is the right shape for the access pattern: "give me every bucket for this tenant in this window" is a `Query` on the partition key with a range condition on the sort key. A single table scan would get slower every hour forever.

```hcl
  ttl {
    attribute_name = "ttl"
    enabled        = true
  }
```

DynamoDB deletes items whose `ttl` attribute holds a past epoch timestamp. This is how usage data expires — no cron job, no cleanup Lambda, no cost. The guide notes a 30-day TTL.

Two caveats worth knowing: deletion is eventually consistent, so expired items can be readable for a short window; and TTL deletions do not consume write capacity in on-demand mode but do generate delete events in streams.

The retention period is set in the application, not in Terraform: `internal/store/dynamodb.go:27` defines `usageTTL = 30 * 24 * time.Hour`. The Terraform config only *enables* the TTL mechanism on the `ttl` attribute; the app decides what timestamp to write into it. If you want a different retention window, change the Go constant and rebuild — not this file.

### Anomalies

```hcl
  hash_key  = "tenant_id"
  range_key = "detected_at"
```

Same composite pattern. `tenant_id` is `__account__` for billing findings, since AWS billing is account-wide.

`detected_at` as the sort key gives two things for free: the per-hour deduplication check is a targeted query rather than a scan, and the dashboard's "recent anomalies" list is a `Query` with `ScanIndexForward = false` on the same partition.

```hcl
  deletion_protection_enabled = var.deletion_protection
```

Present on all three tables, driven by one variable. Flip it to `true` and `terraform destroy` refuses until you flip it back — the intended friction for anything holding data.

---

## Part 10 — `ecr.tf`, `s3.tf`, `ssm.tf`: storage and config

### ECR

```hcl
resource "aws_ecr_repository" "app" {
  name                 = local.name
  image_tag_mutability = "MUTABLE"
  force_delete         = true

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}
```

`image_tag_mutability = "MUTABLE"` lets you overwrite `latest`. `IMMUTABLE` would be safer for traceability — every tag points at exactly one image — but it breaks the `latest`-tag workflow the guide and `deploy.sh` rely on. The trade-off is deliberate: CI also pushes SHA tags, so a specific version is always recoverable.

`scan_on_push = true` runs a vulnerability scan on every push, populating the ECR scan findings. `force_delete = true` lets `terraform destroy` remove the repository even when it still holds images.

```hcl
resource "aws_ecr_lifecycle_policy" "app" {
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the 10 most recent images"
      selection    = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 10
      }
      action = { type = "expire" }
    }]
  })
}
```

Without this, every CI run leaves an image forever and storage grows without bound. The policy keeps 10 and expires the rest.

`jsonencode` converts the HCL object to a JSON string, which is what the ECR API expects. Writing the JSON by hand is possible but loses HCL's interpolation.

`tagStatus = "any"` includes untagged images, which matters because CI pushes SHA tags alongside `latest` — a `tagged` filter would never expire the untagged leftovers.

### S3 artifacts

```hcl
resource "aws_s3_bucket" "artifacts" {
  bucket        = "${local.name}-artifacts-${random_id.bucket_suffix.hex}"
  force_destroy = var.force_destroy_buckets
}
```

`random_id.bucket_suffix` is 4 bytes rendered as 8 hex characters. S3 bucket names are globally unique across all of AWS, so `rateguard-dev-artifacts` alone would collide with anyone else who ran this stack. The suffix makes collision effectively impossible while keeping the name recognisable.

Four separate resources configure the bucket, because AWS split S3 bucket settings into distinct API calls:

```hcl
resource "aws_s3_bucket_public_access_block" "artifacts" {
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
```

All four **account-level and bucket-level** blocks on. Together they make the bucket private even if a policy is later added that would expose it — the public access block overrides the policy. This is defence in depth: a mistake in a bucket policy does not become a data leak.

```hcl
resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_versioning" "artifacts" {
  versioning_configuration {
    status = "Enabled"
  }
}
```

AES256 encryption at rest with an S3-managed key, and versioning so an overwritten artifact can be recovered.

### SSM parameters

```hcl
resource "aws_ssm_parameter" "admin_api_key" {
  name        = "${var.ssm_prefix}/admin-api-key"
  type        = "SecureString"
  value       = var.admin_api_key
}
```

`SecureString` encrypts the value at rest with KMS. The app reads it at startup using the instance role's `ssm:GetParameter` permission.

**The critical caveat:** `value = var.admin_api_key` writes that key into the Terraform **state file**, in plaintext, alongside everything else. SSM encryption protects the parameter at rest in AWS; it does nothing about state. This is why `TERRAFORM-GUIDE.md` says the state bucket must be private, encrypted, and access-restricted. It is also why the backend is a versioned S3 bucket rather than a local file.

```hcl
resource "aws_ssm_parameter" "session_secret" {
  type  = "SecureString"
  value = local.session_secret
}
```

`local.session_secret` resolves to either the operator's value or the generated one, via the `coalesce` in `main.tf`.

```hcl
resource "aws_ssm_parameter" "sns_topic" {
  name  = "${var.ssm_prefix}/sns-topic-arn"
  type  = "String"
  value = aws_sns_topic.alerts.arn
}

resource "aws_ssm_parameter" "upstream_url" {
  name  = "${var.ssm_prefix}/upstream-url"
  type  = "String"
  value = "http://demo:8090"
}
```

Plain `String`, not `SecureString` — these are not secrets. `String` is cheaper (no KMS request per read) and readable in the console, which is useful when debugging.

Note `upstream_url` is `http://demo:8090`, matching the Compose service name. The app resolves it inside the Compose network on the EC2 host.

---

## Part 11 — `sns.tf`: alerts and the billing alarm

### The anomaly topic

```hcl
resource "aws_sns_topic" "alerts" {
  name = "${local.name}-cost-alerts"
}

resource "aws_sns_topic_subscription" "alerts_email" {
  count     = var.admin_email == "" ? 0 : 1
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.admin_email
}
```

The same `count`-with-empty-string pattern used for SSH. No email configured means no subscription resource at all, rather than a subscription with an empty endpoint.

`protocol = "email"` sends a confirmation message to the address. The subscription is **pending** until someone clicks the link in that email. This is why `TERRAFORM-GUIDE.md` lists `admin_email` as "confirms SNS subscriptions" — an `apply` alone does not deliver alerts.

### The billing topic, in the other region

```hcl
resource "aws_sns_topic" "billing" {
  provider = aws.us_east_1
  name     = "${local.name}-billing-alerts"
}
```

`provider = aws.us_east_1` puts this topic in us-east-1 alongside the billing metric it will receive alarms from. An SNS topic in Mumbai cannot be the target of an alarm evaluated in Virginia.

### The billing alarm

```hcl
resource "aws_cloudwatch_metric_alarm" "billing" {
  provider            = aws.us_east_1

  alarm_name          = "${local.name}-estimated-charges"
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "EstimatedCharges"
  namespace           = "AWS/Billing"
  period              = 21600
  statistic           = "Maximum"
  threshold           = var.alarm_threshold_usd
  treat_missing_data  = "notBreaching"

  dimensions = { Currency = "USD" }

  alarm_actions = [aws_sns_topic.billing.arn]
}
```

Each argument matters:

- `namespace = "AWS/Billing"` and `metric_name = "EstimatedCharges"` identify the metric AWS publishes about your account's running spend.
- `period = 21600` is 6 hours in seconds. AWS updates billing metrics only a few times a day, so a shorter period would mostly evaluate stale data.
- `statistic = "Maximum"` over that period — for a cumulative spend figure, max is the conservative choice.
- `evaluation_periods = 1` alarms on a single breaching period, because waiting for several would delay a spend warning by 18+ hours.
- `dimensions = { Currency = "USD" }` — `EstimatedCharges` is published per currency, so without this dimension the metric has no data points at all. This is a common and confusing failure.
- `treat_missing_data = "notBreaching"` avoids a false alarm when billing data has not arrived yet.

**Acknowledge the limitation:** billing metrics are updated roughly every 6 hours and can lag up to 24 hours. This alarm is a backstop, not a real-time guard. The Lambda's hourly `EstimatedCharges` check is the faster signal; this alarm catches "you have already spent a dollar" in case the Lambda is broken.

---

## Part 12 — `lambda.tf` and `observability.tf`: the cost detector

### Zipping the binary

```hcl
data "archive_file" "cost_lambda" {
  type        = "zip"
  source_file = pathexpand(var.lambda_zip_path)
  output_path = "${path.module}/rateguard-cost-lambda.zip"
}
```

`archive_file` is a **data source**, so it runs at plan time, not apply time. It reads `../build/bootstrap` — the file you compiled yourself — and writes a zip next to the Terraform files.

`pathexpand` expands a leading `~` so `lambda_zip_path` works in both shells.

This is why `TERRAFORM-GUIDE.md` insists on building `build/bootstrap` before `plan`: the data source reads the file during plan and fails immediately if it is absent. The filename must be exactly `bootstrap` with no `.zip`, because the AWS Lambda `provided` runtimes expect the compiled binary at that path inside the archive.

Terraform zips it for you, which is why you build an uncompressed binary and let `output_path` produce the `.zip`.

### The function

```hcl
resource "aws_lambda_function" "cost" {
  function_name    = "${local.name}-cost-anomaly"
  role             = aws_iam_role.lambda.arn
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  filename         = data.archive_file.cost_lambda.output_path
  source_code_hash = data.archive_file.cost_lambda.output_base64sha256
  timeout          = 60
  memory_size      = 256

  depends_on = [aws_iam_role_policy.lambda, aws_cloudwatch_log_group.cost]
}
```

`runtime = "provided.al2023"` is the **custom runtime on AL2023**: no language runtime, just an executable named `bootstrap`. That is why the Go binary must be called `bootstrap`. It maps directly to `cmd/lambda/main.go`.

`architectures = ["arm64"]` selects Graviton — cheaper per invocation and matches the `GOARCH=arm64` in the build command. A mismatch fails at invocation with a format error, not at deploy.

`source_code_hash` is what makes updates work. Terraform compares this hash to the deployed one; if it changed, the function's code is replaced. Without it, Terraform would not notice a rebuilt binary.

`timeout = 60` seconds is generous for a function that makes a few API calls. `memory_size = 256` MB also sets CPU share proportionally — Lambda allocates CPU in proportion to memory.

`depends_on` covers two non-obvious orderings:

- `aws_iam_role_policy.lambda` — a function can be created before its policy exists, but it cannot assume the role until the policy is attached. Depending on the policy avoids an invocation window with no permissions.
- `aws_cloudwatch_log_group.cost` — see below.

### The environment variables

```hcl
  environment {
    variables = {
      AWS_REGION               = var.aws_region
      COST_BILLING_REGION      = "us-east-1"
      COST_METRIC_NAME         = "EstimatedCharges"
      DYNAMODB_TENANTS_TABLE   = local.tenants_table
      DYNAMODB_USAGE_TABLE     = local.usage_table
      DYNAMODB_ANOMALIES_TABLE = local.anomalies_table
      SNS_TOPIC_ARN            = aws_sns_topic.alerts.arn
    }
  }
```

`AWS_REGION` is set explicitly even though Lambda provides it — the app reads it the same way it does on EC2, so the code has one configuration path.

`COST_BILLING_REGION = "us-east-1"` is hard-coded because the function itself runs in Mumbai but must read the billing metric from Virginia. The Lambda role has `cloudwatch:GetMetricStatistics` on `*`, which works cross-region.

These are all visible in the console and to anyone with `lambda:GetFunctionConfiguration`. No secrets here, which is why it is safe.

### The hourly trigger

```hcl
resource "aws_cloudwatch_event_rule" "cost_hourly" {
  name                = "${local.name}-cost-hourly"
  schedule_expression = "rate(1 hour)"
}

resource "aws_cloudwatch_event_target" "cost_lambda" {
  rule = aws_cloudwatch_event_rule.cost_hourly.name
  arn  = aws_lambda_function.cost.arn
}

resource "aws_lambda_permission" "cost_eventbridge" {
  statement_id  = "AllowExecutionFromEventBridge"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.cost.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.cost_hourly.arn
}
```

Three resources, because EventBridge and Lambda each enforce their own side of the permission.

`rate(1 hour)` uses EventBridge's `rate()` syntax. Scheduled rules evaluate in **UTC**, so this fires at the top of each UTC hour. That aligns with the worker's per-hour deduplication: `detected_at` is truncated to the hour in UTC.

The third resource is the one people forget. Creating a rule and a target is not enough — Lambda will not accept invocations from EventBridge unless a resource-based policy grants it. `source_arn` scopes the grant to this specific rule, so no other EventBridge rule in the account can invoke the function.

`source_arn` is why the permission must reference the rule rather than using `*`: without it, any rule could trigger the function.

### The log group

```hcl
resource "aws_cloudwatch_log_group" "cost" {
  name              = "/aws/lambda/${local.name}-cost-anomaly"
  retention_in_days = var.log_retention_days
}
```

Declaring the log group **before** the function has a specific benefit: Lambda auto-creates a log group with **never-expire** retention if one does not exist. Terraform would then find an unmanaged resource occupying the name.

By creating it here with `retention_in_days = 7`, logs expire automatically. Without this, a chatty Lambda accumulates CloudWatch logs indefinitely and the bill grows quietly.

The name must match Lambda's convention exactly — `/aws/lambda/<function-name>` — which is why it is derived from `local.name` rather than a literal. That is also what `depends_on` in the function guarantees.

---

## Part 13 — `outputs.tf` and the bootstrap stack

### Outputs

```hcl
output "ec2_public_ip" {
  description = "Public IP of the RateGuard EC2 host."
  value       = aws_instance.app.public_ip
}
```

Printed after `apply`, and retrievable with `terraform output -raw <name>`. The guide's push script uses exactly that:

```bash
ECR=$(terraform output -raw ecr_repository_url)
```

Eight outputs, chosen so a human or a CI job never has to open the console:

| Output | Used for |
|---|---|
| `ec2_public_ip` | Health checks, smoke tests |
| `ecr_repository_url` | `docker login`, `docker push` |
| `sns_topic_arn` | Lambda config, manual publishes |
| `github_actions_role_arn` | The `AWS_ROLE_ARN` repo variable |
| `dashboard_url` | HTTPS when `domain_name` is set, HTTP otherwise |
| `ssm_prefix` | Locating the parameters by hand |
| `dynamodb_tables` | Map of all three table names |
| `artifacts_bucket` | The private bucket name |

`dashboard_url` shows a conditional in an output:

```hcl
  value = var.domain_name != "" ? "https://${var.domain_name}/dashboard" : "http://${aws_instance.app.public_ip}/dashboard"
```

The same branch that drives `local.cookie_secure`, surfacing the consequence rather than making you derive it.

### The bootstrap stack

`terraform/bootstrap/` is a separate, deliberately minimal configuration. It exists to solve a chicken-and-egg problem: **state cannot live in a bucket that the state is responsible for creating.**

```hcl
resource "aws_s3_bucket" "state" {
  bucket        = "${var.bucket_name}-${random_id.suffix.hex}"
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "state" {
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_dynamodb_table" "lock" {
  name         = var.lock_table
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }
}
```

Three things worth noting:

- `force_destroy = false` — the opposite of the root stack's `true`. The state bucket holds every other resource's identity. Destroying it carelessly means losing the ability to manage the whole stack.
- **Versioning on the state bucket.** A corrupted or accidentally deleted `terraform.tfstate` can be recovered from a previous version. This is the single highest-value setting in the entire configuration.
- The lock table's `hash_key = "LockID"` with a capital `L` and `D` is not a typo. Terraform's S3 backend expects exactly `LockID` (string) when using a DynamoDB lock table.

Bootstrap state stays **local** — `terraform.tfstate` in that folder. That is acceptable because the bootstrap stack contains no application secrets, and it keeps the bootstrap independent of the bucket it creates. The guide notes you may commit it privately or back it up.

---

## Part 14 — The dependency graph

Terraform derives ordering from references. Simplified, `apply` proceeds roughly:

```
        random_id, data sources, providers
                     |
          +----------+----------+
          |                     |
       network.tf           iam.tf (roles first)
          |                     |
       ec2.tf  <---- ecr.tf, dynamodb.tf, s3.tf, ssm.tf
          |                     |
   user_data renders      sns.tf, lambda.tf
   (needs ECR +           (needs dynamodb, sns,
    DynamoDB names)        iam role, log group)
          |
      instance boots, pulls image, starts compose
```

Things this ordering guarantees:

- The security group exists before the instance references it.
- The instance profile exists before the instance attaches it.
- The ECR repository and DynamoDB tables exist before the rendered user data names them.
- The Lambda's log group exists before the function, so no auto-created never-expire group appears.
- The Lambda's policy exists before the function, so there is no permission-less window.

Things it does **not** guarantee, which is why the user data has a retry loop:

- That an image exists in ECR. Terraform creates the repository, not the image.

And things that require an operator:

- That the GitHub OIDC provider exists in the account (created once, per the guide).
- That the SNS email subscription is confirmed (requires clicking a link).

---

## Part 15 — Change it yourself

**Scale the instance up.** One line in `terraform.tfvars`:

```hcl
instance_type = "t3.small"
```

Then `plan` — you will see one item to update in place. Note this is not free-tier eligible.

**Run a second environment beside dev.**

```hcl
environment = "prod"
```

`local.name` becomes `rateguard-prod`, so resource names differ. Caveat: the DynamoDB table names in `locals` are hard-coded (`api_guard_tenants`), so **both stacks would share tables**. Edit those three locals to include `${var.environment}` first if you want true isolation.

**Make tables undeletable.**

```hcl
deletion_protection = true
```

`terraform destroy` will now refuse. To actually remove them, set it back to `false`, `apply`, then `destroy`.

**Change the alarm threshold.**

```hcl
alarm_threshold_usd = 5
```

Forces replacement of the CloudWatch alarm. Harmless — an alarm has no data.

**Allow SSH from one IP.**

```hcl
allowed_ssh_cidr = "203.0.113.4/32"
```

Adds a single ingress rule for port 22. Remember AL2023 has no `ec2-user` password; use your key pair, or prefer SSM Session Manager and leave this empty.

**Extend the billing alarm window.** `sns.tf`, `period`. `21600` is 6 hours; `43200` is 12. Longer periods mean fewer data points and a slower alarm.

**Keep more images.** `ecr.tf`, `countNumber`. Change `10` to `25`. Watch ECR storage cost — each image is roughly 15–20 MB here, so this is minor, but the principle generalises.

**Change how often the cost detector runs.** `lambda.tf`, `schedule_expression`. `rate(30 minutes)` doubles invocations and halves detection latency. There is no benefit below about 5 minutes — billing data does not update that fast.

---

## Part 16 — Things that will bite you

**`archive_file: no such file or directory`.** You skipped the build step. Terraform reads `build/bootstrap` at **plan** time.

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/bootstrap ./cmd/lambda
```

**`Error: No credential providers`.** The `hashicorp/terraform` container does not see your host's AWS credentials. Mount them:

```bash
-v "$HOME/.aws:/root/.aws:ro"
```

**`Backend initialization required`.** `backend.hcl` is missing or wrong. Re-run `init -backend-config=backend.hcl` after any change to the state bucket.

**EC2 instance exists but nothing is running.** Read the boot log — the user data tees everything there:

```bash
aws ssm send-command --instance-ids <instance-id> \
  --document-name AWS-RunShellScript \
  --parameters 'commands=["tail -n 100 /var/log/rateguard-userdata.log"]'
```

The usual causes, in order: image not in ECR yet, ECR login failed, `.env` incomplete.

**`/readyz` returns 503 but `/healthz` returns 200.** Liveness and readiness differ deliberately. 503 means the store ping failed — check the instance role's DynamoDB permissions and that `AWS_REGION` matches where the tables live.

**Dashboard login rejects the correct key.** The app reads `/rateguard/admin-api-key` from SSM. Confirm `SSM_ENABLED=true` in the instance's `.env` and that the parameter exists:

```bash
aws ssm get-parameter --name /rateguard/admin-api-key --with-decryption --query Parameter.Value --output text
```

Treat the output as a secret — it is your admin key.

**Cookies do not persist.** `COOKIE_SECURE=true` requires HTTPS. Set `domain_name` and point DNS at the EC2 IP, or leave it empty for HTTP.

**Billing alarm never fires.** It lives in us-east-1, and billing data lags up to 24 hours. Check it there, not in your primary region:

```bash
aws cloudwatch describe-alarms --region us-east-1 \
  --alarm-names rateguard-dev-estimated-charges
```

**`terraform apply` wants to replace the DynamoDB tables.** Something changed a key attribute or the billing mode — both force replacement, which means data loss. Read the plan carefully before approving. If the change is unintended, revert the config instead of applying.

**The Lambda publishes but the dashboard says "Not alerted".** The write succeeded but `MarkAnomalyAlerted` did not. Check the Lambda's policy includes `dynamodb:UpdateItem` on the anomalies table, and read the function logs.

**Two stacks, shared tables.** As noted in Part 15 — `local.tenants_table` and friends are hard-coded without an environment suffix.

---

## Part 17 — The security posture in one page

| Concern | How it is handled | Where |
|---|---|---|
| Admin key at rest in AWS | SSM `SecureString` (KMS) | `ssm.tf` |
| Admin key at rest in Terraform | **Not protected** — state holds it plaintext | `backend.tf` |
| Admin key on the instance | Never written; fetched from SSM at startup | `user_data.sh.tftpl` |
| Admin key in the image | Never — `.env` is in `.dockerignore` | `.dockerignore` |
| Instance credentials | IMDSv2 required, hop limit 1 | `ec2.tf` |
| Long-lived AWS keys in CI | None; OIDC federation only | `iam.tf` |
| Which refs can deploy | `StringEquals` on `sub` = `refs/heads/main` | `iam.tf` |
| SSH exposure | No port 22 ingress by default | `network.tf` |
| Instance filesystem | Read-only root, `noexec` tmpfs, all caps dropped | `docker-compose.yml` |
| DynamoDB deletion | Optional deletion protection | `dynamodb.tf` |
| S3 state bucket | Versioned, encrypted, public access blocked | `bootstrap/main.tf` |
| Secrets in plan output | `sensitive = true` on both variables | `variables.tf` |
| Container registry | Scan on push, 10-image retention | `ecr.tf` |
| Minimum key lengths | `validation` blocks, matching app limits | `variables.tf` |
| Egress | Fully open — required for ECR/SSM/AWS APIs | `network.tf` |

**The one item to act on:** secrets live in Terraform state. The S3 backend is private, encrypted, and versioned, but state files are routinely copied, and anyone with read access to that bucket can recover your admin key and session secret. Restrict access to the state bucket to the narrowest set of principals that need to run Terraform, and never move state files through unencrypted channels.