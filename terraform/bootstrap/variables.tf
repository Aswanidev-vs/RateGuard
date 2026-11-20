variable "region" {
  description = "Region for the state bucket and lock table."
  type        = string
  default     = "ap-south-1"
}

variable "bucket_name" {
  description = "Globally unique base name for the state bucket. A random suffix is appended."
  type        = string
  default     = "rateguard-tfstate"
}

variable "lock_table" {
  description = "DynamoDB table used for state locking."
  type        = string
  default     = "rateguard-tflock"
}
