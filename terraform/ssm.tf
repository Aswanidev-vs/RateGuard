# The application reads these at startup when SSM_ENABLED=true.
# Secrets land in Terraform state: keep the S3 state bucket private and encrypted.
resource "aws_ssm_parameter" "admin_api_key" {
  name        = "${var.ssm_prefix}/admin-api-key"
  description = "RateGuard admin key for the API and dashboard"
  type        = "SecureString"
  value       = var.admin_api_key

  tags = { Name = "${var.ssm_prefix}/admin-api-key" }
}

resource "aws_ssm_parameter" "session_secret" {
  name        = "${var.ssm_prefix}/session-secret"
  description = "RateGuard dashboard session HMAC secret"
  type        = "SecureString"
  value       = local.session_secret

  tags = { Name = "${var.ssm_prefix}/session-secret" }
}

resource "aws_ssm_parameter" "sns_topic" {
  name        = "${var.ssm_prefix}/sns-topic-arn"
  description = "SNS topic for cost anomaly alerts"
  type        = "String"
  value       = aws_sns_topic.alerts.arn

  tags = { Name = "${var.ssm_prefix}/sns-topic-arn" }
}

resource "aws_ssm_parameter" "upstream_url" {
  name        = "${var.ssm_prefix}/upstream-url"
  description = "Upstream URL behind the rate-limit proxy"
  type        = "String"
  value       = "http://demo:8090"

  tags = { Name = "${var.ssm_prefix}/upstream-url" }
}
