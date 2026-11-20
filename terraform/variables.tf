variable "aws_region" {
  description = "Primary AWS region. ap-south-1 keeps the free-tier footprint near Mumbai."
  type        = string
  default     = "ap-south-1"
}

variable "project" {
  description = "Resource name prefix."
  type        = string
  default     = "rateguard"
}

variable "environment" {
  description = "Environment label used in names and tags."
  type        = string
  default     = "dev"
}

variable "instance_type" {
  description = "EC2 instance type (t3.micro is free-tier eligible)."
  type        = string
  default     = "t3.micro"
}

variable "admin_api_key" {
  description = "Admin key for the API and dashboard. Stored as an SSM SecureString; also present in Terraform state."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.admin_api_key) >= 16
    error_message = "admin_api_key must be at least 16 characters."
  }
}

variable "session_secret" {
  description = "Dashboard session HMAC secret. When null, Terraform generates one. Stored as an SSM SecureString."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.session_secret == null || length(var.session_secret) >= 32
    error_message = "session_secret must be at least 32 characters when supplied."
  }
}

variable "admin_email" {
  description = "Optional email subscribed to both SNS topics."
  type        = string
  default     = ""
}

variable "domain_name" {
  description = "Optional DNS name for Caddy/HTTPS. Empty means plain HTTP on the EC2 public IP."
  type        = string
  default     = ""
}

variable "ssm_prefix" {
  description = "SSM Parameter Store prefix consumed by the app."
  type        = string
  default     = "/rateguard"
}

variable "alarm_threshold_usd" {
  description = "Estimated AWS charges (USD) that trigger the billing alarm."
  type        = number
  default     = 1
}

variable "app_image_tag" {
  description = "ECR image tag provisioned on EC2 at boot. CI/deploy.sh overrides it later."
  type        = string
  default     = "latest"
}

variable "allowed_ssh_cidr" {
  description = "Optional CIDR allowed to SSH. Empty disables inbound SSH entirely."
  type        = string
  default     = ""
}

variable "deletion_protection" {
  description = "Enable DynamoDB deletion protection."
  type        = bool
  default     = false
}

variable "force_destroy_buckets" {
  description = "Allow Terraform to delete non-empty buckets on destroy."
  type        = bool
  default     = true
}

variable "lambda_zip_path" {
  description = "Path to the linux/arm64 Go Lambda bootstrap file. Build it before apply."
  type        = string
  default     = "../build/bootstrap"
}

variable "log_retention_days" {
  description = "CloudWatch log retention for the cost Lambda."
  type        = number
  default     = 7
}
