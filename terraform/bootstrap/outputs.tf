output "state_bucket" {
  description = "Use as bucket in backend.hcl."
  value       = aws_s3_bucket.state.id
}

output "lock_table" {
  description = "Use as dynamodb_table in backend.hcl."
  value       = aws_dynamodb_table.lock.name
}

output "next_steps" {
  description = "Reminder for the operator."
  value       = "Create terraform/backend.hcl with bucket, key, region, encrypt and dynamodb_table, then run terraform init -backend-config=backend.hcl"
}
