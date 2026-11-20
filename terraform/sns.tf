resource "aws_sns_topic" "alerts" {
  name = "${local.name}-cost-alerts"
}

resource "aws_sns_topic_subscription" "alerts_email" {
  count     = var.admin_email == "" ? 0 : 1
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.admin_email
}

# Billing metrics and alarms live only in us-east-1.
resource "aws_sns_topic" "billing" {
  provider = aws.us_east_1
  name     = "${local.name}-billing-alerts"
}

resource "aws_sns_topic_subscription" "billing_email" {
  provider  = aws.us_east_1
  count     = var.admin_email == "" ? 0 : 1
  topic_arn = aws_sns_topic.billing.arn
  protocol  = "email"
  endpoint  = var.admin_email
}

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

  dimensions = {
    Currency = "USD"
  }

  alarm_actions = [aws_sns_topic.billing.arn]

  tags = { Name = "${local.name}-billing-alarm" }
}
