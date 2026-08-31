terraform {
  required_providers {
    helm = { source = "hashicorp/helm" }
  }
}

locals {
  values = {
    image = {
      repository = var.image_repository
      tag        = var.image_tag
    }
    app = {
      env = "production"
    }
    serviceAccount = {
      create                       = true
      name                         = var.service_account_name
      automountServiceAccountToken = false
      annotations = {
        "eks.amazonaws.com/role-arn" = var.irsa_role_arn
      }
    }
    database = {
      host    = var.db_host
      port    = var.db_port
      name    = var.db_name
      user    = var.db_user
      sslMode = var.db_ssl_mode
    }
    objectStore = {
      bucket   = var.object_store_bucket
      region   = var.object_store_region
      endpoint = var.object_store_endpoint
      backend = {
        provider = "s3"
        product  = "s3"
      }
    }
    auth = {
      external = {
        issuerURL = var.auth_external_issuer_url
        audience  = var.auth_external_audience
      }
    }
    # Under IRSA the chart only needs DB_PASSWORD in the Secret; object-store
    # auth comes from the role bound to the ServiceAccount.
    secrets = {
      existingSecret = var.existing_secret
    }
  }
}

resource "helm_release" "opentams" {
  name            = var.release_name
  chart           = var.chart_path
  namespace       = var.namespace
  cleanup_on_fail = true

  # extra_values is applied last so it overrides the computed values.
  values = compact([yamlencode(local.values), var.extra_values])
}
