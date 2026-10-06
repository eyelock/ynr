variable "account_id" {
  description = "AWS account the store is in; a run with credentials for any other account fails"
  type        = string
}

variable "region" {
  description = "Region of the bucket"
  type        = string
  default     = "us-east-1"
}

variable "owner" {
  description = "Owner tag on every resource"
  type        = string
}

variable "name" {
  description = "Names the store's resources: ynr-store-<name>"
  type        = string
  default     = "main"
}

variable "versioning" {
  description = "Keep object versions (noncurrent versions expire after a day)"
  type        = bool
  default     = true
}

variable "retention_days" {
  description = "Days to keep objects per prefix; unset ones take NFR-15's central defaults. See store/variables.tf."
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
}

variable "collectors" {
  description = "Collector id to the principals that may assume its role"
  type = map(object({
    trusted_principals = list(string)
  }))
  default = {}
}

variable "central_trusted_principals" {
  description = "Principals that may assume the central role"
  type        = list(string)
}
