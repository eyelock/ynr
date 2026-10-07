terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # State bucket from infra/terraform-state. The key is per deployment and comes from a gitignored
  # file: terraform init -backend-config=example.s3.tfbackend (copy example.s3.tfbackend.example).
  backend "s3" {
    use_lockfile = true
    encrypt      = true
  }
}

provider "aws" {
  region              = var.region
  allowed_account_ids = [var.account_id]

  default_tags {
    tags = {
      Project   = "ynr"
      Owner     = var.owner
      ManagedBy = "terraform"
    }
  }
}
