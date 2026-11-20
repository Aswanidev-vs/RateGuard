resource "aws_cloudwatch_log_group" "cost" {
  name              = "/aws/lambda/${local.name}-cost-anomaly"
  retention_in_days = var.log_retention_days
}
