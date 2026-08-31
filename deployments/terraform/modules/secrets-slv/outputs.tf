output "secret_name" {
  description = "Name of the Secret for OpenTAMS to consume (SLV-materialized or user-provided)."
  value       = var.secret_name
  depends_on  = [null_resource.seal_and_apply]
}

output "operator_namespace" {
  value = var.operator_namespace
}
