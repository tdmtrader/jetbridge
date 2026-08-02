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
{{- $namespace := default .Release.Namespace .Values.kubernetes.namespace -}}
{{- if ne $namespace .Release.Namespace -}}
{{- fail (printf "kubernetes.namespace must match the Helm release namespace (%q); cross-namespace runtime RBAC, artifact-daemon discovery, and TLS are not yet supported" .Release.Namespace) -}}
{{- end -}}
{{- $namespace }}
{{- end }}

{{/*
Artifact-daemon headless service name.
Uses artifactDaemon.serviceName verbatim when set, otherwise defaults to
<fullname>-artifact-daemon.
*/}}
{{- define "concourse.artifactDaemonServiceName" -}}
{{- if .Values.artifactDaemon.serviceName }}
{{- .Values.artifactDaemon.serviceName }}
{{- else }}
{{- printf "%s-artifact-daemon" (include "concourse.fullname" .) }}
{{- end }}
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

{{/*
Whether this deployment configures agent: steps, via the durable snapshot
store (agentSnapshots.enabled) or a directly-set --agent-step-image, either
as a CLI flag (web.extraArgs) or its equivalent env var
(web.env: CONCOURSE_AGENT_STEP_IMAGE; see atc/exec/agent_step.go).
agentSnapshots is a good but incomplete proxy: hermetic: true also applies to
plain agent: steps that never touch snapshots (atc/steps.go,
atc/exec/task_step.go), and those still need --agent-step-image /
CONCOURSE_AGENT_STEP_IMAGE to run at all (atc/atccmd/command.go). Returns the
non-empty string "true" when configured, "" otherwise, so callers can use it
directly in an `and`/`if`.
*/}}
{{- define "concourse.agentStepConfigured" -}}
{{- $configured := .Values.agentSnapshots.enabled -}}
{{- range .Values.web.extraArgs -}}
{{- if hasPrefix "--agent-step-image" . -}}
{{- $configured = true -}}
{{- end -}}
{{- end -}}
{{- range .Values.web.env -}}
{{- if eq (.name | default "") "CONCOURSE_AGENT_STEP_IMAGE" -}}
{{- $configured = true -}}
{{- end -}}
{{- end -}}
{{- if $configured }}true{{ end -}}
{{- end -}}
