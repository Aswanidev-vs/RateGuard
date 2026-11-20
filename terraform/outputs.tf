output "ec2_public_ip" {
  description = "Public IP of the RateGuard EC2 host."
  value       = aws_instance.app.public_ip
}

output "ecr_repository_url" {
  description = "ECR repository URL used by CI and the EC2 bootstrap."
  value       = aws_ecr_repository.app.repository_url
}

output "sns_topic_arn" {
  description = "SNS topic for cost anomaly alerts."
  value       = aws_sns_topic.alerts.arn
}

output "github_actions_role_arn" {
  description = "Set this as the GitHub Actions variable AWS_ROLE_ARN."
  value       = aws_iam_role.ci.arn
}

output "dashboard_url" {
  description = "Dashboard URL (HTTPS when domain_name is set)."
  value       = var.domain_name != "" ? "https://${var.domain_name}/dashboard" : "http://${aws_instance.app.public_ip}/dashboard"
}

output "ssm_prefix" {
  description = "SSM prefix holding the app secrets."
  value       = var.ssm_prefix
}

output "dynamodb_tables" {
  description = "Table names consumed by the app and Lambda."
  value = {
    tenants   = aws_dynamodb_table.tenants.name
    usage     = aws_dynamodb_table.usage.name
    anomalies = aws_dynamodb_table.anomalies.name
  }
}

output "artifacts_bucket" {
  description = "Private S3 bucket for deployment artifacts."
  value       = aws_s3_bucket.artifacts.id
}
