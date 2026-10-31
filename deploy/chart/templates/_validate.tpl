{{/*
Every rule that relates several values, or that the schema cannot state, in
one place (docs/adr/0008-the-chart-offers-choices-not-flags.md). The shape of
each value, its type, enum and range, lives in values.schema.json, which Helm
checks before any template runs; a rule lives here only when it needs another
value or a message naming the fix. Each message names the value and what to
set instead.

Hangar's rules stay with Hangar until its values are reshaped: the
_hangar-*.tpl and hangar-*.yaml templates and the artifactDaemon.hangar rules
in artifact-daemon-daemonset.yaml.

web-deployment.yaml includes this on its first line, and Helm renders that
template first, so these rules run before anything else in the chart.
*/}}
{{- define "concourse.validate" -}}
{{- if eq .Values.artifactDaemon.tls.source "existingSecret" -}}
{{- if empty .Values.artifactDaemon.tls.existingSecret -}}
{{- fail "artifactDaemon.tls.existingSecret is required for tls.source=existingSecret" -}}
{{- end -}}
{{- else if .Values.artifactDaemon.tls.existingSecret -}}
{{- fail "artifactDaemon.tls.existingSecret must be empty for tls.source=generated; select certificate ownership explicitly" -}}
{{- end -}}
{{- if empty .Values.artifactDaemon.resolveCapability.existingSecret -}}
{{- fail "artifactDaemon.resolveCapability.existingSecret is required; signed resolution is always enforced" -}}
{{- end -}}
{{- $url := urlParse .Values.web.externalUrl -}}
{{- if or (not (has $url.scheme (list "http" "https"))) (empty $url.host) (not (has $url.path (list "" "/"))) (not (empty $url.userinfo)) (not (empty $url.query)) (not (empty $url.fragment)) -}}
{{- fail "web.externalUrl must be an HTTP(S) origin URL for MCP, without a path, credentials, query or fragment" -}}
{{- end -}}
{{- $ids := dict -}}
{{- range .Values.mcp.clients -}}
{{- if hasKey $ids .client_id -}}
{{- fail (printf "mcp.clients repeats client_id %q" .client_id) -}}
{{- end -}}
{{- $_ := set $ids .client_id true -}}
{{- end -}}
{{- $owned := list "enable-mcp" "mcp-client-config" "mcp-disable-operation" "kubernetes-artifact-daemon-port" "kubernetes-artifact-daemon-host-path" "kubernetes-artifact-daemon-service" "kubernetes-artifact-daemon-resolve-capability-key" "kubernetes-artifact-daemon-tls-cert" "kubernetes-artifact-daemon-tls-key" "kubernetes-artifact-daemon-tls-ca-cert" -}}
{{- range .Values.web.env -}}
{{- $name := .name -}}
{{- range $owned -}}
{{- if eq $name (printf "CONCOURSE_%s" (. | replace "-" "_" | upper)) -}}
{{- fail (printf "web.env must not override chart-owned %s; use the explicit chart setting" $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and (not .Values.postgresql.enabled) (empty .Values.postgresql.host) -}}
{{- fail "postgresql.host is required when postgresql.enabled=false" -}}
{{- end -}}
{{- /*
  The web refuses to start without --encryption-key and migrate-db runs under
  the same key, so one source must name it: secrets.encryptionKeySecret, or,
  for a deployment that predates it, a CONCOURSE_ENCRYPTION_KEY entry in
  web.env. Both at once would hand the container two values for one variable.
*/ -}}
{{- $envKey := false -}}
{{- range .Values.web.env -}}
{{- if eq .name "CONCOURSE_ENCRYPTION_KEY" -}}
{{- $envKey = true -}}
{{- end -}}
{{- end -}}
{{- if and (empty .Values.secrets.encryptionKeySecret) (not $envKey) -}}
{{- fail "secrets.encryptionKeySecret is required: name the Secret holding the database encryption key under encryption-key; the web does not start without --encryption-key" -}}
{{- end -}}
{{- if and .Values.secrets.encryptionKeySecret $envKey -}}
{{- fail "secrets.encryptionKeySecret and a CONCOURSE_ENCRYPTION_KEY entry in web.env are both set; keep secrets.encryptionKeySecret and drop the web.env entry" -}}
{{- end -}}
{{- /*
  Each grant renders as one --kubernetes-step-pod-grant=name=..,owner=..
  flag, so a field may not be empty or hold the flag's own separators.
*/ -}}
{{- range $i, $grant := .Values.kubernetes.stepPodGrants -}}
{{- $privileged := eq (toString ($grant.privileged | default false)) "true" -}}
{{- range $field := list "name" "owner" "serviceAccount" -}}
{{- $value := index $grant $field | default "" | toString -}}
{{- if and (not $value) (or (ne $field "serviceAccount") (not $privileged)) -}}
{{- fail (printf "kubernetes.stepPodGrants[%d].%s must be set (serviceAccount may be left out only when privileged is true)" $i $field) -}}
{{- end -}}
{{- if or (contains "," $value) (contains "=" $value) -}}
{{- fail (printf "kubernetes.stepPodGrants[%d].%s must hold no ',' or '=' (got %q)" $i $field $value) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and .Values.serviceMonitor.enabled (not .Values.metrics.enabled) -}}
{{- fail "serviceMonitor.enabled requires metrics.enabled: the ATC serves Prometheus metrics only on its own bind port, so a ServiceMonitor without it scrapes the web UI's HTML and every alerting rule evaluates against no data" -}}
{{- end -}}
{{- /*
  mirror.replicas is a number (0 disables mirroring) or the literal "all",
  which the daemonset renders as -1. The schema admits any string so that
  this message, not a generic one, names the value.
*/ -}}
{{- $mirrorReplicas := .Values.artifactDaemon.mirror.replicas -}}
{{- if and (kindIs "string" $mirrorReplicas) (ne $mirrorReplicas "all") -}}
{{- fail (printf "artifactDaemon.mirror.replicas must be an integer or the string \"all\", got %q" $mirrorReplicas) -}}
{{- end -}}
{{- end -}}
