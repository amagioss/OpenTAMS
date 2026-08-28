variable "namespace" {
  description = "Namespace the materialized Secret is created in (the OpenTAMS namespace)."
  type        = string
}

variable "secret_name" {
  description = "Name of the SLV CR and the resulting native Secret."
  type        = string
}

variable "secret_data" {
  description = "Map of secret key -> plaintext value to seal (e.g. { DB_PASSWORD = \"...\" })."
  type        = map(string)
  sensitive   = true
}

variable "slv_env_public_key" {
  description = "SLV environment Public Key (SLV_EPK_...) used to seal. Required when use_slv = true."
  type        = string
  default     = ""
}

variable "slv_env_secret_key" {
  description = "SLV environment Secret Key (SLV_ESK_...) given to the operator bootstrap Secret. Required when use_slv = true and install_operator = true."
  type        = string
  sensitive   = true
  default     = ""
}

variable "use_slv" {
  description = "Set false to skip SLV entirely. You must pre-create the secret named var.secret_name in var.namespace."
  type        = bool
  default     = true
}

variable "install_operator" {
  description = "Set false to skip installing the SLV operator (use an existing installation). Only used when use_slv = true."
  type        = bool
  default     = true
}

variable "operator_namespace" {
  description = "Namespace for the slv-operator."
  type        = string
  default     = "slv"
}

variable "operator_chart_version" {
  description = "slv/slv-operator chart version. Empty = latest."
  type        = string
  default     = ""
}

variable "kubeconfig_path" {
  type    = string
  default = "~/.kube/config"
}

variable "kube_context" {
  type    = string
  default = ""
}
