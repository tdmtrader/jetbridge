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
The ring Secret is named by its composition: the active activation epoch and a
digest of the earlier epochs' keys it retains. A change to those yields a new
Secret rather than a refusal to replace the old one.
*/}}
{{- define "concourse.hangarBootstrap.ringName" -}}
{{- $digest := toJson .Values.hangarBootstrap.referencedKeys | sha256sum | trunc 8 -}}
{{- printf "%s-hangar-rings-e%d-%s" (include "concourse.fullname" .) (int .Values.hangarOutput.activationEpoch) $digest | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Whether the ring exists: it needs both an active control key and an active receipt key. */}}
{{- define "concourse.hangarBootstrap.ringEnabled" -}}
{{- if and .Values.hangarBootstrap.enabled .Values.hangarOutput.executionControl.keySecret .Values.hangarOutput.receipt.privateKeySecret -}}true{{- end -}}
{{- end -}}

{{- define "concourse.hangarBootstrap.inventory" -}}
{{- $ := . -}}
{{- $out := .Values.hangarOutput -}}
{{- $epoch := int $out.activationEpoch -}}
{{- $entries := list -}}

{{- with .Values.artifactDaemon.hangar.keySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "random32" "key" "hangar.key"
  "purposes" (dict "hangar.key" "strict-input materialization warrant key")
  "consumers" (list "web" "artifact-daemon")) -}}
{{- end -}}

{{- with .Values.hangarStorage.disk.tls.existingSecret -}}
{{- $entries = append $entries (dict "name" . "kind" "tls-bundle" "commonName" "hangar disk store"
  "dnsNames" (list (printf "%s.%s.svc" (include "concourse.hangarStorage.name" $) $.Release.Namespace))
  "purposes" (dict "tls.crt" "disk store server certificate" "tls.key" "disk store server key" "ca.crt" "disk store CA, for clients")
  "consumers" (list "hangar-store" "artifact-daemon" "hangar-output-inventory" "hangar-output-reclaimer")) -}}
{{- end -}}

{{- with .Values.hangarStorage.disk.credentials.existingSecret -}}
{{- $entries = append $entries (dict "name" . "kind" "store-tokens"
  "purposes" (dict "input" "strict-input principal token" "publisher" "output publisher token" "inventory" "inventory principal token" "reclaimer" "reclaimer principal token" "server.json" "the store's principal-to-token map")
  "consumers" (list "hangar-store" "artifact-daemon" "hangar-output-inventory" "hangar-output-reclaimer")) -}}
{{- end -}}

{{- with $out.executionControl.keySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "ed25519" "key" "control.key" "ring" "control" "epoch" $epoch
  "purposes" (dict "control.key" "node-control signing key for this activation epoch")
  "consumers" (list "artifact-daemon")) -}}
{{- end -}}

{{- with $out.capabilityKeySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "random32" "key" "capability.key"
  "purposes" (dict "capability.key" "output control warrant key")
  "consumers" (list "web" "artifact-daemon")) -}}
{{- end -}}

{{- with $out.receipt.privateKeySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "ed25519" "key" "receipt.key" "ring" "receipt" "epoch" $epoch "keyID" $out.receipt.keyID
  "purposes" (dict "receipt.key" "publication receipt signing key for this activation epoch")
  "consumers" (list "artifact-daemon")) -}}
{{- end -}}

{{- with $out.materializationKeySecret -}}
{{- $entries = append $entries (dict "name" . "kind" "random32" "key" "materialize.key"
  "purposes" (dict "materialize.key" "output read warrant key")
  "consumers" (list "web" "artifact-daemon")) -}}
{{- end -}}

{{- $entries = append $entries (dict "name" (include "concourse.hangarBootstrap.runInputKeyName" $) "kind" "random32" "key" "input.key"
  "purposes" (dict "input.key" "Run input signing key")
  "consumers" (list "web")) -}}

{{- range $earlier := .Values.hangarBootstrap.referencedKeys -}}
{{- $entries = append $entries (dict "name" $earlier.receiptSecret "kind" "ed25519" "key" "receipt.key" "ring" "receipt" "epoch" (int $earlier.epoch) "keyID" $earlier.receiptKeyID "required" true
  "purposes" (dict "receipt.key" "an earlier activation epoch's publication receipt key, read for its public half")
  "consumers" (list)) -}}
{{- $entries = append $entries (dict "name" $earlier.controlSecret "kind" "ed25519" "key" "control.key" "ring" "control" "epoch" (int $earlier.epoch) "required" true
  "purposes" (dict "control.key" "an earlier activation epoch's node-control key, read for its public half")
  "consumers" (list)) -}}
{{- end -}}

{{- if include "concourse.hangarBootstrap.ringEnabled" $ -}}
{{- $entries = append $entries (dict "name" (include "concourse.hangarBootstrap.ringName" $) "kind" "ring" "activeEpoch" $epoch "activeKeyID" $out.receipt.keyID
  "purposes" (dict "receipt-keys.json" "public publication receipt verification ring" "control-keys.json" "public node-control verification ring")
  "consumers" (list "web")) -}}
{{- end -}}

{{- if .Values.hangarBootstrap.database.enabled -}}
{{- with $out.database.existingSecret -}}
{{- $entries = append $entries (dict "name" . "kind" "dsn"
  "purposes" (dict "dsn" "the activation database role's connection string")
  "consumers" (list "hangar-output-activation")) -}}
{{- end -}}
{{- end -}}

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
