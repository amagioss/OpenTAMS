terraform {
  required_providers {
    helm       = { source = "hashicorp/helm" }
    kubernetes = { source = "hashicorp/kubernetes" }
    null       = { source = "hashicorp/null" }
  }
}

# Operator namespace + bootstrap Secret holding the env Secret Key. The operator
# uses this private key to decrypt SLV CRs into native Secrets.
resource "kubernetes_namespace" "slv" {
  count = var.use_slv && var.install_operator ? 1 : 0
  metadata {
    name = var.operator_namespace
  }
}

resource "kubernetes_secret" "slv_bootstrap" {
  count = var.use_slv && var.install_operator ? 1 : 0
  metadata {
    name      = "slv"
    namespace = var.operator_namespace
  }
  data = {
    SecretKey = var.slv_env_secret_key
  }
  type = "Opaque"
}

resource "helm_release" "slv_operator" {
  count      = var.use_slv && var.install_operator ? 1 : 0
  name       = "slv"
  repository = "https://slv.sh/charts"
  chart      = "slv-operator"
  version    = var.operator_chart_version != "" ? var.operator_chart_version : null
  namespace  = var.operator_namespace
  depends_on = [kubernetes_secret.slv_bootstrap]
}

# Seal each secret into an SLV CR and apply it. The operator then reconciles the
# CR into a native Secret named `var.secret_name` in `var.namespace`.
# Re-runs whenever the secret values, public key, or target identity change.
resource "null_resource" "seal_and_apply" {
  count = var.use_slv ? 1 : 0

  triggers = {
    secret_name = var.secret_name
    namespace   = var.namespace
    public_key  = var.slv_env_public_key
    data_hash   = sha256(jsonencode(var.secret_data))
  }

  provisioner "local-exec" {
    interpreter = ["/bin/bash", "-c"]
    command     = "${path.module}/seal.sh"
    environment = {
      SLV_PUBLIC_KEY = var.slv_env_public_key
      SECRET_NAME    = var.secret_name
      NAMESPACE      = var.namespace
      SECRET_JSON    = jsonencode(var.secret_data)
      KUBECONFIG     = var.kubeconfig_path
      KUBE_CONTEXT   = var.kube_context
    }
  }

  depends_on = [helm_release.slv_operator]
}
