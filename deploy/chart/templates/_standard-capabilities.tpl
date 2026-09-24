{{/* Standard capabilities have no rollout switches. Deployment policy stays explicit. */}}
{{- define "concourse.standardCapabilities.validate" -}}
{{- if hasKey .Values.artifactDaemon "enabled" -}}
{{- fail "artifactDaemon.enabled has been removed; the artifact daemon is always deployed. Remove the value." -}}
{{- end -}}
{{- if hasKey .Values.artifactDaemon.tls "enabled" -}}
{{- fail "artifactDaemon.tls.enabled has been removed; mTLS is always required. Set tls.source and remove the old value." -}}
{{- end -}}
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
{{- $owned := list "enable-mcp" "mcp-client-config" "mcp-disable-operation" "kubernetes-artifact-daemon-port" "kubernetes-artifact-daemon-host-path" "kubernetes-artifact-daemon-service" "kubernetes-artifact-daemon-resolve-capability-key" "kubernetes-artifact-daemon-resolve-capability-ttl" "kubernetes-artifact-daemon-tls-cert" "kubernetes-artifact-daemon-tls-key" "kubernetes-artifact-daemon-tls-ca-cert" -}}
{{- range .Values.web.extraArgs -}}
{{- $arg := . -}}
{{- range $owned -}}
{{- if regexMatch (printf "^--%s($|[=[:space:]])" .) $arg -}}
{{- fail (printf "web.extraArgs must not override chart-owned --%s; use the explicit chart setting" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range .Values.web.env -}}
{{- $name := .name -}}
{{- range $owned -}}
{{- if eq $name (printf "CONCOURSE_%s" (. | replace "-" "_" | upper)) -}}
{{- fail (printf "web.env must not override chart-owned %s; use the explicit chart setting" $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
