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

  tags = { Name = local.tenants_table }
}

resource "aws_dynamodb_table" "usage" {
  name                        = local.usage_table
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "tenant_id"
  range_key                   = "bucket"
  deletion_protection_enabled = var.deletion_protection

  attribute {
    name = "tenant_id"
    type = "S"
  }

  attribute {
    name = "bucket"
    type = "S"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  server_side_encryption {
    enabled = true
  }

  tags = { Name = local.usage_table }
}

resource "aws_dynamodb_table" "anomalies" {
  name                        = local.anomalies_table
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "tenant_id"
  range_key                   = "detected_at"
  deletion_protection_enabled = var.deletion_protection

  attribute {
    name = "tenant_id"
    type = "S"
  }

  attribute {
    name = "detected_at"
    type = "S"
  }

  server_side_encryption {
    enabled = true
  }

  tags = { Name = local.anomalies_table }
}
