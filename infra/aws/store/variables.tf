variable "name" {
  description = "Names the store's resources (the bucket, the roles): ynr-store-<name>"
  type        = string
  default     = "main"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,20}$", var.name))
    error_message = "name is lowercase letters, digits and hyphens, starting with a letter, at most 21 characters."
  }
}

variable "bucket_name" {
  description = "Overrides the bucket's name, ynr-store-<name>-<account id>"
  type        = string
  default     = null
}

variable "versioning" {
  description = <<-EOT
    Keep object versions. A compaction or an erasure then never loses the only copy at once; the
    noncurrent versions expire after noncurrent_version_days, so an erased object does not linger
    (ADR-005, Erasure). Off, a delete is final.
  EOT
  type        = bool
  default     = true
}

variable "noncurrent_version_days" {
  description = "Days a noncurrent version is kept when versioning is on"
  type        = number
  default     = 1

  validation {
    condition     = var.noncurrent_version_days >= 1
    error_message = "noncurrent_version_days is at least 1."
  }
}

variable "force_destroy" {
  description = "Let terraform destroy delete a bucket that still holds objects. Off: the bucket is protected from destroy."
  type        = bool
  default     = false
}

# Retention (NFR-15). One lifecycle rule per prefix, so each can differ. The defaults are central's
# NFR-15 numbers.

variable "retention_days" {
  description = <<-EOT
    Days to keep objects under each prefix, counted from when the object was written.

    traces, logs, metrics: collectors' batches, a backstop, since central deletes a batch once its
    hour is compacted. compacted_*: the Parquet parts and manifests of each signal. index: the item
    index. rollups: daily and monthly aggregates (13 months, as 396 days). leases: central's
    compaction leases, which are stale once their hour is done. registries are never expired.

    Events are log records, so they live under logs/ and take logs' retention. NFR-15 gives events
    90 days and traces and logs 30, and one prefix cannot hold both: the default is the narrowest
    reading, 30 days, and raising logs and compacted_logs to 90 keeps events at the price of
    keeping all logs that long.
  EOT
  type = object({
    traces            = optional(number, 30)
    logs              = optional(number, 30)
    metrics           = optional(number, 30)
    compacted_traces  = optional(number, 30)
    compacted_logs    = optional(number, 30)
    compacted_metrics = optional(number, 30)
    index             = optional(number, 30)
    rollups           = optional(number, 396)
    leases            = optional(number, 7)
  })
  default = {}

  validation {
    condition     = alltrue([for k, v in var.retention_days : v >= 1])
    error_message = "Every retention is at least 1 day."
  }
}

# Who writes and who reads

variable "collectors" {
  description = <<-EOT
    The collectors that write to the store (ADR-003: a runner pool or a host), by collector id. Each
    gets one role, which can write only its own segments and which the principals listed can
    assume. A principal is an IAM role, user or account ARN; a runner pool or host assumes the role
    with its own credentials.
  EOT
  type = map(object({
    trusted_principals = list(string)
  }))
  default = {}

  validation {
    condition     = alltrue([for id, c in var.collectors : can(regex("^[a-z0-9][a-z0-9._-]{0,62}$", id))])
    error_message = "A collector id matches ^[a-z0-9][a-z0-9._-]{0,62}$ (ADR-005)."
  }

  validation {
    condition     = alltrue([for id, c in var.collectors : length(c.trusted_principals) > 0])
    error_message = "Every collector needs at least one trusted principal."
  }

  validation {
    condition     = alltrue([for id, c in var.collectors : length("ynr-${var.name}-collector-${id}") <= 64])
    error_message = "ynr-<name>-collector-<id> is a role name, at most 64 characters: shorten the collector id or name."
  }
}

variable "central_trusted_principals" {
  description = "IAM role, user or account ARNs that may assume the central role, which reads everything and writes compacted/, rollups/, index/ and leases/"
  type        = list(string)

  validation {
    condition     = length(var.central_trusted_principals) > 0
    error_message = "central_trusted_principals needs at least one principal."
  }
}

variable "max_session_seconds" {
  description = "Longest session of any role this module makes"
  type        = number
  default     = 3600

  validation {
    condition     = var.max_session_seconds >= 3600 && var.max_session_seconds <= 43200
    error_message = "max_session_seconds is between 3600 and 43200."
  }
}
