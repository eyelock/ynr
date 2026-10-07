# The store (ADR-005): one S3 bucket for collectors' batches and registries and central's compacted
# parts, rollups, item index and leases. Collectors write only their own segments; central reads
# everything and writes the rest. Laid out as the ADR's "Layout" section describes, at the
# bucket's root, so the store URL is s3://<bucket>?region=<region>.

data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  bucket     = coalesce(var.bucket_name, "ynr-store-${var.name}-${data.aws_caller_identity.current.account_id}")
  signals    = ["traces", "logs", "metrics"]
  store_url  = "s3://${aws_s3_bucket.store.bucket}?region=${data.aws_region.current.region}"
  bucket_arn = aws_s3_bucket.store.arn

  # What only central writes. Collectors are denied all of it.
  central_prefixes = ["compacted", "rollups", "index", "leases"]
}

resource "aws_s3_bucket" "store" {
  bucket        = local.bucket
  force_destroy = var.force_destroy

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_ownership_controls" "store" {
  bucket = aws_s3_bucket.store.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "store" {
  bucket = aws_s3_bucket.store.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "store" {
  bucket = aws_s3_bucket.store.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }

    # AWS sets these on new buckets; declared so the plan does not try to remove them.
    blocked_encryption_types = ["SSE-C"]
    bucket_key_enabled       = false
  }
}

resource "aws_s3_bucket_versioning" "store" {
  bucket = aws_s3_bucket.store.id

  versioning_configuration {
    status = var.versioning ? "Enabled" : "Suspended"
  }
}

# Refuse plain HTTP. Nothing else is in the bucket policy: access is by the roles below.
data "aws_iam_policy_document" "bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [local.bucket_arn, "${local.bucket_arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "store" {
  bucket = aws_s3_bucket.store.id
  policy = data.aws_iam_policy_document.bucket.json

  depends_on = [aws_s3_bucket_public_access_block.store]
}

# Retention is the bucket's job on S3: central does not run store.Retain here. One rule per prefix.
locals {
  expire_prefixes = merge(
    { for s in local.signals : "batches-${s}" => { prefix = "${s}/", days = var.retention_days[s] } },
    { for s in local.signals : "compacted-${s}" => { prefix = "compacted/${s}/", days = var.retention_days["compacted_${s}"] } },
    {
      index   = { prefix = "index/", days = var.retention_days.index }
      rollups = { prefix = "rollups/", days = var.retention_days.rollups }
      leases  = { prefix = "leases/", days = var.retention_days.leases }
    },
  )
}

resource "aws_s3_bucket_lifecycle_configuration" "store" {
  bucket = aws_s3_bucket.store.id

  dynamic "rule" {
    for_each = local.expire_prefixes

    content {
      id     = "expire-${rule.key}"
      status = "Enabled"

      filter {
        prefix = rule.value.prefix
      }

      expiration {
        days = rule.value.days
      }
    }
  }

  # Whole bucket: half-finished uploads, and when versioning is on, old versions and the delete
  # markers left once nothing is under them.
  rule {
    id     = "clean-up"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }

    dynamic "noncurrent_version_expiration" {
      for_each = var.versioning ? [1] : []

      content {
        noncurrent_days = var.noncurrent_version_days
      }
    }

    dynamic "expiration" {
      for_each = var.versioning ? [1] : []

      content {
        expired_object_delete_marker = true
      }
    }
  }

  depends_on = [aws_s3_bucket_versioning.store]
}
