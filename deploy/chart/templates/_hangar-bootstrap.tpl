{{/*
The bootstrap inventory: every Secret a Hangar deployment needs, declared once.

Each entry gives the Secret's name (the values key the consumer already reads),
its material, what each data key is for, and the components that mount it. The
`concourse hangar-bootstrap` Job creates an absent entry and never replaces,
rotates or deletes one. An entry appears only once its values key names a
Secret, so the operator turns each Secret on by naming it.
*/}}

{{- define "concourse.hangarBootstrap.name" -}}
{{- printf "%s-hangar-bootstrap" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "concourse.hangarBootstrap.labelValue" -}}
concourse-hangar-bootstrap
{{- end -}}

{{- define "concourse.hangarBootstrap.runInputKeyName" -}}
{{- .Values.hangarBootstrap.secretNames.runInputSigningKey | default (printf "%s-run-input-signing-key" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-") -}}
{{- end -}}

{{/*
The inventory holds CAs, leaves, bundles, symmetric keys and tokens. The one
symmetric key Hangar needs is the Hangar key (hangar.key): the web signs every
warrant with it and the daemon verifies against it. There is no node signing
key and no verification ring: the daemon is trusted over mTLS and nothing it
says is signed.
*/}}
{{- define "concourse.hangarBootstrap.inventory" -}}
{{- $ := . -}}
{{- $entries := list -}}

{{- with .Values.artifactDaemon.hangar.keySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "random32" "key" "hangar.key"
  "purposes" (dict "hangar.key" "the Hangar key: signs every warrant (materialization, read, control)")
  "consumers" (list "web" "artifact-daemon")) -}}
{{- end -}}

{{- with .Values.hangarStorage.disk.tls.existingSecret -}}
{{- $entries = append $entries (dict "name" . "kind" "tls-bundle" "commonName" "hangar disk store"
  "dnsNames" (list (printf "%s.%s.svc" (include "concourse.hangarStorage.name" $) $.Release.Namespace))
  "purposes" (dict "tls.crt" "disk store server certificate" "tls.key" "disk store server key" "ca.crt" "disk store CA, for clients")
  "consumers" (list "hangar-store" "artifact-daemon" "web")) -}}
{{- end -}}

{{- with .Values.hangarStorage.disk.credentials.existingSecret -}}
{{- $entries = append $entries (dict "name" . "kind" "store-tokens"
  "purposes" (dict "input" "strict-input principal token" "publisher" "output publisher token" "inventory" "list-and-stat token the web's orphan sweep lists with" "reclaimer" "stat-and-delete token the web's reclaim pass and orphan sweep delete with" "server.json" "the store's principal-to-token map")
  "consumers" (list "hangar-store" "artifact-daemon" "web")) -}}
{{- end -}}

{{- $entries = append $entries (dict "name" (include "concourse.hangarBootstrap.runInputKeyName" $) "kind" "random32" "key" "input.key"
  "purposes" (dict "input.key" "Run input signing key")
  "consumers" (list "web")) -}}

{{- toJson (dict "labels" (dict "app.kubernetes.io/managed-by" (include "concourse.hangarBootstrap.labelValue" $)) "entries" $entries) -}}
{{- end -}}

{{/* Every Secret name the inventory holds, for the bootstrap Role and policy. */}}
{{- define "concourse.hangarBootstrap.secretNames" -}}
{{- $names := list -}}
{{- range $entry := (include "concourse.hangarBootstrap.inventory" . | fromJson).entries -}}
{{- $names = append $names $entry.name -}}
{{- end -}}
{{- toJson $names -}}
{{- end -}}
