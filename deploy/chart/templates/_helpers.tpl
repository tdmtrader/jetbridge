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
Common labels, with NO component. Every object that is not the web deployment
includes this one and supplies its own `app.kubernetes.io/component`.

The split exists because `concourse.labels` carries `component: web` (through
the selector labels) and twenty-three objects that are not web then added their
own, so every one of them rendered the key TWICE. YAML's last-wins made the
effective value right and installs work, which is why it survived from the
chart's first commit; a strict decoder -- `kubectl apply --validate=strict`,
server-side apply, a policy engine, or
TestEveryRenderedObjectDecodesAsTheKubernetesObjectItClaimsToBe -- rejects the
object outright.
*/}}
{{- define "concourse.commonLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/name: {{ include "concourse.name" . }}
{{- end }}

{{/*
Common labels for the WEB objects: the common set plus component: web.
*/}}
{{- define "concourse.labels" -}}
{{ include "concourse.commonLabels" . }}
app.kubernetes.io/component: web
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
_validate.tpl refuses an external database with no host.
*/}}
{{- define "concourse.postgresHost" -}}
{{- if .Values.postgresql.enabled }}
{{- printf "%s-db" (include "concourse.fullname" .) }}
{{- else }}
{{- .Values.postgresql.host }}
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
The CONCOURSE_ENCRYPTION_KEY env entry, from secrets.encryptionKeySecret (under
the fixed key encryption-key) or, for a deployment that predates that value,
copied from the web.env entry of that name. _validate.tpl guarantees exactly
one source. migrate-db always includes this; the web container includes it only
for the typed source, since its web.env range already emits the other.
*/}}
{{- define "concourse.encryptionKeyEnv" -}}
{{- if .Values.secrets.encryptionKeySecret -}}
- name: CONCOURSE_ENCRYPTION_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.secrets.encryptionKeySecret }}
      key: encryption-key
{{- else -}}
{{- range .Values.web.env -}}
{{- if eq .name "CONCOURSE_ENCRYPTION_KEY" -}}
- {{ toYaml . | nindent 2 | trim }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
