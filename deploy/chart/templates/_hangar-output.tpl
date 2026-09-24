{{/* Reserve suffix length so long release names cannot collapse distinct workload names. */}}
{{- define "concourse.hangarOutput.qualifiedName" -}}
{{- $suffix := .suffix -}}
{{- $budget := int (sub 62 (len $suffix)) -}}
{{- if lt $budget 1 -}}
{{- fail (printf "the Hangar output plane cannot compose a name for %q: the suffix alone is %d characters and a Kubernetes object name is 63" $suffix (len $suffix)) -}}
{{- end -}}
{{- printf "%s-%s" (include "concourse.fullname" .root | trunc $budget | trimSuffix "-") $suffix -}}
{{- end }}

{{- define "concourse.hangarOutput.daemonName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-daemon") }}
{{- end }}

{{- define "concourse.hangarOutput.inventoryName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-inventory") }}
{{- end }}

{{- define "concourse.hangarOutput.reclaimerName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-reclaimer") }}
{{- end }}


{{- define "concourse.hangarOutput.receiptKeysName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-receipt-keys") }}
{{- end }}

{{- define "concourse.hangarOutput.activationName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-activation") }}
{{- end }}

{{- define "concourse.hangarOutput.daemonServiceAccount" -}}
{{- default (include "concourse.hangarOutput.daemonName" .) .Values.hangarOutput.daemon.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.inventoryServiceAccount" -}}
{{- default (include "concourse.hangarOutput.inventoryName" .) .Values.hangarOutput.inventory.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.reclaimerServiceAccount" -}}
{{- default (include "concourse.hangarOutput.reclaimerName" .) .Values.hangarOutput.reclaimer.serviceAccount.name }}
{{- end }}


{{- define "concourse.hangarOutput.activationServiceAccount" -}}
{{- default (include "concourse.hangarOutput.activationName" .) .Values.hangarOutput.activation.serviceAccount.name }}
{{- end }}

{{- define "concourse.hangarOutput.daemonTLSServerName" -}}
{{- default (printf "%s.%s.svc" (include "concourse.hangarOutput.daemonName" .) .Release.Namespace) .Values.hangarOutput.daemon.tls.serverName }}
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
{{- with .Values.hangarOutput.readControlURL -}}
{{- $url := urlParse . -}}
{{- if or (ne $url.scheme "https") (empty $url.host) (not (empty $url.userinfo)) (not (empty $url.query)) (not (empty $url.fragment)) -}}
{{- fail "hangarOutput.readControlURL must be an HTTPS web API URL without credentials, query or fragment" -}}
{{- end -}}
{{- end -}}

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

{{- if hasKey .Values.web "enablePipelineRunCreation" -}}
{{- fail "web.enablePipelineRunCreation has been removed; Run admission is set by web.pipelineRunActivationEpoch (default 1 admits; 0 admits nothing). Remove the old value and set the epoch." -}}
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
{{- if not $output.executionControl.keySecret -}}
{{- fail "hangarOutput.executionControl.keySecret is required: the node signs every execution and source ledger statement with it, and an unsigned acknowledgement is not proof." -}}
{{- end -}}
{{- if not $output.executionControl.keyID -}}
{{- fail "hangarOutput.executionControl.keyID is required: it is the id every node reports over the attestation handshake, and base attestation is a homogeneity check over exactly those ids. It names KEY MATERIAL and not the Secret -- it used to render the Secret NAME, so two nodes holding different private keys under one Secret name reported one id and a cohort half-way through a rollout attested as homogeneous." -}}
{{- end -}}
{{- if not $output.capabilityKeySecret -}}
{{- fail "hangarOutput.capabilityKeySecret is required: control capabilities are minted by the control plane and verified by the daemon with the same raw 32-byte key." -}}
{{- end -}}
{{- if not $output.daemon.tls.existingSecret -}}
{{- fail "hangarOutput.daemon.tls.existingSecret is required: the ATC calls this daemon's control API from another node, and a bearer capability over plaintext off-node is interceptable inside its TTL." -}}
{{- end -}}
{{- if not $output.daemon.tls.clientSecret -}}
{{- fail "hangarOutput.daemon.tls.clientSecret is required: this daemon's control API is TLS-only and refuses every operation whose request carries no VERIFIED peer certificate, so an ATC with no client certificate of its own can hold no source, issue no writer ticket, seal nothing, publish nothing and issue no read warrant. It is the OUTPUT plane's credential, separate from artifactDaemon.tls: that configuration belongs to a different daemon on a different bucket under a different identity, and a certificate from its CA handshakes here and is then refused by every route." -}}
{{- end -}}
{{- if eq $output.daemon.tls.clientSecret $output.daemon.tls.existingSecret -}}
{{- fail (printf "hangarOutput.daemon.tls.clientSecret and hangarOutput.daemon.tls.existingSecret are both %q. existingSecret holds tls.key -- the key this daemon SERVES with -- and it is mounted in the daemon Pod and nowhere else: whatever else held it could impersonate the output daemon to the ATC. A client needs a CLIENT certificate, issued by the same CA and kept in its own Secret." $output.daemon.tls.existingSecret) -}}
{{- end -}}
{{- if kindIs "string" $output.activationEpoch -}}
{{- fail "hangarOutput.activationEpoch must be an integer, not a string" -}}
{{- end -}}
{{- if le (int $output.activationEpoch) 0 -}}
{{- fail "hangarOutput.activationEpoch is required and must be positive: a stale or absent epoch authorizes nothing, and zero is the absence." -}}
{{- end -}}
{{- include "concourse.hangarOutput.validateScratch" . -}}
{{- end -}}

{{- if $capture -}}
{{- include "concourse.hangarOutput.validateBucket" . -}}
{{- include "concourse.hangarOutput.validateIntervals" . -}}
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
{{- if and (eq $output.store "gcs") .Values.artifactDaemon.durable.bucket (eq $output.bucket .Values.artifactDaemon.durable.bucket) -}}
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
{{- fail "hangarOutput.materializationKeySecret is required: output read warrants use their own key and their own domain, never the receipt key." -}}
{{- end -}}

{{- $ids := dict -}}
{{- range $role, $id := dict "executionControl.keyID" $output.executionControl.keyID "receipt.keyID" $output.receipt.keyID "materializationKeyID" $output.materializationKeyID -}}
{{- if $id -}}
{{- if hasKey $ids $id -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s are both the key id %q. A key id names one piece of key material for one role, and an activation epoch pins the three separately: one id for two roles makes \"which key checks this\" unanswerable." (get $ids $id) $role $id) -}}
{{- end -}}
{{- $_ := set $ids $id $role -}}
{{- end -}}
{{- end -}}

{{- $secrets := dict -}}
{{- range $role, $secret := dict "executionControl.keySecret" $output.executionControl.keySecret "capabilityKeySecret" $output.capabilityKeySecret "receipt.privateKeySecret" $output.receipt.privateKeySecret "materializationKeySecret" $output.materializationKeySecret -}}
{{- if $secret -}}
{{- if hasKey $secrets $secret -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s name the same Secret %q. They say different things -- a receipt says an object exists in a bucket, a control statement says a process on a node did something, a read warrant authorizes one staged read -- and an activation epoch pins them separately, so one Secret for two roles means rotating either rotates both." (get $secrets $secret) $role $secret) -}}
{{- end -}}
{{- $_ := set $secrets $secret $role -}}
{{- end -}}
{{- end -}}

{{- $controlEpochs := dict -}}
{{- range $entry := $output.executionControl.publicKeys -}}
{{- $epoch := toString $entry.epoch -}}
{{- if or (kindIs "string" $entry.epoch) (le (int $entry.epoch) 0) (hasKey $controlEpochs $epoch) (ne (len ($entry.key | default "" | b64dec)) 32) -}}
{{- fail "hangarOutput.executionControl.publicKeys requires one base64 Ed25519 public key per positive integer epoch" -}}
{{- end -}}
{{- $_ := set $controlEpochs $epoch true -}}
{{- end -}}
{{- if not (hasKey $controlEpochs (toString $output.activationEpoch)) -}}
{{- fail "hangarOutput.executionControl.publicKeys has no key for the active epoch; source hold recovery cannot verify node statements" -}}
{{- end -}}

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

{{- define "concourse.hangarOutput.validateControllers" -}}
{{- $output := .Values.hangarOutput -}}
{{- range $name, $controller := dict "inventory" $output.inventory "reclaimer" $output.reclaimer -}}
{{- if not $controller.enabled -}}
{{- fail (printf "hangarOutput.%s.enabled is false while hangarOutput.enabled is true. An activation epoch attests compatible migrations AND the recovery, inventory and reclaim workers: a controller that is not deployed is a facet that cannot be attested, and a plane with no reclaimer keeps every published object forever while its status says it does not." $name) -}}
{{- end -}}
{{- if ne (int $controller.replicas) 1 -}}
{{- fail (printf "hangarOutput.%s.replicas is %d. There is exactly one durable cursor and one renewable lease owner per dedicated output bucket and activation epoch: a second replica is a second owner racing for the same lease, and parallel cursor owners are forbidden by construction rather than mitigated." $name (int $controller.replicas)) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.validateIntervals" -}}
{{- $output := .Values.hangarOutput -}}
{{- $_ := include "concourse.durationSeconds" (dict "name" "hangarOutput.inventory.interval" "value" $output.inventory.interval) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaimer.interval" "value" $output.reclaimer.interval) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaimer.deleteTimeout" "value" $output.reclaimer.deleteTimeout) -}}
{{- end }}

{{- define "concourse.hangarOutput.validatePrincipals" -}}
{{- $subjects := list
  (dict "path" "hangarOutput.daemon"
        "name" (include "concourse.hangarOutput.daemonServiceAccount" .)
        "annotations" .Values.hangarOutput.daemon.serviceAccount.annotations)
  (dict "path" "hangarOutput.inventory"
        "name" (include "concourse.hangarOutput.inventoryServiceAccount" .)
        "annotations" .Values.hangarOutput.inventory.serviceAccount.annotations)
  (dict "path" "hangarOutput.reclaimer"
        "name" (include "concourse.hangarOutput.reclaimerServiceAccount" .)
        "annotations" .Values.hangarOutput.reclaimer.serviceAccount.annotations)
  (dict "path" "hangarOutput.activation"
        "name" (include "concourse.hangarOutput.activationServiceAccount" .)
        "annotations" .Values.hangarOutput.activation.serviceAccount.annotations)
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
      "annotations" dict) -}}

{{- $accounts := dict -}}
{{- range $subject := $subjects -}}
{{- $name := $subject.name -}}
{{- if hasKey $accounts $name -}}
{{- fail (printf "%s and %s render the same Kubernetes service account %q. A service account is Pod-wide: two workloads sharing one are ONE cloud identity holding both sets of permissions, and no care inside either process takes that back. The publisher must not be able to list or delete; the reclaimer must not be able to create; and web, task, cache and strict-input identities hold no output role whatsoever." (get $accounts $name) $subject.path $name) -}}
{{- end -}}
{{- $_ := set $accounts $name $subject.path -}}
{{- end -}}

{{- $principals := dict -}}
{{- range $subject := $subjects -}}
{{- $principal := get ($subject.annotations | default dict) "iam.gke.io/gcp-service-account" -}}
{{- if $principal -}}
{{- if hasKey $principals $principal -}}
{{- fail (printf "%s and %s are annotated with the same cloud principal %q. Shared Workload Identity principals are an activation failure: the whole separation is distinct cloud identities with disjoint grants, and one principal bound to two Kubernetes service accounts has the union of both." (get $principals $principal) $subject.path $principal) -}}
{{- end -}}
{{- $_ := set $principals $principal $subject.path -}}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "concourse.hangarOutput.namespaceFlags" -}}
- --output-store={{ .Values.hangarOutput.store }}
- --output-bucket={{ .Values.hangarOutput.bucket }}
- --output-prefix={{ .Values.hangarOutput.prefix }}
- --output-tenant={{ .Values.hangarOutput.tenant }}
- --activation-epoch={{ int .Values.hangarOutput.activationEpoch }}
{{- if eq .Values.hangarOutput.store "disk" }}
- --output-endpoint={{ include "concourse.hangarStorage.endpoint" . }}
- --output-store-id={{ .Values.hangarStorage.disk.storeID }}
- --output-token-file=/etc/concourse/hangar-disk/token
- --output-ca-cert=/etc/concourse/hangar-disk/ca.crt
{{- else if .Values.hangarOutput.endpoint }}
- --output-endpoint={{ .Values.hangarOutput.endpoint }}
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
