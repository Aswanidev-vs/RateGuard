# Configure the S3 backend at init time with -backend-config=backend.hcl.
# See docs/terraform/TERRAFORM-GUIDE.md. The bucket and lock table come from
# the one-time bootstrap stack in terraform/bootstrap/.
terraform {
  backend "s3" {}
}
