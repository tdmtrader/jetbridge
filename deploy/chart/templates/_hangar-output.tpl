{{/*
The Hangar output plane's names, its duration arithmetic, and its refusals.

Every rule here refuses a RENDER rather than letting a workload start and fail.
A render is what an operator reviews and what GitOps diffs; a configuration that
only a pod's startup rejects is one that reaches a cluster, CrashLoopBackOffs,
and is diagnosed from logs at three in the morning.
*/}}

{{/* ------------------------------------------------------------------ names */}}

{{/*
qualifiedName composes `<fullname>-<suffix>` inside the 63-character bound by
truncating the FULLNAME, never the composed string.

Truncating the composed string is what the four workload names used to do, and
the four suffixes share a 14-character `-hangar-output` prefix: once the
fullname reached 49 characters there were fewer than 14 distinguishing
characters left and all four collapsed to ONE name. validatePrincipals then
refused the render -- correctly, and with a message about service accounts --
for a problem the operator can only fix by renaming the release. The fullname is
`<release>-concourse-jetbridge`, so the chart was unusable at any release name
of 28 characters or more, and Helm permits 53.

Reserving the suffix instead keeps the four distinct at every length, because
the part that distinguishes them is the part that survives. Names stay
byte-identical for any fullname short enough not to need truncating, which is
every release name in use.

Expects a dict: root, suffix.
*/}}
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

{{- define "concourse.hangarOutput.attestorName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-policy-attestor") }}
{{- end }}

{{- define "concourse.hangarOutput.receiptKeysName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-receipt-keys") }}
{{- end }}

{{- define "concourse.hangarOutput.activationName" -}}
{{- include "concourse.hangarOutput.qualifiedName" (dict "root" . "suffix" "hangar-output-activation") }}
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

{{/*
daemonTLSServerName is the name the ATC verifies the output daemon's SERVER
certificate against.

The daemon is dialed at `<node InternalIP>:<port>` and renders no Service, so
there is no name in the dial at all -- and a node IP cannot be a SAN in a
certificate issued before that node existed. Verification is therefore against
a name the operator puts in the certificate, and this is the one place the
chart spells it: the same string reaches the ATC's flag and values.yaml's
instruction to the operator.
*/}}
{{- define "concourse.hangarOutput.daemonTLSServerName" -}}
{{- default (printf "%s.%s.svc" (include "concourse.hangarOutput.daemonName" .) .Release.Namespace) .Values.hangarOutput.daemon.tls.serverName }}
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
{{- if not .Values.artifactDaemon.enabled -}}
{{- fail "hangarOutput.executionControl.enabled requires artifactDaemon.enabled. Every pod this runtime builds -- capture-selected or ordinary -- carries a REQUIRED node affinity on concourse.dev/artifact-cache=ready before the two output labels are appended, and the artifact daemon is the only thing that sets that label. With it disabled no node ever carries it, so every pod sits Pending until its deadline expires and the failure names a timeout rather than a label: the plane would render complete and schedule nothing." -}}
{{- end -}}
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
{{- fail "hangarOutput.daemon.tls.clientSecret is required: this daemon's control API is TLS-only and refuses every operation whose request carries no VERIFIED peer certificate, so an ATC with no client certificate of its own can hold no source, issue no writer ticket, seal nothing, publish nothing and grant no read. It is the OUTPUT plane's credential and not artifactDaemon.tls.enabled's: that switch belongs to a different daemon on a different bucket under a different identity, and a certificate from its CA handshakes here and is then refused by every route." -}}
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
{{- include "concourse.hangarOutput.validateIntervals" . -}}
{{- include "concourse.hangarOutput.validateKeys" . -}}
{{- include "concourse.hangarOutput.validateDurations" . -}}
{{- include "concourse.hangarOutput.validateControllers" . -}}
{{- include "concourse.hangarOutput.validatePrincipals" . -}}
{{- include "concourse.hangarOutput.validateCloudIdentities" . -}}
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

{{/*
Three key roles, three ids. A receipt says an object exists in a bucket, a
control statement says a process on a node did something, and a read grant
authorizes one staged read; "which key checks this" has to have one answer per
id, and a shared id makes it two.
*/}}
{{- $ids := dict -}}
{{- range $role, $id := dict "executionControl.keyID" $output.executionControl.keyID "receipt.keyID" $output.receipt.keyID "materializationKeyID" $output.materializationKeyID -}}
{{- if $id -}}
{{- if hasKey $ids $id -}}
{{- fail (printf "hangarOutput.%s and hangarOutput.%s are both the key id %q. A key id names one piece of key material for one role, and an activation epoch pins the three separately: one id for two roles makes \"which key checks this\" unanswerable." (get $ids $id) $role $id) -}}
{{- end -}}
{{- $_ := set $ids $id $role -}}
{{- end -}}
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

{{/*
validatePrincipals covers EVERY service account this chart renders, not only the
four output roles.

It used to cover four of seven, and the three it did not were the three an
operator can actually reach by accident:

  - hangarOutput.activation.serviceAccount.name pointed at the reclaimer's
    account rendered TWO ServiceAccount objects with one name. Which one the
    apply leaves standing decides whether the reclaimer keeps its Workload
    Identity annotation -- and a reclaimer whose annotation was stripped has no
    delete authority, so the plane keeps every published object forever while
    its status says it reclaims;
  - the reclaimer's cloud principal on the activation account's annotation is
    the same union-of-grants defect the four-role check already refuses, one
    account over;
  - serviceAccount.name -- the TOP-LEVEL one -- set to the reclaimer's account
    makes the *web* Deployment run as the delete-holding identity. Req 54 is
    explicit that web/control-plane, task, cache and strict-input identities
    have no role on the output bucket, and this is the one values override that
    gives web all of them;
  - kubernetes.serviceAccount is the TASK pods' account, and it is the worst of
    the four. Task pods run arbitrary user-supplied code; the reclaimer holds
    the only storage.objects.delete on the output bucket. Req 54 names this
    case by hand -- "shared KSAs, shared Workload Identity principals,
    prefix-only isolation in a mixed bucket, OR TASK CREDENTIALS are activation
    failures". An empty value needs nothing: it means "the web SA", which the
    serviceAccount subject already covers.

That this list has now been extended four times is the argument for the guard
that reads it. deploy/chart/tests/hangar_output_principals_test.go enumerates
every `.Values.…serviceAccount[.name]` any template consults, points each at
another workload's account and requires the refusal, so a fifth override cannot
be added without either landing here or turning that rule red.

The web account is checked by NAME even when serviceAccount.create is false: a
pre-provisioned account named after the reclaimer's is the same Pod running as
the same identity, and the chart not rendering the object does not make that
untrue. Its PRINCIPAL is only checked when the chart renders the annotation,
because that is the only case where the chart is the thing asserting it.
*/}}
{{/*
validateIntervals checks the three controllers' cadences, which nothing checked.

Every other duration in this plane is parsed and bounded at render time. The
attestor's was not, and it is the one whose bound has a consequence written into
the SCHEMA: a policy snapshot older than fifteen minutes is stale, a stale
snapshot puts the plane at-risk, and at-risk blocks five kinds of admission. An
interval above that bound guarantees the state it is supposed to prevent.
*/}}
{{- define "concourse.hangarOutput.validateIntervals" -}}
{{- $output := .Values.hangarOutput -}}
{{- $_ := include "concourse.durationSeconds" (dict "name" "hangarOutput.inventory.interval" "value" $output.inventory.interval) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaimer.interval" "value" $output.reclaimer.interval) -}}
{{- $_ = include "concourse.durationSeconds" (dict "name" "hangarOutput.reclaimer.deleteTimeout" "value" $output.reclaimer.deleteTimeout) -}}
{{- $attest := atoi (include "concourse.durationSeconds" (dict "name" "hangarOutput.policyAttestor.interval" "value" $output.policyAttestor.interval)) -}}
{{- if gt $attest 900 -}}
{{- fail (printf "hangarOutput.policyAttestor.interval is %s; the maximum is 15m. Policy evidence older than fifteen minutes is stale, the schema enforces that bound on every admission, and a stale snapshot puts the plane at-risk -- so a longer refresh interval does not make detection slower, it makes the plane at-risk between every pass." $output.policyAttestor.interval) -}}
{{- end -}}
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
  (dict "path" "hangarOutput.policyAttestor"
        "name" (include "concourse.hangarOutput.attestorServiceAccount" .)
        "annotations" .Values.hangarOutput.policyAttestor.serviceAccount.annotations)
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
{{- if .Values.artifactDaemon.enabled -}}
{{- $subjects = append $subjects (dict
      "path" "artifactDaemon"
      "name" (printf "%s-artifact-daemon" (include "concourse.fullname" .))
      "annotations" dict) -}}
{{- end -}}

{{- $accounts := dict -}}
{{- range $subject := $subjects -}}
{{- $name := $subject.name -}}
{{- if hasKey $accounts $name -}}
{{- fail (printf "%s and %s render the same Kubernetes service account %q. A service account is Pod-wide: two workloads sharing one are ONE cloud identity holding both sets of permissions, and no care inside either process takes that back. The publisher must not be able to list or delete; the reclaimer must not be able to create; the attestor must hold no object permission at all; and web, task, cache and strict-input identities hold no output role whatsoever." (get $accounts $name) $subject.path $name) -}}
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

{{/*
cloudIdentity is the IAM member bound to one output role's Kubernetes service
account, read off the Workload Identity annotation the chart renders on that
account.

It is not the service account's NAME. The policy attestor compares the bucket's
IAM policy against these four values and the matcher normalises an IAM member
("serviceAccount:a@b" and "a@b" are one identity), so a Kubernetes name here
matches no binding on any real bucket: every role attests insufficient_role,
every real principal on the bucket becomes a stranger, and the epoch goes
permanently at risk. It fails CLOSED, which is why nothing noticed -- and it
means Req 54's "activation verifies the KSA-to-cloud identity bindings" could
never succeed as deployed.

An empty annotation fails the render rather than rendering an empty identity,
because an empty member matches no binding either and is indistinguishable from
a principal that really has no role: the operator would be shown a plane whose
publisher holds nothing, for a deployment whose Workload Identity is correct
and merely undeclared.
*/}}
{{- define "concourse.hangarOutput.cloudIdentity" -}}
{{- $principal := get (.annotations | default dict) "iam.gke.io/gcp-service-account" -}}
{{- if not $principal -}}
{{- fail (printf "%s.serviceAccount.annotations has no iam.gke.io/gcp-service-account. The policy attestor compares the output bucket's IAM policy against the four principals this plane claims to have, and a Kubernetes service account name -- or an empty one -- matches no member on any real bucket: all four roles attest insufficient_role, every real principal becomes a stranger and the epoch goes permanently at risk. The chart cannot verify the binding; activation does. It does need to be told what it is." .path) -}}
{{- end -}}
{{- $principal -}}
{{- end }}

{{/*
validateCloudIdentities requires all four before anything renders, so the
refusal names the value rather than arriving from whichever template happened
to be first.
*/}}
{{- define "concourse.hangarOutput.validateCloudIdentities" -}}
{{- $output := .Values.hangarOutput -}}
{{- range $path, $values := dict "hangarOutput.daemon" $output.daemon "hangarOutput.inventory" $output.inventory "hangarOutput.reclaimer" $output.reclaimer "hangarOutput.policyAttestor" $output.policyAttestor -}}
{{- $_ := include "concourse.hangarOutput.cloudIdentity" (dict "path" $path "annotations" $values.serviceAccount.annotations) -}}
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
{{/*
No liveness probe, and that is a gap rather than a decision.

The absent READINESS probe is argued above and is right: these three serve
nothing, and the one replica is the readiness the lease enforces. Liveness is a
different question -- a wedged worker holding a lease is exactly what a liveness
probe exists for, and this plane's status surface already knows how to say "the
sweep has stalled". What it needs is a heartbeat the controller loop writes, and
that loop lives in atc/hangaroutput/controller rather than here. Recorded so
that the absence is not read as a considered one.
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
