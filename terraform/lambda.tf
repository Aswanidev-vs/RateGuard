data "archive_file" "cost_lambda" {
  type        = "zip"
  source_file = pathexpand(var.lambda_zip_path)
  output_path = "${path.module}/rateguard-cost-lambda.zip"
}

resource "aws_lambda_function" "cost" {
  function_name    = "${local.name}-cost-anomaly"
  description      = "Hourly AWS spend anomaly detection and SNS alerting"
  role             = aws_iam_role.lambda.arn
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  filename         = data.archive_file.cost_lambda.output_path
  source_code_hash = data.archive_file.cost_lambda.output_base64sha256
  timeout          = 60
  memory_size      = 256

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

  depends_on = [aws_iam_role_policy.lambda, aws_cloudwatch_log_group.cost]
}

resource "aws_cloudwatch_event_rule" "cost_hourly" {
  name                = "${local.name}-cost-hourly"
  description         = "Run the RateGuard cost anomaly detector every hour"
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
