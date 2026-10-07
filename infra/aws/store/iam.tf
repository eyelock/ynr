# Write identity (ADR-005, "Collectors can write only their own batches and registries").
#
# IAM's * matches across /, so a pattern such as */<id>/* would also match keys under compacted/ and
# other collectors' registries. Each collector's grant is therefore anchored per prefix, with an
# explicit deny on everything only central writes. A policy still cannot fix a key's depth
# (traces/*/<id>/* also matches traces/a/b/<id>/x), so the reader accepts only keys of the exact
# shape and ignores the rest. The collector id is validated to contain no IAM wildcard or policy
# variable characters.

data "aws_iam_policy_document" "collector_trust" {
  for_each = var.collectors

  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = each.value.trusted_principals
    }
  }
}

resource "aws_iam_role" "collector" {
  for_each = var.collectors

  name                 = "ynr-${var.name}-collector-${each.key}"
  description          = "ynr collector ${each.key}: writes its own batches and registries to ${local.bucket}"
  assume_role_policy   = data.aws_iam_policy_document.collector_trust[each.key].json
  max_session_duration = var.max_session_seconds

  tags = {
    Collector = each.key
  }
}

data "aws_iam_policy_document" "collector" {
  for_each = var.collectors

  # Writes only: a collector never reads the store. A conditional put (never overwrite) needs only
  # PutObject.
  statement {
    sid     = "WriteOwnSegments"
    actions = ["s3:PutObject", "s3:AbortMultipartUpload"]
    resources = concat(
      [for s in local.signals : "${local.bucket_arn}/${s}/*/${each.key}/*"],
      ["${local.bucket_arn}/registries/${each.key}/*"],
    )
  }

  statement {
    sid       = "DenyCentralPrefixes"
    effect    = "Deny"
    actions   = ["s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:AbortMultipartUpload"]
    resources = [for p in local.central_prefixes : "${local.bucket_arn}/${p}/*"]
  }
}

resource "aws_iam_role_policy" "collector" {
  for_each = var.collectors

  name   = "write-own-segments"
  role   = aws_iam_role.collector[each.key].id
  policy = data.aws_iam_policy_document.collector[each.key].json
}

# Central (ADR-005): reads everything, writes what compaction produces, and deletes what it
# supersedes: batches once their hour is compacted (and on erasure), and its own outputs.

data "aws_iam_policy_document" "central_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = var.central_trusted_principals
    }
  }
}

resource "aws_iam_role" "central" {
  name                 = "ynr-${var.name}-central"
  description          = "ynr central: reads ${local.bucket}, writes compacted parts, rollups, the index and leases"
  assume_role_policy   = data.aws_iam_policy_document.central_trust.json
  max_session_duration = var.max_session_seconds
}

data "aws_iam_policy_document" "central" {
  statement {
    sid       = "ReadEverything"
    actions   = ["s3:GetObject"]
    resources = ["${local.bucket_arn}/*"]
  }

  statement {
    sid       = "ListEverything"
    actions   = ["s3:ListBucket"]
    resources = [local.bucket_arn]
  }

  statement {
    sid       = "WriteAndDeleteOwnOutputs"
    actions   = ["s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload"]
    resources = [for p in local.central_prefixes : "${local.bucket_arn}/${p}/*"]
  }

  # Compaction prunes the batches it covers, and erasure deletes them. Never written, and never
  # the registries.
  statement {
    sid       = "DeleteBatches"
    actions   = ["s3:DeleteObject"]
    resources = [for s in local.signals : "${local.bucket_arn}/${s}/*"]
  }
}

resource "aws_iam_role_policy" "central" {
  name   = "read-all-write-outputs"
  role   = aws_iam_role.central.id
  policy = data.aws_iam_policy_document.central.json
}
