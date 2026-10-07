# One store: the bucket, a role per collector and central's role.

module "store" {
  source = "../store"

  name                       = var.name
  versioning                 = var.versioning
  retention_days             = var.retention_days
  collectors                 = var.collectors
  central_trusted_principals = var.central_trusted_principals
}
