variable "namespace" { type = string }
variable "release_name" {
  type    = string
  default = "opentams"
}
variable "chart_path" { type = string }

variable "image_repository" { type = string }
variable "image_tag" {
  type    = string
  default = ""
}

variable "service_account_name" { type = string }
variable "irsa_role_arn" { type = string }
variable "existing_secret" { type = string }

variable "db_host" { type = string }
variable "db_port" { type = number }
variable "db_name" { type = string }
variable "db_user" { type = string }
variable "db_ssl_mode" {
  type    = string
  default = "require"
}

variable "object_store_bucket" { type = string }
variable "object_store_region" { type = string }
variable "object_store_endpoint" {
  type    = string
  default = ""
}

variable "auth_external_issuer_url" { type = string }
variable "auth_external_audience" { type = string }

variable "extra_values" {
  type    = string
  default = ""
}
