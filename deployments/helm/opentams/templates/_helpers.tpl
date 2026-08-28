{{/*
Expand the name of the chart.
*/}}
{{- define "opentams.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "opentams.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "opentams.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "opentams.labels" -}}
helm.sh/chart: {{ include "opentams.chart" . }}
{{ include "opentams.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "opentams.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opentams.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name to use.
*/}}
{{- define "opentams.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "opentams.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image reference. Tag defaults to .Chart.AppVersion.
*/}}
{{- define "opentams.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/*
opentams.validate — cross-field validation that JSON Schema can't express.
Fails the render with a clear message when constraints are violated.
*/}}
{{- define "opentams.validate" -}}
{{- if not .Values.secrets.existingSecret -}}
{{- fail "secrets.existingSecret is required: the chart never creates a Secret; provision it out-of-band (IaC/ESO/Vault/SealedSecrets) and set secrets.existingSecret." -}}
{{- end -}}
{{- if not .Values.database.host -}}
{{- fail "database.host is required." -}}
{{- end -}}
{{- if not .Values.objectStore.bucket -}}
{{- fail "objectStore.bucket is required." -}}
{{- end -}}
{{- if not .Values.objectStore.region -}}
{{- fail "objectStore.region is required." -}}
{{- end -}}
{{- if not .Values.objectStore.backend.provider -}}
{{- fail "objectStore.backend.provider is required." -}}
{{- end -}}
{{- if ne .Values.app.env "development" -}}
  {{- if not .Values.auth.external.issuerURL -}}
  {{- fail "auth.external.issuerURL is required when app.env != development." -}}
  {{- end -}}
  {{- if not .Values.auth.external.audience -}}
  {{- fail "auth.external.audience is required when app.env != development." -}}
  {{- end -}}
{{- end -}}
{{- if .Values.auth.internal.enabled -}}
  {{- if or (not .Values.auth.internal.issuerURL) (not .Values.auth.internal.audience) -}}
  {{- fail "auth.internal requires BOTH issuerURL and audience when enabled (both-or-neither)." -}}
  {{- end -}}
  {{- if not .Values.auth.internal.allowedSubjects -}}
  {{- fail "auth.internal.allowedSubjects is required when internal auth is enabled (fails closed; list the permitted ServiceAccount subjects)." -}}
  {{- end -}}
{{- end -}}
{{- if .Values.server.tls.enabled -}}
  {{- if not .Values.server.tls.existingSecret -}}
  {{- fail "server.tls.existingSecret is required when server.tls.enabled." -}}
  {{- end -}}
{{- end -}}
{{- if .Values.networkPolicy.enabled -}}
  {{- if and (not .Values.networkPolicy.ingressNamespaceSelector) (not .Values.networkPolicy.monitoringNamespaceSelector) -}}
  {{- fail "networkPolicy.enabled requires at least one of ingressNamespaceSelector / monitoringNamespaceSelector: an empty `from` would allow traffic from ALL sources, defeating default-deny." -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
opentams.env — emits a complete `env:` list. Call with the root context:
    env:
      {{- include "opentams.env" . | nindent <n> }}
Single source of truth shared by the Deployment and the migrate Job (no drift).
*/}}
{{- define "opentams.env" -}}
{{- /* 1. Non-secret config: ENV_NAME -> value (defaults live in values.yaml). */ -}}
{{- $cfg := dict
    "APP_ENV"                          .Values.app.env
    "LOG_LEVEL"                        .Values.app.logLevel
    "SERVER_PORT"                      (.Values.server.port | toString)
    "DB_HOST"                          .Values.database.host
    "DB_PORT"                          (.Values.database.port | toString)
    "DB_NAME"                          .Values.database.name
    "DB_USER"                          .Values.database.user
    "DB_SSLMODE"                       .Values.database.sslMode
    "DB_POOL_MIN"                      (.Values.database.pool.min | toString)
    "DB_POOL_MAX"                      (.Values.database.pool.max | toString)
    "OBJECT_STORE_BUCKET"              .Values.objectStore.bucket
    "OBJECT_STORE_REGION"              .Values.objectStore.region
    "OBJECT_STORE_ENDPOINT"            .Values.objectStore.endpoint
    "OBJECT_STORE_PRESIGN_EXPIRY"      (.Values.objectStore.presignExpiry | toString)
    "STORAGE_BACKEND_PROVIDER"         .Values.objectStore.backend.provider
    "STORAGE_BACKEND_PRODUCT"          .Values.objectStore.backend.product
    "AUTH_EXTERNAL_ISSUER_URL"         .Values.auth.external.issuerURL
    "AUTH_EXTERNAL_AUDIENCE"           .Values.auth.external.audience
-}}
{{- /* 1a. Conditional knobs — only when enabled. */ -}}
{{- if .Values.auth.internal.enabled -}}
  {{- $_ := set $cfg "AUTH_INTERNAL_ISSUER_URL" .Values.auth.internal.issuerURL -}}
  {{- $_ := set $cfg "AUTH_INTERNAL_AUDIENCE"   .Values.auth.internal.audience -}}
  {{- $_ := set $cfg "AUTH_INTERNAL_ALLOWED_SUBJECTS" (join "," .Values.auth.internal.allowedSubjects) -}}
{{- end -}}
{{- if .Values.server.tls.enabled -}}
  {{- $_ := set $cfg "SERVER_TLS_CERT_FILE" .Values.server.tls.certPath -}}
  {{- $_ := set $cfg "SERVER_TLS_KEY_FILE"  .Values.server.tls.keyPath -}}
{{- end -}}
{{- /* 2. Emit non-secret in sorted order; skip empties so binary defaults apply. */ -}}
{{- range $k := keys $cfg | sortAlpha -}}
{{- $v := get $cfg $k -}}
{{- if ne (toString $v) "" }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end -}}
{{- end -}}
{{- /* 3. Secret-backed vars (external existingSecret). List + `optional` is a
       chart-author decision, not an operator value. */ -}}
{{- $sec := .Values.secrets.existingSecret | required "secrets.existingSecret is required (chart never creates a Secret)" -}}
{{- $secretEnv := list
    (dict "name" "DB_PASSWORD"                    "optional" false)
    (dict "name" "OBJECT_STORE_ACCESS_KEY_ID"     "optional" true)
    (dict "name" "OBJECT_STORE_SECRET_ACCESS_KEY" "optional" true)
-}}
{{- range $s := $secretEnv }}
- name: {{ $s.name }}
  valueFrom:
    secretKeyRef:
      name: {{ $sec }}
      key: {{ $s.name }}
      optional: {{ $s.optional }}
{{- end }}
{{- /* 4. Operator passthrough. */ -}}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end -}}
{{- end -}}
