# ---------------------------------------------------------------------------
# Cluster access (existing EKS cluster — Terraform does NOT create one)
# ---------------------------------------------------------------------------
variable "region" {
  description = "AWS region for RDS, S3 and IAM."
  type        = string
}

variable "kubeconfig_path" {
  description = "Path to the kubeconfig used to reach the existing cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "kubeconfig context to use. Empty = current-context."
  type        = string
  default     = ""
}

variable "eks_cluster_name" {
  description = "Name of the existing EKS cluster (used to discover its OIDC issuer for IRSA)."
  type        = string
}

variable "resource_name_prefix" {
  description = "Fixed leading prefix for AWS resource names (IAM role/policy, RDS, SG). Lets a scoped deployer IAM policy match <prefix>-*. The cluster name is appended for uniqueness."
  type        = string
  default     = "opentams"
}

variable "default_tags" {
  description = "Tags applied to every AWS resource created by this module (cost allocation, ownership)."
  type        = map(string)
  default = {
    ManagedBy = "terraform"
    Project   = "opentams"
  }
}

# ---------------------------------------------------------------------------
# IRSA / OIDC
# ---------------------------------------------------------------------------
variable "create_oidc_provider" {
  description = "Create the IAM OIDC provider for the cluster. Set false if one already exists (e.g. created by eksctl)."
  type        = bool
  default     = false
}

variable "existing_oidc_provider_arn" {
  description = "ARN of an already-existing IAM OIDC provider. Required when create_oidc_provider = false."
  type        = string
  default     = ""
}

# ---------------------------------------------------------------------------
# Networking for RDS
# ---------------------------------------------------------------------------
variable "db_subnet_ids" {
  description = "Subnet IDs (>=2 AZs) for the RDS subnet group. Use subnets in the same VPC as the cluster nodes."
  type        = list(string)
}

variable "db_ingress_security_group_ids" {
  description = "Security group IDs allowed to reach Postgres on 5432 (typically the EKS node/cluster SG)."
  type        = list(string)
  default     = []
}

variable "db_ingress_cidr_blocks" {
  description = "Additional CIDR blocks allowed to reach Postgres on 5432."
  type        = list(string)
  default     = []
}

# ---------------------------------------------------------------------------
# Database (RDS Postgres)
# ---------------------------------------------------------------------------
variable "db_name" {
  type    = string
  default = "opentams"
}

variable "db_user" {
  type    = string
  default = "opentams"
}

variable "db_engine_version" {
  type    = string
  default = "16"
}

variable "db_instance_class" {
  type    = string
  default = "db.t3.micro"
}

variable "db_allocated_storage" {
  type    = number
  default = 20
}

variable "db_multi_az" {
  description = "Run RDS across two availability zones. Defaults to true: this module is published, so it defaults to the production-safe value. Set to false for a throwaway dev stack to halve the instance cost."
  type        = bool
  default     = true
}

variable "db_skip_final_snapshot" {
  description = "Destroy the database without taking a final snapshot. Defaults to false, which also enables deletion protection (see `deletion_protection` on aws_db_instance). Set to true only for a dev stack you intend to `terraform destroy`."
  type        = bool
  default     = false
}

variable "db_ssl_mode" {
  description = "DB_SSLMODE passed to OpenTAMS. RDS requires TLS; keep 'require' or stricter."
  type        = string
  default     = "require"
}

variable "db_backup_retention_days" {
  description = "Automated backup retention in days (0 disables backups)."
  type        = number
  default     = 7
}

variable "db_storage_type" {
  description = "RDS storage type. Use gp3 for better baseline performance at lower cost."
  type        = string
  default     = "gp3"
}

variable "db_performance_insights" {
  description = "Enable RDS Performance Insights (not supported on db.t3.micro)."
  type        = bool
  default     = false
}


# ---------------------------------------------------------------------------
# Object store (S3)
# ---------------------------------------------------------------------------
variable "bucket_name" {
  description = "S3 bucket name for media objects (globally unique)."
  type        = string
}

variable "bucket_force_destroy" {
  description = "Allow Terraform to delete a non-empty bucket on destroy."
  type        = bool
  default     = false
}

variable "bucket_kms_key_arn" {
  description = "Customer-managed KMS key ARN for S3 SSE. Empty = SSE-S3 (AES256). When set, the IRSA role is also granted kms:Decrypt/GenerateDataKey on it."
  type        = string
  default     = ""
}

# ---------------------------------------------------------------------------
# Kubernetes placement
# ---------------------------------------------------------------------------
variable "namespace" {
  description = "Namespace OpenTAMS (and its Secret) are deployed into."
  type        = string
  default     = "opentams"
}

variable "namespace_labels" {
  description = "Additional labels for the OpenTAMS namespace. Use to match your Prometheus serviceMonitorNamespaceSelector (e.g. {\"monitoring\" = \"true\"})."
  type        = map(string)
  default     = {}
}

variable "service_account_name" {
  description = "ServiceAccount name OpenTAMS runs as (bound to the IRSA role)."
  type        = string
  default     = "opentams"
}

variable "secret_name" {
  description = "Name of the Secret SLV materializes and the chart consumes (secrets.existingSecret)."
  type        = string
  default     = "opentams-secrets"
}

# ---------------------------------------------------------------------------
# SLV (secrets operator)
# ---------------------------------------------------------------------------
variable "use_slv" {
  description = "Set false to bypass SLV entirely. You must pre-create the Kubernetes Secret named var.secret_name in var.namespace before running tofu apply."
  type        = bool
  default     = true
}

variable "slv_env_public_key" {
  description = "SLV environment Public Key (SLV_EPK_...) used to seal secrets. Required when use_slv = true."
  type        = string
  default     = ""
}

variable "slv_env_secret_key" {
  description = "SLV environment Secret Key (SLV_ESK_...) handed to the operator. Required when use_slv = true and slv_install_operator = true. Pass via TF_VAR_slv_env_secret_key — never commit to git."
  type        = string
  sensitive   = true
  default     = ""
}

variable "slv_install_operator" {
  description = "Set false to skip installing the SLV operator (use an existing installation). Only used when use_slv = true."
  type        = bool
  default     = true
}

variable "slv_operator_namespace" {
  description = "Namespace to install the SLV operator into."
  type        = string
  default     = "slv"
}

variable "slv_operator_chart_version" {
  description = "Version of the slv/slv-operator Helm chart. Empty = latest."
  type        = string
  default     = ""
}

# ---------------------------------------------------------------------------
# OpenTAMS application
# ---------------------------------------------------------------------------
variable "image_repository" {
  type    = string
  default = "ghcr.io/amagioss/opentams"
}

variable "image_tag" {
  description = "Image tag. Empty = chart appVersion."
  type        = string
  default     = ""
}

variable "opentams_chart_path" {
  description = "Path to the local OpenTAMS Helm chart."
  type        = string
  default     = ""
}

variable "auth_external_issuer_url" {
  description = "AUTH_EXTERNAL_ISSUER_URL — your existing OIDC issuer."
  type        = string
}

variable "auth_external_audience" {
  description = "AUTH_EXTERNAL_AUDIENCE."
  type        = string
}

variable "extra_values" {
  description = "Optional raw Helm values (YAML string) merged last, for overrides like ingress/autoscaling."
  type        = string
  default     = ""
}
