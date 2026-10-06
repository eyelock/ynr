output "store_url" {
  description = "The store URL: --store <this>"
  value       = module.store.store_url
}

output "bucket" {
  description = "The bucket's name"
  value       = module.store.bucket
}

output "bucket_arn" {
  description = "The bucket's ARN"
  value       = module.store.bucket_arn
}

output "collector_role_arns" {
  description = "Each collector's role ARN, by collector id"
  value       = module.store.collector_role_arns
}

output "central_role_arn" {
  description = "Central's role ARN"
  value       = module.store.central_role_arn
}
