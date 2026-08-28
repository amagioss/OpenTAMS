output "db_endpoint" {
  description = "RDS Postgres endpoint (host)."
  value       = aws_db_instance.this.address
}

output "db_port" {
  value = aws_db_instance.this.port
}

output "bucket_name" {
  value = aws_s3_bucket.media.bucket
}

output "irsa_role_arn" {
  description = "IAM role assumed by the OpenTAMS ServiceAccount."
  value       = aws_iam_role.opentams.arn
}

output "oidc_provider_arn" {
  value = local.oidc_arn
}

output "secret_name" {
  description = "Kubernetes Secret materialized by SLV and consumed by the chart."
  value       = module.secrets_slv.secret_name
}

output "helm_release" {
  value = module.opentams.release_name
}
