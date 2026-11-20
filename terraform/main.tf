data "aws_caller_identity" "current" {}
data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-x86_64"]
  }
}

resource "random_password" "session" {
  count   = var.session_secret == null ? 1 : 0
  length  = 48
  special = false
}

resource "random_id" "bucket_suffix" {
  byte_length = 4
}

locals {
  name = "${var.project}-${var.environment}"

  session_secret = coalesce(var.session_secret, one(random_password.session[*].result))

  tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
    Repository  = "Aswanidev-vs/RateGuard"
  }

  tenants_table   = "api_guard_tenants"
  usage_table     = "api_guard_usage"
  anomalies_table = "api_guard_cost_anomalies"

  cookie_secure = var.domain_name != "" ? "true" : "false"
  caddy_domain  = var.domain_name != "" ? var.domain_name : "localhost"

  # The GitHub OIDC provider is a one-time account-level resource created via
  # the guide. Referencing it by ARN avoids hard-coding a rotating thumbprint.
  github_oidc_provider_arn = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:oidc-provider/token.actions.githubusercontent.com"
  github_repo              = "Aswanidev-vs/RateGuard"
}
