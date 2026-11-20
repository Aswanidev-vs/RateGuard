# RateGuard Terraform Guide

This guide covers provisioning RateGuard on AWS with Terraform, run through the official `hashicorp/terraform` Docker image (no local Terraform install). **You execute every command.** Nothing in this repository has been applied to AWS by the coding agent.

Region defaults to `ap-south-1` (Mumbai). All resources are designed for the AWS free tier where possible; a billing alarm at $1 is included as a safety net.

---

## 1. What gets created

| Area | Resources |
|---|---|
| Network | VPC `10.20.0.0/16`, one public subnet, IGW, route table, security group (80/443, optional SSH) |
| Compute | EC2 `t3.micro` (Amazon Linux 2023), IMDSv2, encrypted 10 GB gp3, SSM instance profile |
| Data | DynamoDB `api_guard_tenants` (+ `api_key_prefix-index` GSI), `api_guard_usage` (30-day TTL), `api_guard_cost_anomalies` |
| Registry | ECR repository with scan-on-push and a keep-10 lifecycle policy |
| Storage | Private, encrypted, versioned S3 artifacts bucket |
| Config | SSM SecureStrings: `/rateguard/admin-api-key`, `/rateguard/session-secret`; Strings: `/rateguard/sns-topic-arn`, `/rateguard/upstream-url` |
| Alerts | Cost-anomaly SNS topic (+ optional email), us-east-1 billing SNS topic + $1 billing alarm |
| Cost worker | Lambda (`provided.al2023`, arm64), EventBridge hourly rule, log group with retention |
| CI | OIDC-assumable GitHub Actions role (ECR push + SSM deploy, restricted to `main`) |
| Bootstrap | S3 state bucket (versioned, encrypted, private) + DynamoDB lock table |

The EC2 instance installs Docker + the Compose plugin, writes `/opt/rateguard/{.env,docker-compose.yml,Caddyfile,deploy.sh}`, pulls the application image from ECR, and starts `api`, `demo`, and `caddy`. The app reads its secrets from SSM at startup; none are baked into the image or the instance user data.

---

## 2. Prerequisites

1. **AWS account + CLI** with rights to create VPC/EC2/IAM/DynamoDB/S3/ECR/SNS/Lambda/SSM/CloudWatch. Verify:
   ```bash
   aws sts get-caller-identity
   ```
2. **Docker Desktop running**, Linux containers. Terraform is invoked with:
   - Git Bash: `docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest ...`
   - PowerShell: `docker run --rm -v "${PWD}:/workspace" -w /workspace hashicorp/terraform:latest ...`
   - `cmd.exe` (as in the PRD): `docker run --rm -v "%cd%:/workspace" -w /workspace hashicorp/terraform:latest ...`
3. **GitHub OIDC provider** in the account (one-time). If it is missing:
   ```bash
   THUMBPRINT=$(curl -s https://raw.githubusercontent.com/github/explore/main/topics/actions/actions.md >/dev/null; \
     openssl s_client -servername token.actions.githubusercontent.com -connect token.actions.githubusercontent.com:443 \
     -showcerts </dev/null 2>/dev/null | openssl x509 -fingerprint -sha1 -noout | cut -d= -f2 | tr -d ':' | tr 'A-Z' 'a-z')
   aws iam create-open-id-connect-provider \
     --url https://token.actions.githubusercontent.com \
     --client-id-list sts.amazonaws.com \
     --thumbprint-list "$THUMBPRINT"
   ```
   If the provider already exists, skip this step. Terraform only references its ARN.
4. **Build the Lambda binary before `plan`/`apply`** — `archive_file` needs a real file:
   ```bash
   GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o build/bootstrap ./cmd/lambda
   ```
   On Windows Bash, `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/bootstrap ./cmd/lambda` works; PowerShell:
   ```powershell
   $env:GOOS="linux"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"
   go build -trimpath -o build/bootstrap ./cmd/lambda
   ```
   The file must be named exactly `build/bootstrap` (no `.zip` extension). Terraform zips it.

---

## 3. Bootstrap remote state (once)

From `G:\RateGuard\terraform\bootstrap`:

```bash
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest init
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest apply \
  -var='bucket_name=rateguard-tfstate' -var='region=ap-south-1'
```

The bucket gets a random suffix. Copy the `state_bucket` and `lock_table` outputs. Bootstrap state is local (`terraform.tfstate` in that folder) — back it up or commit it to a private repo only if it contains no secrets (it does not).

Create `terraform/backend.hcl` (gitignored):

```hcl
bucket         = "PASTE_STATE_BUCKET_OUTPUT"
key            = "rateguard/dev/terraform.tfstate"
region         = "ap-south-1"
encrypt        = true
dynamodb_table = "PASTE_LOCK_TABLE_OUTPUT"
```

Newer Terraform stores locks in S3 natively; the DynamoDB table is kept for compatibility and as a visible lock record. If your Terraform version warns about `dynamodb_table`, keep it — the table exists and is harmless.

---

## 4. Configure the root stack

From the repository root, copy the example and edit it:

```bash
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
```

Generate secrets and put them in `terraform.tfvars`, or export them as environment variables so they never touch disk:

```bash
export TF_VAR_admin_api_key="$(openssl rand -base64 48)"
export TF_VAR_session_secret="$(openssl rand -base64 48)"
```

If you use `TF_VAR_*`, remove the two corresponding lines from `terraform.tfvars` (or leave placeholders — environment variables win).

Recommended settings for a first run:

```hcl
admin_email   = "you@example.com"   # confirms SNS subscriptions
domain_name   = ""                  # HTTP on the public IP; set a DNS name for HTTPS
allowed_ssh_cidr = ""               # use SSM Session Manager instead of SSH
```

> **Security:** `admin_api_key`, `session_secret`, and the generated session secret are stored in SSM **and in Terraform state**. The state bucket is private and encrypted, but restrict access to it and never commit `backend.hcl`, `terraform.tfvars`, or state files.

---

## 5. Plan and apply

From `G:\RateGuard\terraform`:

```bash
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest init -backend-config=backend.hcl
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest fmt -check
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest validate
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest plan -out=tfplan
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest apply tfplan
```

If you prefer two stages (recommended for the first run), create ECR first, push an image, then create the rest:

```bash
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest \
  apply -target=aws_ecr_repository.app
# Build and push the image (section 6), then:
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest apply
```

EC2 user data retries the image pull for up to 10 minutes, so a single `apply` also works if the image arrives shortly after boot.

Useful outputs:

```bash
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest output
```

---

## 6. First image push (you run this)

Terraform creates the ECR repository; CI normally pushes images. For the very first boot, push one yourself from the repository root:

```bash
ECR=$(docker run --rm -v "/$PWD/terraform:/workspace" -w /workspace hashicorp/terraform:latest \
  output -raw ecr_repository_url)

aws ecr get-login-password --region ap-south-1 \
  | docker login --username AWS --password-stdin "$ECR"

docker build --build-arg VERSION=bootstrap --build-arg COMMIT=bootstrap \
  -t rateguard:bootstrap .
docker tag rateguard:bootstrap "$ECR:latest"
docker push "$ECR:latest"
```

You are intentionally running these commands; the coding agent did not build or push anything.

---

## 7. Verify the deployment

1. Health: `curl http://<ec2_public_ip>/healthz` → `{"status":"ok"}`.
2. Readiness: `curl http://<ec2_public_ip>/readyz` → `{"status":"ready"}` (checks DynamoDB).
3. Dashboard: open the `dashboard_url` output. Sign in with the admin key from `TF_VAR_admin_api_key` / `terraform.tfvars`.
4. Create a tenant in the dashboard; copy the one-time `ag_live_...` key.
5. Smoke test the limiter:

   ```bash
   KEY=ag_live_your_key_here
   for i in $(seq 1 120); do
     curl -s -o /dev/null -w "%{http_code}\n" \
       -H "Authorization: Bearer $KEY" \
       http://<ec2_public_ip>/proxy/quote
   done
   ```
   Expect a run of `200`s (up to the configured burst), then `429`s with `Retry-After`.

6. Check the EC2 boot log if the stack did not start:
   ```bash
   aws ssm send-command --instance-ids <instance-id> \
     --document-name AWS-RunShellScript \
     --parameters 'commands=["sudo tail -n 80 /var/log/rateguard-userdata.log"]'
   ```

---

## 8. Cost anomaly worker (M4 test)

The Lambda evaluates hourly and writes anomalies for the reserved `__account__` scope (AWS billing data is account-wide, not per tenant).

Inject a synthetic spike:

```bash
aws lambda update-function-configuration \
  --function-name rateguard-dev-cost-anomaly \
  --environment 'Variables={AWS_REGION=ap-south-1,COST_BILLING_REGION=us-east-1,COST_FAKE_CURRENT=9.5,COST_FAKE_HISTORY=1.1,1.2,1.0,1.3,1.1,1.2,1.0,DYNAMODB_TENANTS_TABLE=api_guard_tenants,DYNAMODB_USAGE_TABLE=api_guard_usage,DYNAMODB_ANOMALIES_TABLE=api_guard_cost_anomalies,SNS_TOPIC_ARN=<anomaly topic ARN>,COST_METRIC_NAME=EstimatedCharges}' \
  --query FunctionName --output text

aws lambda invoke --function-name rateguard-dev-cost-anomaly /tmp/lambda.json
cat /tmp/lambda.json
```

Then confirm: the anomaly appears on `/dashboard/anomalies`, a record exists in `api_guard_cost_anomalies`, and the SNS email arrives. Re-run the invoke within the same UTC hour — no duplicate alert should be sent. Restore the original environment afterwards (re-apply Terraform):

```bash
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest apply
```

---

## 9. GitHub Actions setup

1. Terraform output `github_actions_role_arn` → repository variable `AWS_ROLE_ARN`.
2. Output `ecr_repository_url` → repository variable `ECR_REPOSITORY_URL`.
3. EC2 instance ID → repository variable `EC2_INSTANCE_ID`.
4. Base health URL (e.g. `https://guard.example.com` or `http://<ip>`) → repository variable `PUBLIC_HEALTH_URL`.
5. Push to `main`. CI tests, checks Terraform formatting/validation, builds and Trivy-scans the image, pushes SHA + `latest` tags to ECR, then calls `/opt/rateguard/deploy.sh <sha>` on EC2 via SSM and curls `/healthz`.
6. Rollback: rerun the deploy command with a previous SHA, or push a tag:
   ```bash
   aws ssm send-command --instance-ids <instance-id> \
     --document-name AWS-RunShellScript \
     --parameters 'commands=["sudo /opt/rateguard/deploy.sh <previous-sha>"]'
   ```

Pull requests only run tests and Terraform checks; they never touch AWS.

---

## 10. Teardown

```bash
# From terraform/
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest destroy
```

ECR is configured with `force_delete = true`, and S3 with `force_destroy_buckets = true`, so images and artifacts do not block destroy. The remote-state bucket and lock table are in the separate bootstrap stack and survive intentionally:

```bash
# From terraform/bootstrap/ (only after the root stack is destroyed)
docker run --rm -v "/$PWD:/workspace" -w /workspace hashicorp/terraform:latest destroy
```

Stop the EC2 instance between demos to stay inside the 750 free-tier hours. Delete the SNS subscriptions you confirmed with email.

---

## 11. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `Error: No credential providers` | Configure `aws configure`/SSO on the host; the Terraform container reads `~/.aws` only if you mount it (`-v "$HOME/.aws:/root/.aws:ro"`). |
| `Backend initialization required` | Re-run `init -backend-config=backend.hcl` after creating/changing the bucket. |
| `archive_file: no such file` | Build `build/bootstrap` (section 2) before plan/apply. |
| EC2 has no stack | Check `/var/log/rateguard-userdata.log`; ECR login or image pull is the usual cause. |
| `/readyz` 503 | Instance role cannot read DynamoDB; verify the IAM policy and SSM prefix. |
| Dashboard login fails | Admin key is read from SSM `/rateguard/admin-api-key`; check the parameter and `SSM_ENABLED=true`. |
| Cookies do not stick | `COOKIE_SECURE=true` requires HTTPS; set `domain_name` and point DNS at the EC2 IP. |
| Billing alarm never fires | Billing metrics are only in `us-east-1` and can lag ~24h; the alarm is created there. |
| Lambda publishes but dashboard shows "Not alerted" | `MarkAnomalyAlerted` failed; check the Lambda logs and DynamoDB write policy. |
| SSH refused | Intended: `allowed_ssh_cidr` is empty. Use SSM Session Manager. |

---

## 12. Security notes

- No long-lived AWS keys are stored in GitHub Actions; the deploy role is OIDC-only and scoped to `refs/heads/main`.
- SSM parameters hold secrets; Terraform state also contains them, so lock down the S3 state bucket and enable bucket versioning (already configured).
- IMDSv2 is required on EC2; the instance role grants only the actions the app and SSM agent need.
- SSH is disabled unless you explicitly set `allowed_ssh_cidr`.
- The app forwards only 80/443 through Caddy; API and demo ports stay on the instance's internal network.
- Rotate `admin_api_key` / `session_secret` by changing the variables and re-applying; rotating the session secret signs out all dashboard sessions.
