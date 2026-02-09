{{/*
Chart name, truncated to 63 chars.
*/}}
{{- define "concourse.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, truncated to 63 chars.
*/}}
{{- define "concourse.fullname" -}}
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
Common labels.
*/}}
{{- define "concourse.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{ include "concourse.selectorLabels" . }}
{{- end }}

{{/*
Selector labels for the web deployment.
*/}}
{{- define "concourse.selectorLabels" -}}
app.kubernetes.io/name: {{ include "concourse.name" . }}
app.kubernetes.io/component: web
{{- end }}

{{/*
ServiceAccount name for the web pod.
*/}}
{{- define "concourse.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-web" (include "concourse.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image reference.
*/}}
{{- define "concourse.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Kubernetes namespace for task pods. Defaults to release namespace.
*/}}
{{- define "concourse.kubernetesNamespace" -}}
{{- default .Release.Namespace .Values.kubernetes.namespace }}
{{- end }}

{{/*
PostgreSQL host. Internal service name when bundled, external host otherwise.
*/}}
{{- define "concourse.postgresHost" -}}
{{- if .Values.postgresql.enabled }}
{{- printf "%s-db" (include "concourse.fullname" .) }}
{{- else }}
{{- required "postgresql.host is required when postgresql.enabled=false" .Values.postgresql.host }}
{{- end }}
{{- end }}

{{/*
PostgreSQL port.
*/}}
{{- define "concourse.postgresPort" -}}
{{- if .Values.postgresql.enabled }}
{{- "5432" }}
{{- else }}
{{- default "5432" .Values.postgresql.port | toString }}
{{- end }}
{{- end }}
