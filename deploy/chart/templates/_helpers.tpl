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

{{/*
The web settings the chart renders as environment variables, one row each:
[variable, the value that sets it, the rendered value or "" when unset]. The
web container renders every set row; an empty one keeps the binary's default.
*/}}
{{- define "concourse.web.settingsEnv" -}}
{{- $web := .Values.web -}}
{{- $attempts := "" -}}
{{- if $web.secretRetry.attempts -}}
{{- $attempts = int $web.secretRetry.attempts | toString -}}
{{- end -}}
{{- list
  (list "CONCOURSE_AUTH_DURATION" "web.authDuration" $web.authDuration)
  (list "SSL_CERT_FILE" "web.caCertFile" $web.caCertFile)
  (list "CONCOURSE_RESOURCE_CHECKING_INTERVAL" "web.checks.interval" $web.checks.interval)
  (list "CONCOURSE_RESOURCE_TYPE_CHECKING_INTERVAL" "web.checks.resourceTypeInterval" $web.checks.resourceTypeInterval)
  (list "CONCOURSE_RESOURCE_WITH_WEBHOOK_CHECKING_INTERVAL" "web.checks.webhookInterval" $web.checks.webhookInterval)
  (list "CONCOURSE_GLOBAL_RESOURCE_CHECK_TIMEOUT" "web.checks.timeout" $web.checks.timeout)
  (list "CONCOURSE_GC_ONE_OFF_GRACE_PERIOD" "web.gc.oneOffGracePeriod" $web.gc.oneOffGracePeriod)
  (list "CONCOURSE_GC_MISSING_GRACE_PERIOD" "web.gc.missingGracePeriod" $web.gc.missingGracePeriod)
  (list "CONCOURSE_SECRET_RETRY_ATTEMPTS" "web.secretRetry.attempts" $attempts)
  (list "CONCOURSE_SECRET_RETRY_INTERVAL" "web.secretRetry.interval" $web.secretRetry.interval)
  | toJson -}}
{{- end -}}

{{/*
Whether the web reads Secrets: "true" when the Kubernetes credential manager
is on, else empty. That manager is the web's only reader of Secrets: it
resolves ((vars)) from <namespacePrefix><team>, a namespace per team, so the
web ClusterRole's grant is cluster-wide. kubernetes.credentialManager.enabled
turns it on, and a deployment that predates that value turns it on through
web.env with CONCOURSE_KUBERNETES_IN_CLUSTER or
CONCOURSE_KUBERNETES_CONFIG_PATH. Nothing else in web reads a Secret: image
pull Secrets are named on step pods and read by the kubelet. Kept here, beside
the other web env names, so web_env_architecture_test.go checks these two
names are still web flags.
*/}}
{{- define "concourse.web.readsSecrets" -}}
{{- $reads := .Values.kubernetes.credentialManager.enabled -}}
{{- range .Values.web.env -}}
{{- if has .name (list "CONCOURSE_KUBERNETES_IN_CLUSTER" "CONCOURSE_KUBERNETES_CONFIG_PATH") -}}
{{- $reads = true -}}
{{- end -}}
{{- end -}}
{{- if $reads -}}true{{- end -}}
{{- end -}}

{{/*
The web's secret flag values, kept out of the pod's args (which anyone who may
get the pod can read) and handed over as environment variables from the
<fullname>-web-credentials Secret. A JSON object, key -> value; empty when
there is nothing to hand over, and then no Secret renders.

- postgres-password: postgresql.password, unless postgresql.existingSecret
  names the operator's own Secret.
- add-local-user: web.localUsers, each entry trimmed and the empty ones
  dropped, joined with the comma go-flags splits CONCOURSE_ADD_LOCAL_USER on.
  Left out when web.env names CONCOURSE_ADD_LOCAL_USER: that entry used to
  lose to the flags, so the flags stay (concourse.web.argCredentials).

Each value is what the old flag delivered (concourse.web.argValue). A value a
Secret cannot reproduce exactly is left out, and its flag stays an arg as
before; for the local users, one such entry keeps them all flags.
*/}}
{{- define "concourse.web.credentials" -}}
{{- $data := dict -}}
{{- if not .Values.postgresql.existingSecret -}}
{{- $password := include "concourse.web.argValue" (dict "flag" "postgres-password" "value" .Values.postgresql.password) | fromJson -}}
{{- if $password.ok -}}
{{- $_ := set $data "postgres-password" $password.value -}}
{{- end -}}
{{- end -}}
{{- $users := list -}}
{{- $reproduced := true -}}
{{- range splitList "," .Values.web.localUsers -}}
{{- if trim . -}}
{{- $user := include "concourse.web.argValue" (dict "flag" "add-local-user" "value" (trim .)) | fromJson -}}
{{- if $user.ok -}}
{{- $users = append $users $user.value -}}
{{- else -}}
{{- $reproduced = false -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if and $users $reproduced (not (has "CONCOURSE_ADD_LOCAL_USER" (include "concourse.web.argCredentials" . | fromJsonArray))) -}}
{{- $_ := set $data "add-local-user" (join "," $users) -}}
{{- end -}}
{{- toJson $data -}}
{{- end -}}

{{/*
What the arg `- --<flag>=<value>`, as web-deployment.yaml used to render it,
handed the binary: JSON {"ok": true, "value": ...}, or {"ok": false} when a
Secret cannot hand over the same string.

The arg reached the container through two parsers. YAML read it as an
unquoted scalar: a " #" began a comment, trailing spaces went, and ": " made
it a mapping. The kubelet then turned $$ into $ and replaced $(VAR) with the
container's variable VAR, which it never does to a value from a Secret. So
the arg is rebuilt and parsed the same way, and refused when it is not one
string starting with the flag, when it spans lines (that broke the
manifest), or when it holds $( (a reference the kubelet resolved, or a $$(
it unescaped).
*/}}
{{- define "concourse.web.argValue" -}}
{{- $prefix := printf "--%s=" .flag -}}
{{- $value := toString .value -}}
{{- $out := dict "ok" false -}}
{{- if not (or (contains "\n" $value) (contains "\r" $value)) -}}
{{- $parsed := printf "- %s%s" $prefix $value | fromYamlArray -}}
{{- if and (eq (len $parsed) 1) (kindIs "string" (first $parsed)) -}}
{{- $arg := first $parsed -}}
{{- if and (hasPrefix $prefix $arg) (not (contains "$(" $arg)) -}}
{{- $out = dict "ok" true "value" (trimPrefix $prefix $arg | replace "$$" "$") -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/*
The credential variables the web container still takes as flags, as a JSON
list: each one a web.env entry also names. A flag beats its environment
variable in go-flags, so until now the flag won; keeping it keeps that.
*/}}
{{- define "concourse.web.argCredentials" -}}
{{- $names := list -}}
{{- range .Values.web.env -}}
{{- if has .name (list "CONCOURSE_POSTGRES_PASSWORD" "CONCOURSE_ADD_LOCAL_USER") -}}
{{- $names = append $names .name -}}
{{- end -}}
{{- end -}}
{{- toJson $names -}}
{{- end -}}

{{/*
One env entry reading a key of <fullname>-web-credentials.
*/}}
{{- define "concourse.web.credentialEnv" -}}
- name: {{ .name }}
  valueFrom:
    secretKeyRef:
      name: {{ include "concourse.fullname" .root }}-web-credentials
      key: {{ .key }}
{{- end -}}
