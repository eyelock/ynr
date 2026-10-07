output "bucket" {
  description = "The bucket's name"
  value       = aws_s3_bucket.store.bucket
}

output "bucket_arn" {
  description = "The bucket's ARN"
  value       = aws_s3_bucket.store.arn
}

output "store_url" {
  description = "The store URL for ynr serve, ynr central and ynr query: --store <this>"
  value       = local.store_url
}

output "collector_role_arns" {
  description = "Each collector's role ARN, by collector id"
  value       = { for id, r in aws_iam_role.collector : id => r.arn }
}

output "central_role_arn" {
  description = "Central's role ARN"
  value       = aws_iam_role.central.arn
}
