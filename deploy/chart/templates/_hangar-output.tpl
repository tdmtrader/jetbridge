{{/*
The Hangar output plane's names, its duration arithmetic, and its refusals.

Every rule here refuses a RENDER rather than letting a workload start and fail.
A render is what an operator reviews and what GitOps diffs; a configuration that
only a pod's startup rejects is one that reaches a cluster, CrashLoopBackOffs,
and is diagnosed from logs at three in the morning.
*/}}

{{/* ------------------------------------------------------------------ names */}}

{{- define "concourse.hangarOutput.daemonName" -}}
{{- printf "%s-hangar-output-daemon" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "concourse.hangarOutput.inventoryName" -}}
{{- printf "%s-hangar-output-inventory" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "concourse.hangarOutput.reclaimerName" -}}
{{- printf "%s-hangar-output-reclaimer" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "concourse.hangarOutput.attestorName" -}}
{{- printf "%s-hangar-output-policy-attestor" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "concourse.hangarOutput.receiptKeysName" -}}
{{- printf "%s-hangar-output-receipt-keys" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "concourse.hangarOutput.activationName" -}}
{{- printf "%s-hangar-output-activation" (include "concourse.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Service-account names. Each role's own value wins; otherwise the workload's
name. Four distinct names by construction, and the validation below refuses two
that an operator made equal.
*/}}
{{- define "concourse.hangarOutput.daemonServiceAccount" -}}
{{- default (include "concourse.hangarOutput.daemonName" .) .Values.hangarOutput.daemon.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.inventoryServiceAccount" -}}
{{- default (include "concourse.hangarOutput.inventoryName" .) .Values.hangarOutput.inventory.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.reclaimerServiceAccount" -}}
{{- default (include "concourse.hangarOutput.reclaimerName" .) .Values.hangarOutput.reclaimer.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.attestorServiceAccount" -}}
{{- default (include "concourse.hangarOutput.attestorName" .) .Values.hangarOutput.policyAttestor.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.activationServiceAccount" -}}
{{- default (include "concourse.hangarOutput.activationName" .) .Values.hangarOutput.activation.serviceAccount.name }}
{{- end }}

{{/* -------------------------------------------------------------- durations */}}

{{/*
durationSeconds parses a Go-style duration of whole hours, minutes and seconds.

Helm has no duration arithmetic, and the relationships between the capture
deadline, the publication grace and the lease term are the ones a mistake is
expensive in: a grace that does not exceed the maximum capture deadline lets
inventory adopt an object a capture is still entitled to register. So the chart
does the arithmetic rather than documenting the rule and hoping.

Sub-second precision is deliberately not accepted. No value in this plane is
expressed in milliseconds, and admitting "1500ms" would mean either parsing it
or silently reading it as 1500.
*/}}
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

{{/*
quantityBytes turns a Kubernetes quantity (32Gi, 500Mi, 1073741824) into bytes,
so the scratch volume's size limit can be compared with the content limit it has
to hold.
*/}}
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

{{/* ------------------------------------------------------------- validation */}}

{{/*
validate is invoked UNCONDITIONALLY by the output plane's first template, so
that "output enabled without base control" is refused rather than rendered as a
daemon that would refuse itself at startup.
*/}}
{{- define "concourse.hangarOutput.validate" -}}
{{- $output := .Values.hangarOutput -}}
{{- $base := $output.executionControl.enabled | default false -}}
{{- $capture := $output.enabled | default false -}}

{{- if and $capture (not $base) -}}
{{- fail "hangarOutput.enabled requires hangarOutput.executionControl.enabled. Durable output capture is an EXTENSION of exact execution control, never a synonym for it: a capture pod requires both ready labels, and a cohort advertising output without base would be claiming a capture plane with no exact-execution protocol underneath. Output admission can never be true while base admission is false." -}}
{{- end -}}
{{- if and $output.webEnabled (not $capture) -}}
{{- fail "hangarOutput.webEnabled requires hangarOutput.enabled" -}}
{{- end -}}

{{- if $base -}}
{{- if not $output.executionControl.keySecret -}}
{{- fail "hangarOutput.executionControl.keySecret is required: the node signs every execution and source ledger statement with it, and an unsigned acknowledgement is not proof." -}}
{{- end -}}
{{- if not $output.capabilityKeySecret -}}
{{- fail "hangarOutput.capabilityKeySecret is required: control capabilities are minted by the control plane and verified by the daemon with the same raw 32-byte key." -}}
{{- end -}}
{{- if not $output.daemon.tls.existingSecret -}}
{{- fail "hangarOutput.daemon.tls.existingSecret is required: the ATC calls this daemon's control API from another node, and a bearer capability over plaintext off-node is interceptable inside its TTL." -}}
{{- end -}}
{{- if kindIs "string" $output.activationEpoch -}}
{{- fail "hangarOutput.activationEpoch must be an integer, not a string" -}}
{{- end -}}
{{- if le (int $output.activationEpoch) 0 -}}
{{- fail "hangarOutput.activationEpoch is required and must be positive: a stale or absent epoch authorizes nothing, and zero is the absence." -}}
{{- end -}}
{{/*
validateScratch belongs to the BASE switch, not the output one. The DaemonSet,
its hangar-output-scratch emptyDir and its --scratch-dir flag all render under
$base, so with the check under $capture a base-control-only deployment rendered
`sizeLimit:` -- an explicit null, i.e. unbounded -- beneath a comment asserting
the limit was required and validated. Nothing writes that volume in base-only
mode today, so the exposure was the CLAIM; a render that documents a bound it
does not set is read once and believed.
*/}}
{{- include "concourse.hangarOutput.validateScratch" . -}}
{{- end -}}

{{- if $capture -}}
{{- include "concourse.hangarOutput.validateBucket" . -}}
{{- include "concourse.hangarOutput.validateKeys" . -}}
{{- include "concourse.hangarOutput.validateDurations" . -}}
{{- include "concourse.hangarOutput.validateControllers" . -}}
{{- include "concourse.hangarOutput.validatePrincipals" . -}}
{{- if not $output.database.existingSecret -}}
{{- fail "hangarOutput.database.existingSecret is required: the activation and drain Jobs use a PostgreSQL role of their own, distinct from the web pod's, which is what makes \"only the activation command writes hangar_output_activation_epochs\" enforceable rather than aspirational." -}}
{{- end -}}
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
{{- if $output.sharedBucketPrefixOnlyIsolation -}}
{{- fail "hangarOutput.sharedBucketPrefixOnlyIsolation is refused. Object-level permission is not expressible in a bucket policy, and storage.objects.list authority has no caller-visible prefix boundary, so prefix-only IAM inside a shared bucket is not an activation-compatible substitute for a dedicated one." -}}
{{- end -}}
{{- if and $output.cacheBucket (eq $output.bucket $output.cacheBucket) -}}
{{- fail (printf "hangarOutput.bucket is %q, which is hangarOutput.cacheBucket. The output plane needs a DEDICATED bucket: the cache tier is fail-open, name-keyed and re-derivable, and mixing the two puts objects with no ownership marker in the namespace inventory sweeps." $output.bucket) -}}
{{- end -}}
{{- if and $output.strictInputBucket (eq $output.bucket $output.strictInputBucket) -}}
{{- fail (printf "hangarOutput.bucket is %q, which is hangarOutput.strictInputBucket. The output plane needs a DEDICATED bucket; the strict-input bucket is caller-published and attests inputs." $output.bucket) -}}
{{- end -}}
{{- if and .Values.artifactDaemon.durable.bucket (eq $output.bucket .Values.artifactDaemon.durable.bucket) -}}
{{- fail (printf "hangarOutput.bucket is %q, which is artifactDaemon.durable.bucket. The output plane needs a DEDICATED bucket and never the durable cache one." $output.bucket) -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateKeys" -}}
{{- $output := .Values.hangarOutput -}}
{{- if not $output.receipt.keyID -}}
{{- fail "hangarOutput.receipt.keyID is required: a receipt names the key that can check it." -}}
{{- end -}}
{{- if not $output.receipt.privateKeySecret -}}
{{- fail "hangarOutput.receipt.privateKeySecret is required: the output daemon is the only process that holds the private half." -}}
{{- end -}}
{{- if not $output.materializationKeySecret -}}
{{- fail "hangarOutput.materializationKeySecret is required: output read grants use their own key and their own domain, never the receipt key." -}}
{{- end -}}

{{/* Three key roles, three Secrets. One Secret for two of them means rotating either rotates both. */}}
{{- $secrets := dict -}}
{{- range $role, $secret := dict "executionControl.keySecret" $output.executionControl.keySecret "capabilityKeySecret" $output.capabilityKeySecret "receipt.privateKeySecret" $output.receipt.privateKeySecret "materializationKeySecret" $output.materializationKeySecret -}}
{{- if $secret -}}
{{- if hasKey $secrets $secret -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s name the same Secret %q. They say different things -- a receipt says an object exists in a bucket, a control statement says a process on a node did something, a read grant authorizes one staged read -- and an activation epoch pins them separately, so one Secret for two roles means rotating either rotates both." (get $secrets $secret) $role $secret) -}}
{{- end -}}
{{- $_ := set $secrets $secret $role -}}
{{- end -}}
{{- end -}}

{{/* The public verification ring. */}}
{{- $active := dict -}}
{{- $byID := dict -}}
{{- $epochs := dict -}}
{{- range $index, $entry := $output.receipt.publicKeys -}}
{{- if not $entry.id -}}{{- fail (printf "hangarOutput.receipt.publicKeys[%d] has no id" $index) -}}{{- end -}}
{{- if not $entry.key -}}{{- fail (printf "hangarOutput.receipt.publicKeys[%d] (%s) has no key" $index $entry.id) -}}{{- end -}}
{{- if or (not (hasKey $entry "epoch")) (kindIs "string" $entry.epoch) -}}{{- fail (printf "hangarOutput.receipt.publicKeys[%d] (%s) has no integer epoch" $index $entry.id) -}}{{- end -}}
{{- if hasKey $byID $entry.id -}}
{{- if ne (get $byID $entry.id) $entry.key -}}
{{- fail (printf "hangarOutput.receipt.publicKeys declares the key id %q twice with two different public keys. A receipt key is never replaced IN PLACE: rotation creates a new activation epoch with a new key id, because an old private key is retained while its epoch still has an unsettled capture, and two keys under one id makes \"which key checks this receipt\" unanswerable." $entry.id) -}}
{{- end -}}
{{- end -}}
{{- $_ := set $byID $entry.id $entry.key -}}
{{- $_ := set $epochs (toString $entry.epoch) $entry.id -}}
{{- if eq $entry.id $output.receipt.keyID -}}
{{- $_ := set $active "entry" $entry -}}
{{- end -}}
{{- end -}}
{{- if not (hasKey $active "entry") -}}
{{- fail (printf "hangarOutput.receipt.keyID is %q and no entry in hangarOutput.receipt.publicKeys declares it. A receipt names the key that can check it; a control plane with no entry for the active key cannot verify a single one." $output.receipt.keyID) -}}
{{- end -}}
{{- $entry := get $active "entry" -}}
{{- if $entry.retired -}}
{{- fail (printf "hangarOutput.receipt.keyID is %q and its ring entry is retired. A retired key verifies old receipts; it does not sign new ones." $output.receipt.keyID) -}}
{{- end -}}
{{- if ne (int $entry.epoch) (int $output.activationEpoch) -}}
{{- fail (printf "hangarOutput.receipt.keyID %q is declared for epoch %d and hangarOutput.activationEpoch is %d. Rotation creates a NEW epoch rather than replacing a key in place, so the active key's entry names the active epoch; reusing one id across epochs is the in-place replacement the receipt-key rule forbids." $output.receipt.keyID (int $entry.epoch) (int $output.activationEpoch)) -}}
{{- end -}}
{{- range $referenced := $output.receipt.referencedEpochs -}}
{{- if not (hasKey $epochs (toString $referenced)) -}}
{{- fail (printf "hangarOutput.receipt.referencedEpochs names epoch %v and hangarOutput.receipt.publicKeys has no entry for it. Public verification material is retained while any durable state references its epoch -- a reservation, a receipt, a recovery row -- and dropping it makes those receipts unverifiable forever rather than merely unusable." $referenced) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateDurations" -}}
{{- $output := .Values.hangarOutput -}}
{{- $capture := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.captureDeadline" "value" $output.captureDeadline)) -}}
{{- $seal := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.sealDeadline" "value" $output.sealDeadline)) -}}
{{- $grace := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.publicationGrace" "value" $output.publicationGrace)) -}}
{{- $lease := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.leaseTerm" "value" $output.leaseTerm)) -}}
{{- $renew := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.leaseRenewInterval" "value" $output.leaseRenewInterval)) -}}

{{- if or (lt $capture 3600) (gt $capture 604800) -}}
{{- fail (printf "hangarOutput.captureDeadline is %s; it is configurable from 1h to 168h. Below an hour a legitimate slow capture is terminalised; above a week a lost capture pins its correlation for longer than anybody will look." $output.captureDeadline) -}}
{{- end -}}
{{- if or (lt $seal 30) (gt $seal 1800) -}}
{{- fail (printf "hangarOutput.sealDeadline is %s; it is configurable from 30s to 30m." $output.sealDeadline) -}}
{{- end -}}
{{- if gt $grace 2592000 -}}
{{- fail (printf "hangarOutput.publicationGrace is %s; the maximum is 720h (30 days)." $output.publicationGrace) -}}
{{- end -}}
{{- if lt $grace (add $capture 3600) -}}
{{- fail (printf "hangarOutput.publicationGrace is %s and hangarOutput.captureDeadline is %s. Grace must exceed the maximum capture deadline by at least an hour: below that, inventory can treat as an orphan an object whose capture is still entitled to register it, and the object is deleted out from under a live capture." $output.publicationGrace $output.captureDeadline) -}}
{{- end -}}
{{- if lt $lease 900 -}}
{{- fail (printf "hangarOutput.leaseTerm is %s; the minimum is 15m. A shorter term makes expiry -- rather than a fence -- the thing a worker races." $output.leaseTerm) -}}
{{- end -}}
{{- if gt $renew 60 -}}
{{- fail (printf "hangarOutput.leaseRenewInterval is %s; a lease is renewed at least once a minute, so a longer interval is a lease that expires under its own owner." $output.leaseRenewInterval) -}}
{{- end -}}
{{- if ge $renew $lease -}}
{{- fail (printf "hangarOutput.leaseRenewInterval (%s) is not shorter than hangarOutput.leaseTerm (%s)." $output.leaseRenewInterval $output.leaseTerm) -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateScratch" -}}
{{- $scratch := .Values.hangarOutput.daemon.scratch -}}
{{- if not $scratch.sizeLimit -}}
{{- fail "hangarOutput.daemon.scratch.sizeLimit is required. The output daemon canonicalizes and spools whole trees into an emptyDir, and an emptyDir with no sizeLimit is bounded only by the node's disk: filling it evicts every Pod on the node rather than only this one. Set it to at least concurrency times maxContentBytes." -}}
{{- end -}}
{{- $limit := atoi (include "concourse.quantityBytes" (dict "name" "hangarOutput.daemon.scratch.sizeLimit" "value" $scratch.sizeLimit)) -}}
{{- $concurrency := int $scratch.concurrency -}}
{{- if lt $concurrency 1 -}}
{{- fail "hangarOutput.daemon.scratch.concurrency must be at least 1; zero would admit no capture at all." -}}
{{- end -}}
{{- $needed := mul $concurrency (int64 $scratch.maxContentBytes) -}}
{{- if gt (int64 $needed) (int64 $limit) -}}
{{- fail (printf "hangarOutput.daemon.scratch.concurrency is %d and maxContentBytes is %v, so %v bytes may be spooled at once -- more than the sizeLimit %s (%v bytes). Either raise the limit or lower the concurrency: the bound only holds if their product fits." $concurrency $scratch.maxContentBytes $needed $scratch.sizeLimit $limit) -}}
{{- end -}}
{{- if not (hasPrefix "/" (clean (toString $scratch.path))) -}}
{{- fail "hangarOutput.daemon.scratch.path must be absolute" -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateControllers" -}}
{{- $output := .Values.hangarOutput -}}
{{- range $name, $controller := dict "inventory" $output.inventory "reclaimer" $output.reclaimer "policyAttestor" $output.policyAttestor -}}
{{- if not $controller.enabled -}}
{{- fail (printf "hangarOutput.%s.enabled is false while hangarOutput.enabled is true. An activation epoch attests compatible migrations AND the recovery, inventory and reclaim workers: a controller that is not deployed is a facet that cannot be attested, and a plane with no reclaimer keeps every published object forever while its status says it does not." $name) -}}
{{- end -}}
{{- if ne (int $controller.replicas) 1 -}}
{{- fail (printf "hangarOutput.%s.replicas is %d. There is exactly one durable cursor and one renewable lease owner per dedicated output bucket and activation epoch: a second replica is a second owner racing for the same lease, and parallel cursor owners are forbidden by construction rather than mitigated." $name (int $controller.replicas)) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validatePrincipals" -}}
{{- $accounts := dict -}}
{{- range $role, $name := dict
  "daemon" (include "concourse.hangarOutput.daemonServiceAccount" .)
  "inventory" (include "concourse.hangarOutput.inventoryServiceAccount" .)
  "reclaimer" (include "concourse.hangarOutput.reclaimerServiceAccount" .)
  "policyAttestor" (include "concourse.hangarOutput.attestorServiceAccount" .) -}}
{{- if hasKey $accounts $name -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s render the same Kubernetes service account %q. A service account is Pod-wide: two workloads sharing one are ONE cloud identity holding both sets of permissions, and no care inside either process takes that back. The publisher must not be able to list or delete; the reclaimer must not be able to create; the attestor must hold no object permission at all." (get $accounts $name) $role $name) -}}
{{- end -}}
{{- $_ := set $accounts $name $role -}}
{{- end -}}

{{- $principals := dict -}}
{{- range $role, $annotations := dict
  "daemon" .Values.hangarOutput.daemon.serviceAccount.annotations
  "inventory" .Values.hangarOutput.inventory.serviceAccount.annotations
  "reclaimer" .Values.hangarOutput.reclaimer.serviceAccount.annotations
  "policyAttestor" .Values.hangarOutput.policyAttestor.serviceAccount.annotations -}}
{{- $principal := get ($annotations | default dict) "iam.gke.io/gcp-service-account" -}}
{{- if $principal -}}
{{- if hasKey $principals $principal -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s are annotated with the same cloud principal %q. Shared Workload Identity principals are an activation failure: the whole separation is four cloud identities with four disjoint grants, and one principal bound to two Kubernetes service accounts has the union of both." (get $principals $principal) $role $principal) -}}
{{- end -}}
{{- $_ := set $principals $principal $role -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/* ------------------------------------------------- shared container pieces */}}

{{/*
outputNamespaceFlags are the derived-namespace flags every output workload that
addresses objects is given. One include, so a controller cannot be pointed at a
namespace the daemon does not publish into.
*/}}
{{- define "concourse.hangarOutput.namespaceFlags" -}}
- --output-store=gcs
- --output-bucket={{ .Values.hangarOutput.bucket }}
- --output-prefix={{ .Values.hangarOutput.prefix }}
- --output-tenant={{ .Values.hangarOutput.tenant }}
- --activation-epoch={{ int .Values.hangarOutput.activationEpoch }}
{{- if .Values.hangarOutput.endpoint }}
- --output-endpoint={{ .Values.hangarOutput.endpoint }}
{{- end }}
{{- end }}

{{/*
controllerPodSpec is the body every one of the three controllers shares:
database credential, security context, probes-by-liveness-of-process. Each is a
single bounded worker with no HTTP surface, so there is no readiness probe to
write -- the Deployment's one replica IS the readiness the lease enforces.
*/}}
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
