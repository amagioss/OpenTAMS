provider "aws" {
  region = var.region
  default_tags {
    tags = var.default_tags
  }
}

# Both the kubernetes and helm providers target the cluster you already have
# access to from your laptop (kubeconfig). This keeps the Kubernetes-side of the
# deployment cloud-agnostic — only the managed-service modules below are AWS.
provider "kubernetes" {
  config_path    = var.kubeconfig_path
  config_context = var.kube_context
}

provider "helm" {
  kubernetes {
    config_path    = var.kubeconfig_path
    config_context = var.kube_context
  }
}
