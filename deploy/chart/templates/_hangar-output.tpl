{{/* Reserve suffix length so long release names cannot collapse distinct workload names. */}}
{{- define "concourse.hangarOutput.qualifiedName" -}}
{{- $suffix := .suffix -}}
{{- $budget := int (sub 62 (len $suffix)) -}}
{{- if lt $budget 1 -}}
{{- fail (printf "the Hangar output plane cannot compose a name for %q: the suffix alone is %d characters and a Kubernetes object name is 63" $suffix (len $suffix)) -}}
{{- end -}}
{{- printf "%s-%s" (include "concourse.fullname" .root | trunc $budget | trimSuffix "-") $suffix -}}
{{- end }}

{{- define "concourse.durationSeconds" -}}
{{- $name := .name -}}
{{- $value := toString .value -}}
{{- if not (regexMatch "^([0-9]+h)?([0-9]+m)?([0-9]+s)?$" $value) -}}
{{- fail (printf "%s is %q, which is not a duration of whole hours, minutes and seconds (for example 24h, 90m, 30s)" $name $value) -}}
{{- end -}}
{{- $total := 0 -}}
{{- with regexFind "[0-9]+h" $value -}}
{{- $total = add $total (mul (atoi (trimSuffix "h" .)) 3600) -}}
{{- end -}}
{{- with regexFind "[0-9]+m" $value -}}
{{- $total = add $total (mul (atoi (trimSuffix "m" .)) 60) -}}
{{- end -}}
{{- with regexFind "[0-9]+s" $value -}}
{{- $total = add $total (atoi (trimSuffix "s" .)) -}}
{{- end -}}
{{- if le (int $total) 0 -}}
{{- fail (printf "%s is %q, which is zero or empty; every deadline in this plane is positive" $name $value) -}}
{{- end -}}
{{- $total -}}
{{- end }}

{{- define "concourse.quantityBytes" -}}
{{- $name := .name -}}
{{- $value := toString .value -}}
{{- if not (regexMatch "^[0-9]+(Ki|Mi|Gi|Ti|K|M|G|T)?$" $value) -}}
{{- fail (printf "%s is %q, which is not a Kubernetes quantity (for example 32Gi)" $name $value) -}}
{{- end -}}
{{- $digits := regexFind "^[0-9]+" $value -}}
{{- $unit := trimPrefix $digits $value -}}
{{- $multiplier := 1 -}}
{{- if eq $unit "Ki" }}{{- $multiplier = 1024 -}}{{- end -}}
{{- if eq $unit "Mi" }}{{- $multiplier = 1048576 -}}{{- end -}}
{{- if eq $unit "Gi" }}{{- $multiplier = 1073741824 -}}{{- end -}}
{{- if eq $unit "Ti" }}{{- $multiplier = 1099511627776 -}}{{- end -}}
{{- if eq $unit "K" }}{{- $multiplier = 1000 -}}{{- end -}}
{{- if eq $unit "M" }}{{- $multiplier = 1000000 -}}{{- end -}}
{{- if eq $unit "G" }}{{- $multiplier = 1000000000 -}}{{- end -}}
{{- if eq $unit "T" }}{{- $multiplier = 1000000000000 -}}{{- end -}}
{{- mul (atoi $digits) $multiplier -}}
{{- end }}

{{- define "concourse.hangarOutput.validate" -}}
{{- $output := .Values.hangarOutput -}}
{{- $base := $output.executionControl.enabled | default false -}}
{{- $capture := $output.enabled | default false -}}

{{- with .Values.web.runInputSigningKeySecret -}}
{{- if not $.Values.hangarOutput.webEnabled -}}
{{- fail "web.runInputSigningKeySecret requires hangarOutput.webEnabled" -}}
{{- end -}}
{{- if or (eq . $output.capabilityKeySecret) (eq . $output.materializationKeySecret) (eq . $.Values.artifactDaemon.resolveCapability.existingSecret) (eq . $.Values.artifactDaemon.tls.existingSecret) -}}
{{- fail "web.runInputSigningKeySecret must be separate from node capability and materialization Secrets" -}}
{{- end -}}
{{- end -}}

{{- if lt (int .Values.web.pipelineRunActivationEpoch) 0 -}}
{{- fail "web.pipelineRunActivationEpoch must be zero (admit no pipeline runs) or a positive Run contract activation epoch." -}}
{{- end -}}

{{- if and $capture (not $base) -}}
{{- fail "hangarOutput.enabled requires hangarOutput.executionControl.enabled. Durable output capture is an EXTENSION of exact execution control, never a synonym for it: a capture pod requires both ready labels, and a cohort advertising output without base would be claiming a capture plane with no exact-execution protocol underneath. Output admission can never be true while base admission is false." -}}
{{- end -}}
{{- if and $output.webEnabled (not $capture) -}}
{{- fail "hangarOutput.webEnabled requires hangarOutput.enabled" -}}
{{- end -}}

{{- if $base -}}
{{- if not $output.capabilityKeySecret -}}
{{- fail "hangarOutput.capabilityKeySecret is required: control capabilities are minted by the control plane and verified by the daemon with the same raw 32-byte key, and the daemon mounts its output plane only when it is given that key." -}}
{{- end -}}
{{- include "concourse.hangarOutput.validateScratch" . -}}
{{- end -}}

{{- if $capture -}}
{{- include "concourse.hangarOutput.validateBucket" . -}}
{{- include "concourse.hangarOutput.validateIntervals" . -}}
{{- include "concourse.hangarOutput.validateKeys" . -}}
{{- include "concourse.hangarOutput.validateDurations" . -}}
{{- include "concourse.hangarOutput.validatePrincipals" . -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateBucket" -}}
{{- $output := .Values.hangarOutput -}}
{{- if not $output.bucket -}}
{{- fail "hangarOutput.bucket is required when the output facet is enabled. It is a DEDICATED bucket, externally provisioned, containing only Hangar output-plane objects." -}}
{{- end -}}
{{- if not $output.tenant -}}
{{- fail "hangarOutput.tenant is required: the opaque output scope is derived from authenticated deployment/tenant identity, and an empty one would make every deployment's scope the same." -}}
{{- end -}}
{{- if and $output.cacheBucket (eq $output.bucket $output.cacheBucket) -}}
{{- fail (printf "hangarOutput.bucket is %q, which is hangarOutput.cacheBucket. The output plane needs a DEDICATED bucket: the cache tier is fail-open, name-keyed and re-derivable, and mixing the two puts objects with no ownership marker in the namespace the orphan sweep lists." $output.bucket) -}}
{{- end -}}
{{- if and $output.strictInputBucket (eq $output.bucket $output.strictInputBucket) -}}
{{- fail (printf "hangarOutput.bucket is %q, which is hangarOutput.strictInputBucket. The output plane needs a DEDICATED bucket; the strict-input bucket is caller-published." $output.bucket) -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateKeys" -}}
{{- $output := .Values.hangarOutput -}}
{{- if not $output.materializationKeySecret -}}
{{- fail "hangarOutput.materializationKeySecret is required: output read warrants use their own key and their own domain, never the node control key." -}}
{{- end -}}

{{- if and $output.capabilityKeySecret (eq $output.capabilityKeySecret $output.materializationKeySecret) -}}
{{- fail (printf "hangarOutput.capabilityKeySecret and hangarOutput.materializationKeySecret name the same Secret %q. They say different things -- a control capability authorizes one operation, a read warrant authorizes one staged read -- and they are pinned separately, so one Secret for two roles means rotating either rotates both." $output.capabilityKeySecret) -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateDurations" -}}
{{- $output := .Values.hangarOutput -}}
{{- $capture := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.captureDeadline" "value" $output.captureDeadline)) -}}
{{- $grace := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.publicationGrace" "value" $output.publicationGrace)) -}}

{{- if or (lt $capture 3600) (gt $capture 604800) -}}
{{- fail (printf "hangarOutput.captureDeadline is %s; it is configurable from 1h to 168h. Below an hour a legitimate slow capture is terminalised; above a week a lost capture pins its correlation for longer than anybody will look." $output.captureDeadline) -}}
{{- end -}}
{{- if gt $grace 2592000 -}}
{{- fail (printf "hangarOutput.publicationGrace is %s; the maximum is 720h (30 days)." $output.publicationGrace) -}}
{{- end -}}
{{- if lt $grace (add $capture 3600) -}}
{{- fail (printf "hangarOutput.publicationGrace is %s and hangarOutput.captureDeadline is %s. Grace must exceed the maximum capture deadline by at least an hour: below that, the reclaim pass can delete an object whose capture is still entitled to register it, out from under a live capture." $output.publicationGrace $output.captureDeadline) -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateScratch" -}}
{{- $scratch := .Values.artifactDaemon.outputScratch -}}
{{- if not $scratch.sizeLimit -}}
{{- fail "artifactDaemon.outputScratch.sizeLimit is required. The artifact daemon's output plane canonicalizes and spools whole trees into an emptyDir, and an emptyDir with no sizeLimit is bounded only by the node's disk: filling it evicts every Pod on the node rather than only this one. Set it to at least concurrency times maxContentBytes." -}}
{{- end -}}
{{- $limit := atoi (include "concourse.quantityBytes" (dict "name" "artifactDaemon.outputScratch.sizeLimit" "value" $scratch.sizeLimit)) -}}
{{- $concurrency := int $scratch.concurrency -}}
{{- if lt $concurrency 1 -}}
{{- fail "artifactDaemon.outputScratch.concurrency must be at least 1; zero would admit no capture at all." -}}
{{- end -}}
{{- $needed := mul $concurrency (int64 $scratch.maxContentBytes) -}}
{{- if gt (int64 $needed) (int64 $limit) -}}
{{- fail (printf "artifactDaemon.outputScratch.concurrency is %d and maxContentBytes is %v, so %v bytes may be spooled at once -- more than the sizeLimit %s (%v bytes). Either raise the limit or lower the concurrency: the bound only holds if their product fits." $concurrency $scratch.maxContentBytes $needed $scratch.sizeLimit $limit) -}}
{{- end -}}
{{- if not (hasPrefix "/" (clean (toString $scratch.path))) -}}
{{- fail "artifactDaemon.outputScratch.path must be absolute" -}}
{{- end -}}
{{- $path := clean (toString $scratch.path) -}}
{{- $storage := clean (.Values.artifactDaemon.hostPath | default "/var/concourse/artifacts") -}}
{{- if or (eq $path $storage) (hasPrefix (printf "%s/" $storage) $path) (hasPrefix (printf "%s/" $path) $storage) -}}
{{- fail "artifactDaemon.outputScratch.path and artifactDaemon.hostPath must be disjoint" -}}
{{- end -}}
{{- if .Values.artifactDaemon.hangar.enabled -}}
{{- $strict := clean (toString .Values.artifactDaemon.hangar.scratchPath) -}}
{{- if or (eq $path $strict) (hasPrefix (printf "%s/" $strict) $path) (hasPrefix (printf "%s/" $path) $strict) -}}
{{- fail "artifactDaemon.outputScratch.path and artifactDaemon.hangar.scratchPath must be disjoint" -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.web.runResultReads" -}}
{{- if and .Values.hangarOutput.executionControl.enabled .Values.hangarOutput.enabled .Values.hangarOutput.webEnabled -}}true{{- end -}}
{{- end }}

{{- define "concourse.web.validateRunResults" -}}
{{- $results := .Values.web.runResults -}}
{{- $concurrency := int $results.readConcurrency -}}
{{- if lt $concurrency 1 -}}
{{- fail "web.runResults.readConcurrency must be at least 1; zero would refuse every result download." -}}
{{- end -}}
{{- if not $results.scratchSizeLimit -}}
{{- fail "web.runResults.scratchSizeLimit is required: an emptyDir with no sizeLimit is bounded only by the node's disk." -}}
{{- end -}}
{{- $limit := atoi (include "concourse.quantityBytes" (dict "name" "web.runResults.scratchSizeLimit" "value" $results.scratchSizeLimit)) -}}
{{- $needed := mul $concurrency 2 268435456 -}}
{{- if gt (int64 $needed) (int64 $limit) -}}
{{- fail (printf "web.runResults.readConcurrency is %d, so up to %d bytes of result archives may be spooled at once -- more than web.runResults.scratchSizeLimit %s (%d bytes). Raise the limit to at least readConcurrency x 512Mi or lower the concurrency." $concurrency $needed $results.scratchSizeLimit $limit) -}}
{{- end -}}
{{- end }}

{{- define "concourse.web.validateRunCredentialWorkerImages" -}}
{{- $images := .Values.web.runCredentialWorkerImages | default list -}}
{{- if and $images (not .Values.web.runInputSigningKeySecret) -}}
{{- fail "web.runCredentialWorkerImages requires web.runInputSigningKeySecret: without Run input intake there is no credential handoff to pin." -}}
{{- end -}}
{{- range $images -}}
{{- if not (regexMatch "^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[0-9a-f]{64}$" (toString .)) -}}
{{- fail (printf "web.runCredentialWorkerImages entry %q is not a digest-qualified image reference (repository@sha256:<64 hex>). A tag can be moved after it is pinned." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateIntervals" -}}
{{- $output := .Values.hangarOutput -}}
{{- $_ := include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaim.interval" "value" $output.reclaim.interval) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaim.deleteTimeout" "value" $output.reclaim.deleteTimeout) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.orphanSweep.interval" "value" $output.orphanSweep.interval) -}}
{{- if lt (int $output.reclaim.batch) 1 -}}
{{- fail (printf "hangarOutput.reclaim.batch is %d; one pass must admit at least one generation, or nothing is ever reclaimed." (int $output.reclaim.batch)) -}}
{{- end -}}
{{- end }}

{{- /*
The web and the artifact daemon are the output plane's two principals: the
daemon publishes (create, get), and the web reclaims and sweeps (list, get,
delete). A service account is Pod-wide, so the two -- and the task pods' --
must be distinct Kubernetes accounts bound to distinct cloud principals.
*/ -}}
{{- define "concourse.hangarOutput.validatePrincipals" -}}
{{- $subjects := list
  (dict "path" "serviceAccount"
        "name" (include "concourse.serviceAccountName" .)
        "annotations" (ternary (.Values.serviceAccount.annotations | default dict) dict (.Values.serviceAccount.create | default false | not | not)))
-}}
{{- if .Values.kubernetes.serviceAccount -}}
{{- $subjects = append $subjects (dict
      "path" "kubernetes.serviceAccount"
      "name" .Values.kubernetes.serviceAccount
      "annotations" dict) -}}
{{- end -}}
{{- $subjects = append $subjects (dict
      "path" "artifactDaemon"
      "name" (printf "%s-artifact-daemon" (include "concourse.fullname" .))
      "annotations" (.Values.artifactDaemon.serviceAccount.annotations | default dict)) -}}

{{- $accounts := dict -}}
{{- range $subject := $subjects -}}
{{- $name := $subject.name -}}
{{- if hasKey $accounts $name -}}
{{- fail (printf "%s and %s render the same Kubernetes service account %q. A service account is Pod-wide: two workloads sharing one are ONE cloud identity holding both sets of permissions, and no care inside either process takes that back. The publisher (the artifact daemon) must not be able to list or delete; the web, which reclaims and sweeps, must not be able to create; and task, cache and strict-input identities hold no output role whatsoever." (get $accounts $name) $subject.path $name) -}}
{{- end -}}
{{- $_ := set $accounts $name $subject.path -}}
{{- end -}}

{{- $principals := dict -}}
{{- range $subject := $subjects -}}
{{- $principal := get ($subject.annotations | default dict) "iam.gke.io/gcp-service-account" -}}
{{- if $principal -}}
{{- if hasKey $principals $principal -}}
{{- fail (printf "%s and %s are annotated with the same cloud principal %q. Shared Workload Identity principals are refused: the whole separation is distinct cloud identities with disjoint grants, and one principal bound to two Kubernetes service accounts has the union of both." (get $principals $principal) $subject.path $principal) -}}
{{- end -}}
{{- $_ := set $principals $principal $subject.path -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.namespaceFlagsAt" -}}
{{- $root := .root -}}
- --output-store={{ $root.Values.hangarOutput.store }}
- --output-bucket={{ $root.Values.hangarOutput.bucket }}
- --output-prefix={{ $root.Values.hangarOutput.prefix }}
- --output-tenant={{ $root.Values.hangarOutput.tenant }}
{{- if eq $root.Values.hangarOutput.store "disk" }}
- --output-endpoint={{ include "concourse.hangarStorage.endpoint" $root }}
- --output-store-id={{ $root.Values.hangarStorage.disk.storeID }}
- --output-token-file={{ .disk }}/token
- --output-ca-cert={{ .disk }}/ca.crt
{{- else if $root.Values.hangarOutput.endpoint }}
- --output-endpoint={{ $root.Values.hangarOutput.endpoint }}
{{- end }}
{{- end }}

{{- define "concourse.hangarOutput.controllerSecurityContext" -}}
securityContext:
  runAsNonRoot: true
  runAsUser: 65534
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL
  seccompProfile:
    type: RuntimeDefault
{{- end }}
