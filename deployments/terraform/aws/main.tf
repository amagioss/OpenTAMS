# Existing cluster — used only to discover the OIDC issuer for IRSA.
data "aws_eks_cluster" "this" {
  name = var.eks_cluster_name
}

locals {
  oidc_issuer = data.aws_eks_cluster.this.identity[0].oidc[0].issuer
  oidc_host   = replace(local.oidc_issuer, "https://", "")
  oidc_arn    = var.create_oidc_provider ? aws_iam_openid_connect_provider.this[0].arn : var.existing_oidc_provider_arn
  sa_subject  = "system:serviceaccount:${var.namespace}:${var.service_account_name}"

  chart_path = var.opentams_chart_path != "" ? var.opentams_chart_path : "${path.module}/../../helm/opentams"
}

# ---------------------------------------------------------------------------
# IAM OIDC provider (optional — reuse an existing one when create=false)
# ---------------------------------------------------------------------------
data "tls_certificate" "oidc" {
  count = var.create_oidc_provider ? 1 : 0
  url   = local.oidc_issuer
}

resource "aws_iam_openid_connect_provider" "this" {
  count           = var.create_oidc_provider ? 1 : 0
  url             = local.oidc_issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.oidc[0].certificates[0].sha1_fingerprint]
}

# ---------------------------------------------------------------------------
# S3 object store
# ---------------------------------------------------------------------------
resource "aws_s3_bucket" "media" {
  bucket        = var.bucket_name
  force_destroy = var.bucket_force_destroy
}

resource "aws_s3_bucket_public_access_block" "media" {
  bucket                  = aws_s3_bucket.media.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = var.bucket_kms_key_arn != "" ? "aws:kms" : "AES256"
      kms_master_key_id = var.bucket_kms_key_arn != "" ? var.bucket_kms_key_arn : null
    }
    bucket_key_enabled = var.bucket_kms_key_arn != ""
  }
}

resource "aws_s3_bucket_versioning" "media" {
  bucket = aws_s3_bucket.media.id
  versioning_configuration {
    status = "Enabled"
  }
}

# ---------------------------------------------------------------------------
# IRSA role for OpenTAMS -> S3
# ---------------------------------------------------------------------------
data "aws_iam_policy_document" "s3_access" {
  statement {
    sid       = "ListBucket"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.media.arn]
  }
  statement {
    sid = "ObjectRW"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:AbortMultipartUpload",
      "s3:ListMultipartUploadParts",
    ]
    # Object-level wildcard is required: the app reads/writes arbitrary media
    # keys within this single bucket. Scope is the bucket, not account-wide.
    resources = ["${aws_s3_bucket.media.arn}/*"]
  }

  # Only needed when the bucket is encrypted with a customer-managed KMS key.
  dynamic "statement" {
    for_each = var.bucket_kms_key_arn != "" ? [1] : []
    content {
      sid       = "KMS"
      actions   = ["kms:Decrypt", "kms:GenerateDataKey"]
      resources = [var.bucket_kms_key_arn]
    }
  }
}

resource "aws_iam_policy" "s3_access" {
  name   = "${var.resource_name_prefix}-${var.eks_cluster_name}-s3"
  policy = data.aws_iam_policy_document.s3_access.json
}

data "aws_iam_policy_document" "assume_role" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"
    principals {
      type        = "Federated"
      identifiers = [local.oidc_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = [local.sa_subject]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "opentams" {
  name               = "${var.resource_name_prefix}-${var.eks_cluster_name}-irsa"
  assume_role_policy = data.aws_iam_policy_document.assume_role.json
}

resource "aws_iam_role_policy_attachment" "opentams_s3" {
  role       = aws_iam_role.opentams.name
  policy_arn = aws_iam_policy.s3_access.arn
}

# ---------------------------------------------------------------------------
# RDS Postgres
# ---------------------------------------------------------------------------
resource "random_password" "db" {
  length  = 32
  special = false
}

resource "aws_db_subnet_group" "this" {
  name       = "${var.resource_name_prefix}-${var.eks_cluster_name}"
  subnet_ids = var.db_subnet_ids
}

# Target-only SG: no egress rules (RDS does not initiate outbound for normal
# operation, so omitting egress removes the default allow-all).
resource "aws_security_group" "rds" {
  name        = "${var.resource_name_prefix}-${var.eks_cluster_name}-rds"
  description = "OpenTAMS Postgres access"
  vpc_id      = data.aws_eks_cluster.this.vpc_config[0].vpc_id
}

resource "aws_security_group_rule" "rds_from_sg" {
  for_each                 = toset(var.db_ingress_security_group_ids)
  type                     = "ingress"
  description              = "Postgres from allowed security group"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = aws_security_group.rds.id
  source_security_group_id = each.value
}

resource "aws_security_group_rule" "rds_from_cidr" {
  count             = length(var.db_ingress_cidr_blocks) > 0 ? 1 : 0
  type              = "ingress"
  description       = "Postgres from allowed CIDR blocks"
  from_port         = 5432
  to_port           = 5432
  protocol          = "tcp"
  security_group_id = aws_security_group.rds.id
  cidr_blocks       = var.db_ingress_cidr_blocks
}

resource "aws_db_instance" "this" {
  identifier                   = "${var.resource_name_prefix}-${var.eks_cluster_name}"
  engine                       = "postgres"
  engine_version               = var.db_engine_version
  instance_class               = var.db_instance_class
  allocated_storage            = var.db_allocated_storage
  storage_type                 = var.db_storage_type
  storage_encrypted            = true
  db_name                      = var.db_name
  username                     = var.db_user
  password                     = random_password.db.result
  db_subnet_group_name         = aws_db_subnet_group.this.name
  vpc_security_group_ids       = [aws_security_group.rds.id]
  multi_az                     = var.db_multi_az
  publicly_accessible          = false
  skip_final_snapshot          = var.db_skip_final_snapshot
  deletion_protection          = !var.db_skip_final_snapshot
  apply_immediately            = true
  backup_retention_period      = var.db_backup_retention_days
  auto_minor_version_upgrade   = true
  performance_insights_enabled = var.db_performance_insights
}

# ---------------------------------------------------------------------------
# Namespace (created before SLV CR + Helm release so both can target it)
# ---------------------------------------------------------------------------
resource "kubernetes_namespace" "opentams" {
  metadata {
    name   = var.namespace
    labels = var.namespace_labels
  }
}

# ---------------------------------------------------------------------------
# Secrets via SLV
# ---------------------------------------------------------------------------
module "secrets_slv" {
  source = "../modules/secrets-slv"

  use_slv                = var.use_slv
  namespace              = kubernetes_namespace.opentams.metadata[0].name
  secret_name            = var.secret_name
  slv_env_public_key     = var.slv_env_public_key
  slv_env_secret_key     = var.slv_env_secret_key
  install_operator       = var.slv_install_operator
  operator_namespace     = var.slv_operator_namespace
  operator_chart_version = var.slv_operator_chart_version
  kubeconfig_path        = var.kubeconfig_path
  kube_context           = var.kube_context

  secret_data = {
    DB_PASSWORD = random_password.db.result
  }
}

# ---------------------------------------------------------------------------
# OpenTAMS application
# ---------------------------------------------------------------------------
module "opentams" {
  source = "../modules/opentams"

  namespace            = kubernetes_namespace.opentams.metadata[0].name
  chart_path           = local.chart_path
  image_repository     = var.image_repository
  image_tag            = var.image_tag
  service_account_name = var.service_account_name
  irsa_role_arn        = aws_iam_role.opentams.arn
  existing_secret      = module.secrets_slv.secret_name

  db_host     = aws_db_instance.this.address
  db_port     = aws_db_instance.this.port
  db_name     = var.db_name
  db_user     = var.db_user
  db_ssl_mode = var.db_ssl_mode

  object_store_bucket   = aws_s3_bucket.media.bucket
  object_store_region   = var.region
  object_store_endpoint = "" # empty = AWS S3

  auth_external_issuer_url = var.auth_external_issuer_url
  auth_external_audience   = var.auth_external_audience

  extra_values = var.extra_values

  depends_on = [module.secrets_slv]
}
