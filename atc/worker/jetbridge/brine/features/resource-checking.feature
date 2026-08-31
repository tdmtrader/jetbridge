Feature: Deciding what to check, and resolving what can be answered without a pod

  lidar is the tick. Once an interval it asks the database which resources and
  which resource types exist, and then, for each one, does one of two things:
  writes a check plan onto a queue so a pod will go and ask the resource what
  versions it has, or — for an image that a registry can answer about directly
  — resolves the digest itself and writes it down. Everything after that
  depends on it. A resource lidar declines to check never produces a new
  version, so the jobs that trigger on it never run again and nobody is told;
  a resource it checks too eagerly costs a pod every tick; an image it resolves
  from the wrong repository is the wrong image, silently, in every step that
  pulls it.

  Source: atc/lidar/scanner_test.go, 34 specs across three Describes. There is
  no atc/lidar/checker_test.go in this tree — the assessment named one, and
  lidar has held only scanner.go since `resource_checking` moved into it.

  WHAT IS DIFFERENT HERE, AND WHY IT IS NOT MERELY DIFFERENT.

  The ginkgo suite already ran against real PostgreSQL, so there was no
  recording double to replace — except one. The scanner is constructed with an
  imageresolver.Resolver, and the suite passed a counterfeiter fake and
  asserted `ResolveCallCount()` and `ResolveArgsForCall(0)`. That is a call
  count, and it is weaker than it looks: a scanner that asked the right
  registry and then wrote somebody else's digest into the database passed
  every one of those assertions.

  The registry below answers instead of recording. It holds images at
  repository and tag and hands back the digest it holds, so a scenario that
  seeds two repositories with two digests can say WHICH one was resolved by
  naming the digest that reached the row. Credentials work the same way: the
  registry refuses a private image unless the password matches, so "the digest
  landed" IS the assertion that the credentials survived the trip, rather than
  a comparison against a struct field the test itself supplied.

  The three failures are injected without fabricating a single error. A closed
  connection for the database going away; a REAL `ALTER TABLE ... RENAME` for
  the second enumeration failing while the first still works; and, for the two
  garbage-collection races, the scope row is genuinely DELETED, so the foreign
  key violation is PostgreSQL's own, on the real constraint, at the real
  moment. The ginkgo suite hand-built a `*pgconn.PgError` with SQLSTATE 23503;
  a scanner classifying on error text rather than on the driver error would
  have passed that and would fail here.

  # ==========================================================================
  # What the scan enumerates, and what it refuses
  # ==========================================================================

  # lidar makes two reads before it does anything: the resources, then the
  # resource types. If the first is refused there is nothing to do and the
  # error has to travel, because the component runner is what decides whether
  # to retry and how loudly to complain. A scan that swallowed it would report
  # a clean pass every tick over a database it cannot read, and the only
  # symptom would be resources that quietly stop producing versions.
  #
  # There is no "and nothing was checked" clause here on purpose. It would
  # pass whatever the scanner did: a swallowed enumeration failure leaves no
  # resources to check either, so the absence distinguishes nothing. The
  # refusal is the whole assertion, and the scenario below is where the
  # absence becomes worth writing.
  #
  # Reddened by: `return err` becoming `return nil` after checkFactory.Resources()
  # in scanner.go's Run.
  Scenario: A scan whose resource enumeration the database refuses says so
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "waiting-resource"
    And the database the scan reads has gone away
    When the scan runs
    Then the scan was refused, saying "closed"

  # ...and the half where the absence does distinguish something. Here the
  # first enumeration SUCCEEDED — the resource is loaded and in hand — and only
  # the second failed. A scanner that logged that and carried on would check
  # every resource against an empty set of resource types, which for a resource
  # sitting on a custom type means a check plan with no parent image: the check
  # runs against the wrong image or fails to start, once per resource, every
  # tick, on a pipeline that is perfectly healthy.
  #
  # Nothing but a real table rename can arrange this. A closed connection fails
  # the first enumeration too, so the scanner never reaches the one under test.
  #
  # Reddened by: `return err` becoming `return nil` after
  # checkFactory.ResourceTypesByPipeline() — the resource is then checked, and
  # the second clause fails while the first one does too.
  Scenario: A scan that loaded its resources but cannot read their types checks nothing
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "waiting-resource"
    And the resource types table has been renamed out from under the scan
    When the scan runs
    Then the scan was refused, saying "resource_types"
    And no check was enqueued for "waiting-resource"

  # ==========================================================================
  # Which resources are checked
  # ==========================================================================

  # A resource nothing reads is an output, and once its last check succeeded
  # there is nothing to learn by checking it again — a put writes the version,
  # it is not discovered. Checking outputs anyway costs a pod per output per
  # tick forever, on every pipeline in the cluster.
  #
  # Both resources below have already been checked successfully, and that is
  # the whole point: the only thing separating them is that a job reads one of
  # them. Without the sibling this scenario would pass on a rule as coarse as
  # "never check anything that has been checked", which would be the far worse
  # bug of the two.
  #
  # Reddened by: dropping the `ji.resource_id != nil` branch from
  # checkFactory.Resources' predicate — the put-only resource is then checked.
  Scenario: An output nobody reads is not checked again once its check succeeded
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "input-resource"
    And the pipeline has the resource "put-only-resource" written by a job but never read
    And the resource "input-resource" has already been checked successfully
    And the resource "put-only-resource" has already been checked successfully
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "input-resource"
    And no check was enqueued for "put-only-resource"

  # `check_every: never` is the escape hatch for a resource whose upstream
  # cannot take the traffic, or that is driven entirely by webhooks. It is an
  # instruction, and a scan that ignored it would hammer exactly the endpoint
  # an operator went out of their way to protect.
  #
  # Reddened by: deleting the `CheckEvery().Never` guard from scanner.go's
  # check(). The sibling is what keeps this from passing on a scan that
  # checked nothing at all.
  Scenario: A resource told never to be checked is left alone, and its neighbours are not
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "never-resource"
    And the resource "never-resource" is checked never
    And the pipeline has the resource "ordinary-resource"
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "ordinary-resource"
    And no check was enqueued for "never-resource"

  # The scan fans out over a fixed number of workers and a pipeline can hold
  # far more resources than that. Every one has to be reached: a resource the
  # fan-out drops is a resource that never checks, and because the scan still
  # reports success nothing anywhere says so. Twenty against five is the
  # smallest ratio where a worker that stops after one item, or a producer that
  # only offers as many as there are workers, is visibly wrong.
  #
  # The assertion is a set, not a count. Twenty checks of the same resource is
  # exactly what a broken fan-out produces, and a count would pass on it.
  #
  # Reddened by: the worker loop in scanResources returning after handling one
  # resource instead of looping, or the producer goroutine ranging over
  # `resources[:maxConcurrency]`.
  Scenario: Every resource is checked even when there are four times as many as there are workers
    Given a lidar scan that was given no image resolver
    And the scan runs 5 resources at a time
    And the pipeline has 20 resources
    When the scan runs
    Then the scan completed without error
    And 20 checks were enqueued
    And every resource in the pipeline was checked exactly once

  # A pin is a promise: this resource stays at this version until a human says
  # otherwise. lidar keeps it by handing the pinned version to the check as the
  # version to start from. Forwarding nothing means the check re-discovers the
  # newest version and the pipeline moves off the pin it was held at, which is
  # the failure operators pin to prevent — and forwarding a version for an
  # UNPINNED resource is the same bug from the other side, freezing a resource
  # nobody asked to freeze.
  #
  # Both are here because one sentence in scanner.go decides both, so a
  # scenario carrying only one of them leaves the other free to drift.
  #
  # Reddened by: `version := checkable.CurrentPinnedVersion()` becoming
  # `var version atc.Version` — the pinned row fails. Or becoming a constant —
  # the unpinned row fails.
  Scenario: A pinned resource is checked from its pin and an unpinned one from nothing
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "pinned-resource"
    And the pipeline has the resource "unpinned-resource"
    And the resource "pinned-resource" is pinned to the version "ref: pinned-version"
    When the scan runs
    Then the scan completed without error
    And the check plan for "pinned-resource" starts from the version "ref: pinned-version"
    And the check plan for "unpinned-resource" starts from the version "nothing"

  # scanner.go wraps each unit of work in its own recover, and the comment
  # beside it says why: "so we don't lose the worker go routine if there's a
  # panic". With one worker there is exactly one goroutine to lose, and losing
  # it means every resource after the crashing one goes unchecked for as long
  # as the process lives — the ATC keeps reporting healthy scans while a
  # growing set of resources silently stops updating.
  #
  # The panic comes out of the registry, which reaches the guard through the
  # native-resolution branch. It is the same recover that covers the check
  # branch beside it; nothing about the guard is specific to which arm panicked.
  #
  # The registry HOLDS broken/image:latest as well as crashing on it, and that
  # is what makes the crash a fault rather than a decoration. "Left unresolved"
  # is satisfied by any refusal, and a registry that does not hold an image
  # refuses it too — so with nothing held, this scenario passed whether or not
  # the panic ever fired. That was MEASURED by an audit, and it was true: the
  # crash was injected without a witness. Holding the image supplies the
  # witness, because the only remaining reason the row can be unresolved is
  # that the resolve crashed — without the panic the digest would have landed.
  #
  # Reddened by: deleting the `defer util.DumpPanic(recover(), ...)` from the
  # worker in scanResources — the goroutine dies and the ordinary resource
  # behind it is never reached. Or by hoisting the recover out of the per-item
  # closure, which leaves the worker recovered but the loop abandoned. And, for
  # the crash itself, by deleting the "crashes when asked for" line below —
  # MEASURED, the last clause then goes red naming broken-image among the
  # resources the scan attached to a config, and restoring the line turns it
  # green again.
  Scenario: A crash scanning one resource does not cost the resources behind it
    Given a lidar scan backed by an image registry
    And the scan runs 1 resources at a time
    And the registry holds "broken/image:latest" at the digest "sha256:broken"
    And the registry crashes when asked for "broken/image:latest"
    And the pipeline has the image resource "broken-image" reading "broken/image:latest"
    And the pipeline has the resource "ordinary-resource"
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "ordinary-resource"
    And the resource "broken-image" was left unresolved

  # ==========================================================================
  # The document the check pod runs
  # ==========================================================================

  # The check plan is not a record of a call — it is the instruction the check
  # build executes, and every field below decides something the pod then does.
  # The source picks the registry it talks to. The tags decide which workers
  # will run it, and a plan that lost them schedules onto workers that cannot
  # reach the resource at all. The timeout is what stops a hung check holding
  # a worker forever, and the interval is what the check itself re-reads to
  # decide whether it should run.
  #
  # This assertion travels through lidar but pins atc/db's CheckPlan and the
  # planner behind it. That is where it belongs and where a mutation to it also
  # reddens atc/builds/planner_test.go; what is lidar's own here is that the
  # checkable reaching the plan is this resource and not another.
  #
  # Reddened by: any field dropped on the way into the plan — most cheaply, by
  # `Source: sourceDefaults.Merge(r.config.Source)` becoming
  # `Source: sourceDefaults` in resource.go's CheckPlan.
  Scenario: The check plan carries the resource configuration a check pod needs
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "base-resource"
    And the resource "base-resource" carries the tags "tag-a,tag-b"
    And the resource "base-resource" times out after "7m"
    And the resource "base-resource" is checked every "23m"
    When the scan runs
    Then the scan completed without error
    And the check plan for "base-resource" checks the resource "base-resource"
    And the check plan for "base-resource" runs the type "global-base-type"
    And the check plan for "base-resource" has the source "repository: base-resource"
    And the check plan for "base-resource" carries the tags "tag-a,tag-b"
    And the check plan for "base-resource" times out after "7m"
    And the check plan for "base-resource" repeats every "23m0s"
    And the check plan for "base-resource" pulls its image from the base type "global-base-type"

  # A resource built on a custom type cannot be checked until that type's own
  # image exists on the worker, and the plan is where that dependency is
  # written down: a nested check of the parent type, and a nested fetch of what
  # the check found. Lose the nested check and the type is never re-checked, so
  # the resource is checked forever against whatever image was current the day
  # the pipeline was set. Lose the nested fetch and the check pod has no image
  # to run at all.
  #
  # This is also the only observable proof of lidar's own
  # `resourceTypesMap[rs.PipelineID()]`: the nested plans exist only because
  # the scan handed this resource its own pipeline's resource types.
  #
  # Reddened by: passing nil resource types to TryCreateCheck, or looking the
  # map up under any key other than the resource's pipeline — the nested check
  # and fetch both disappear and this scenario reports that the custom type
  # would never be checked.
  Scenario: A resource on a custom type gets a plan that checks and fetches the type first
    Given a lidar scan that was given no image resolver
    And the pipeline has the custom resource type "custom-type" reading "custom-image" tagged "type-tag"
    And the pipeline has the resource "custom-resource" of the custom type "custom-type"
    When the scan runs
    Then the scan completed without error
    And the check plan for "custom-resource" runs the type "custom-type"
    And the check plan for "custom-resource" pulls its image from the base type "global-base-type"
    And the parent type check in the plan for "custom-resource" names "custom-type"
    And the parent type check in the plan for "custom-resource" has the source "repository: custom-image"
    And the parent type check in the plan for "custom-resource" carries the tags "type-tag"
    And the parent type fetch in the plan for "custom-resource" names "custom-type"
    And the parent type fetch in the plan for "custom-resource" runs the type "global-base-type"
    And the parent type fetch in the plan for "custom-resource" has the source "repository: custom-image"

  # ==========================================================================
  # Resolving an image without a pod
  # ==========================================================================

  # The reason lidar was given a registry at all: a registry-image resource can
  # be answered with one HEAD request, and scheduling a pod to ask the same
  # question costs a container, an image pull and a scheduling round trip every
  # tick. The two branches have to stay apart, and this scenario fails from
  # either direction — the image resource resolved and NOT checked, the
  # ordinary resource checked and NOT resolved. Each absence has the other as
  # its witness, in the same database.
  #
  # The recorded successful check is the third clause and the one with the
  # longest tail: it is what the next tick reads to decide whether the interval
  # has elapsed. A resolve that does not record it re-resolves every tick
  # forever, which is the cost this whole path exists to avoid.
  #
  # Reddened by: dropping `rs.Type() == "registry-image"` from the branch in
  # scanResources — both halves fail at once. Or by deleting the
  # UpdateLastCheckEndTime call, which fails the third clause alone.
  Scenario: An image resource is resolved from the registry while an ordinary one goes to a pod
    Given a lidar scan backed by an image registry
    And the registry holds "my-org/my-image:latest" at the digest "sha256:mixed123"
    And the pipeline has the image resource "native-image" reading "my-org/my-image:latest"
    And the pipeline has the resource "ordinary-resource"
    When the scan runs
    Then the scan completed without error
    And the resource "native-image" resolved to the digest "sha256:mixed123"
    And the resource "native-image" recorded a successful check
    And a check was enqueued for "ordinary-resource"
    And no check was enqueued for "native-image"
    And the resource "ordinary-resource" was left unresolved

  # The deployment switch. Native resolution is only available where the ATC
  # was started with a resolver; everywhere else a registry-image resource is
  # an ordinary resource and has to keep working exactly as it always did. A
  # cluster where this regressed would stop checking every registry-image
  # resource it has the moment the flag was absent.
  #
  # The registry is seeded even though nothing is wired to it, so the resource
  # here is one that COULD have been resolved. That is what makes the second
  # clause a decision rather than an inability.
  #
  # Reddened by: dropping `s.resolver != nil` from the branch in scanResources.
  # The resolver is then a nil interface, the resolve panics into the recover,
  # and no check is enqueued for a resource that should have had one.
  Scenario: Without a resolver an image resource is checked the ordinary way
    Given a lidar scan that was given no image resolver
    And the registry holds "my-org/my-image:latest" at the digest "sha256:never-asked-for"
    And the pipeline has the image resource "native-image" reading "my-org/my-image:latest"
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "native-image"
    And the resource "native-image" was left unresolved

  # Private registries are the normal case in a company, and the credentials
  # come out of the resource's own source. Dropping them means every private
  # image in the cluster stops resolving, which looks like a registry outage
  # and is not one.
  #
  # A wrong password is a separate row on purpose: the registry refuses it, so
  # "the digest landed" is a statement about the credentials that ARRIVED and
  # not merely about a field being read. Resources and resource types both
  # appear because scanner.go carries two copies of the credential-extraction
  # block, and a scenario touching one leaves the other free to drift.
  #
  # The resource and the resource type read DIFFERENT repositories under
  # DIFFERENT logins, and that is load-bearing rather than tidy. Both halves
  # resolve through FindOrCreateResourceConfig, which is keyed on the source,
  # and then through FindOrCreateScope(nil) — the GLOBAL scope, with no
  # resource id — so byte-identical sources put both halves on one scope row,
  # which both then write to and both clauses read from. They were identical
  # here until an audit said so. MEASURED with a temporary step that printed
  # the two scope ids: identical sources gave resource scope 11000001 and type
  # scope 11000001, one row; the distinct sources below give 11000002 and
  # 11000001, two. The distinct digests carry the same weight — sha256:private-app
  # can only have come from the resource's reference and sha256:private-type
  # only from the type's, so each clause now names which image its own half
  # resolved.
  #
  # What the shared row cost, read off the two code paths rather than run,
  # because this migration does not mutate production: resource types are
  # scanned first, so any mutation that left resolveResourceType attaching a
  # scope but writing no version to it — dropping its SaveVersions — was
  # covered a moment later by the resource writing the same digest to the same
  # row, and the type's clause passed on the resource's work.
  #
  # MEASURED on the fixture as it now stands: giving the resource type a
  # password the registry refuses reddens the type's clause alone, with "the
  # resource type \"private-type\" was never attached to a config scope", and
  # the resource's clause stays green.
  #
  # Reddened by: deleting the `if username, ok := source["username"]` block
  # from resolveResource — the resource row fails. From resolveResourceType —
  # the type row fails. Passing the username with an empty password fails the
  # first two rows and leaves the third passing, which is why the third is here.
  Scenario: A private image resolves with the credentials it carries and not without them
    Given a lidar scan backed by an image registry
    And the registry holds "private-registry/app:v2" at the digest "sha256:private-app" behind the login "appuser" and the password "apppass"
    And the registry holds "private-registry/type:v3" at the digest "sha256:private-type" behind the login "typeuser" and the password "typepass"
    And the pipeline has the image resource "private-image" reading "private-registry/app:v2"
    And the resource "private-image" signs in as "appuser" with the password "apppass"
    And the pipeline has the resource type "private-type" reading "private-registry/type:v3"
    And the resource type "private-type" signs in as "typeuser" with the password "typepass"
    And the pipeline has the image resource "wrong-password" reading "private-registry/app:v2"
    And the resource "wrong-password" signs in as "appuser" with the password "not-the-password"
    When the scan runs
    Then the scan completed without error
    And the resource "private-image" resolved to the digest "sha256:private-app"
    And the resource type "private-type" resolved to the digest "sha256:private-type"
    And the resource "wrong-password" was left unresolved

  # The digest on its own is not what a step pulls. What it pulls is the
  # repository and the digest joined back together, and that join is what makes
  # the resolution worth having: a pod that pulls `repo:latest` is pulling
  # whatever moved under it since the scan, while one that pulls
  # `repo@sha256:...` is pulling the thing lidar actually looked at. That is the
  # whole guarantee.
  #
  # Reddened by: ResolvedImage building its reference with ":" instead of "@",
  # or from a repository other than the one in the resource type's source.
  Scenario: A resolved resource type is pulled by digest and not by tag
    Given a lidar scan backed by an image registry
    And the registry holds "my-registry/my-image:latest" at the digest "sha256:abc123"
    And the pipeline has the resource type "my-custom-type" reading "my-registry/my-image:latest"
    When the scan runs
    Then the scan completed without error
    And the resource type "my-custom-type" resolved to the digest "sha256:abc123"
    And the resource type "my-custom-type" will be pulled as "my-registry/my-image@sha256:abc123"

  # Resource types are collected across EVERY pipeline before they are
  # resolved, not per pipeline as resources are. A bug that resolved only the
  # first pipeline's types would be invisible on a single-pipeline test
  # installation and would leave every other team's custom types frozen at
  # whatever image they were first set to.
  #
  # The second type carries no tag, which also puts the empty-tag default
  # through the same scan: a reference written without a tag has to reach the
  # registry as "latest" or a pipeline author's shorthand silently stops
  # resolving.
  #
  # Reddened by: scanResourceTypes collecting from a single map entry rather
  # than ranging over the map — the second pipeline's type never resolves.
  Scenario: Resource types in every pipeline are resolved, not only the first one
    Given a lidar scan backed by an image registry
    And the registry holds "my-registry/my-image:latest" at the digest "sha256:first"
    And the registry holds "other-registry/other-image:latest" at the digest "sha256:second"
    And the pipeline has the resource type "my-custom-type" reading "my-registry/my-image:latest"
    And everything after this is on a second pipeline in another team
    And the pipeline has the resource type "other-type" reading "other-registry/other-image"
    When the scan runs
    Then the scan completed without error
    And the resource type "my-custom-type" resolved to the digest "sha256:first"
    And the resource type "other-type" resolved to the digest "sha256:second"

  # The two reasons a registry-image resource is not resolved, and they are two
  # because each one is a different decision made at a different line. A
  # registry that cannot answer is an outage and the scan has to survive it,
  # leaving the row untouched so the next tick tries again; and
  # `check_every: never` is an instruction.
  #
  # A third row stood here — a source naming no repository — and it was removed
  # because it could not fail for the guard it named. The whole of that finding
  # is recorded as the last DISPOSITION at the foot of this file.
  #
  # Each row is its own database, so the quiet resource cannot vouch for
  # itself: "was left unresolved" is an absence and an absence passes on an
  # empty table. The bystander is a resource the same scan DID resolve, so a
  # fixture that stopped inserting fails on the bystander rather than passing
  # on the vacuum.
  #
  # Reddened by, row for row: dropping the `err != nil` return after Resolve,
  # which writes an empty digest into the version; and dropping the
  # `CheckEvery().Never` guard from resolveResource.
  Scenario Outline: A registry-image resource is not resolved when <case>
    Given a lidar scan backed by an image registry
    And the registry holds "quiet/app:latest" at the digest "sha256:quiet"
    And the registry holds "bystander/app:latest" at the digest "sha256:bystander"
    And the pipeline has the image resource "quiet-image" reading "quiet/app:latest"
    And the pipeline has the image resource "bystander-image" reading "bystander/app:latest"
    And <state>
    When the scan runs
    Then the scan completed without error
    And the resource "quiet-image" was left unresolved
    And the resource "bystander-image" resolved to the digest "sha256:bystander"

    Examples:
      | case                                  | state                                                        |
      | the registry cannot answer for it     | the resource "quiet-image" reads "missing/app:latest" instead |
      | it is configured to be checked never  | the resource "quiet-image" is checked never                  |

  # The same two reasons for a resource type, plus the one only a type has —
  # and the same third row removed for the same reason, recorded at the foot.
  # These are two outlines rather than one because scanner.go carries
  # resolveResource and resolveResourceType as two copies of the same function:
  # a change to either is a change to only one of them, and a single outline
  # sweeping both would still be reporting on whichever copy it happened to
  # exercise.
  #
  # The last row is the one with no counterpart. A resource type that names
  # its image outright has already answered the question a registry would be
  # asked, and resolving it anyway would overwrite an operator's explicit
  # pin with whatever the tag points at today.
  #
  # Reddened by: the same two guards in resolveResourceType, and for the last
  # row by dropping the `rt.Image() != ""` skip.
  Scenario Outline: A resource type is not resolved when <case>
    Given a lidar scan backed by an image registry
    And the registry holds "quiet/type:latest" at the digest "sha256:quiet"
    And the registry holds "bystander/type:latest" at the digest "sha256:bystander"
    And the pipeline has the resource type "quiet-type" reading "quiet/type:latest"
    And the pipeline has the resource type "bystander-type" reading "bystander/type:latest"
    And <state>
    When the scan runs
    Then the scan completed without error
    And the resource type "quiet-type" was left unresolved
    And the resource type "bystander-type" resolved to the digest "sha256:bystander"

    Examples:
      | case                                  | state                                                            |
      | the registry cannot answer for it     | the resource type "quiet-type" reads "missing/type:latest" instead |
      | it is configured to be checked never  | the resource type "quiet-type" is checked never                  |
      | its image is already named in full    | the resource type "quiet-type" names its image directly as "pinned-image:sha256" |

  # The interval is what stops the tick becoming a load test on somebody's
  # registry. It is asserted here by letting the registry MOVE between the two
  # scans: if the second scan had asked, it would have got a different answer,
  # and the row would say so. Nothing about this scenario has to look at a
  # clock or a call record — the digest that stayed put is the evidence.
  #
  # Only a resource TYPE, and the missing half is a defect this migration
  # found. See DEFECT at the foot of this file: the identical guard in
  # resolveResource cannot fire in production, because a natively resolved
  # resource never gets the last_check_build_id that Resource.LastCheckEndTime
  # requires before it will report a time at all. Writing the resource half of
  # this scenario would mean writing the ginkgo suite's fixture, which hands
  # the resource a check build production never gives it — a test that passes
  # on a state the system cannot reach.
  #
  # Reddened by: deleting the
  # `time.Now().Before(rt.LastCheckEndTime().Add(interval))` guard from
  # resolveResourceType, or by ignoring the declared check_every and using the
  # default interval of zero — the second scan then overwrites the digest.
  Scenario: A resource type resolved inside its interval is not resolved again
    Given a lidar scan backed by an image registry
    And the registry holds "app/type:latest" at the digest "sha256:type-first"
    And the pipeline has the resource type "app-type" reading "app/type:latest"
    And the resource type "app-type" is checked every "1h"
    When the scan runs
    And the registry now holds "app/type:latest" at the digest "sha256:type-second"
    And the scan runs again
    Then the scan completed without error
    And the resource type "app-type" resolved to the digest "sha256:type-first"

  # ==========================================================================
  # A collector that got there first
  # ==========================================================================

  # These two are the reason scanner.go has foreign-key guards at all. The
  # garbage collector removes resource config scopes that nothing points at,
  # and a scan that is halfway through using one loses it under its feet. It
  # is a race, it is expected, and the only correct response is to leave
  # everything alone and let the next tick do it properly.
  #
  # What must NOT happen is the failed pass recording that it checked. The
  # check end time is what the next tick reads, and a pass that advanced it
  # after failing would put the subject to sleep for a whole interval having
  # resolved nothing. That is why the assertion is not "the pass reported an
  # error" but "the pass after it worked", and why the resource type below
  # declares a check_every: without one the interval is zero and a stamped
  # clock would be indistinguishable from an unstamped one, so the scenario
  # would pass on the very mutation it exists to catch. MEASURED — the first
  # version of this scenario had no check_every, and hoisting
  # UpdateLastCheckEndTime above SaveVersions in BOTH copies left all 25
  # scenarios green.
  #
  # The scope is really deleted here, so the error is PostgreSQL's own 23503
  # on resource_config_versions' foreign key. Both a resource and a resource
  # type are in flight, because the two copies of the save path fail at the
  # same line and either could be fixed alone. The ordinary resource is the
  # sibling Rule 2 asks for: "holds no resolved digest" is an absence, and the
  # check enqueued beside it is what shows the pass ran and carried on rather
  # than dying at the first violation.
  #
  # Reddened by: dropping the `return` after the save error, so the pass falls
  # through to the stamp, the success log and the metric. MEASURED — the
  # counter is what catches it: a resolve that failed must not be counted as a
  # check enqueued, or an operator watching throughput sees a cluster resolving
  # images it is not resolving. Two earlier attempts at a redden for this
  # scenario did NOT work and are recorded so nobody retries them: hoisting
  # UpdateLastCheckEndTime above SaveVersions changes nothing, because the row
  # the stamp lands on is the row the collector deleted, and a stale clock
  # cannot survive on a row that is gone; and the DEBUG-versus-ERROR
  # classification of the violation changes no control flow at all.
  Scenario: A scope collected before the version is saved leaves the next scan free to retry
    Given a lidar scan backed by an image registry
    And the registry holds "retry/app:latest" at the digest "sha256:resource"
    And the registry holds "retry/type:latest" at the digest "sha256:type"
    And the pipeline has the image resource "retried-image" reading "retry/app:latest"
    And the pipeline has the resource type "retried-type" reading "retry/type:latest"
    And the resource type "retried-type" is checked every "1h"
    And the pipeline has the resource "unaffected-resource"
    And the garbage collector deletes the scope before the scan can save the version
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "unaffected-resource"
    And the checks enqueued counter went up by 1
    And the resource "retried-image" holds no resolved digest
    And the resource type "retried-type" holds no resolved digest
    When the scan runs again
    Then the scan completed without error
    And the resource "retried-image" resolved to the digest "sha256:resource"
    And the resource type "retried-type" resolved to the digest "sha256:type"

  # The earlier moment, and a different constraint: here the scope is gone
  # before the resource can even be pointed at it, so the violation is on
  # resources.resource_config_scope_id rather than on the versions table.
  #
  # Only a resource can reach this. A resource TYPE is attached by setting
  # resource_config_id alone — it has no scope column — so deleting the scope
  # cannot fail its attachment at all, and the guard scanner.go carries in
  # resolveResourceType for this moment is answering a race that its own schema
  # cannot produce. Worth knowing, and the reason this scenario has no resource
  # type in it while the one above has both.
  #
  # Reddened by: dropping the `return` after the attachment error, which lets
  # a resource whose scope was collected out from under it be counted, logged
  # and stamped as though it had resolved. The counter below is what says so —
  # one check was enqueued in this pass, for the ordinary resource, and the
  # image resource contributed nothing because nothing about it was resolved.
  #
  # The ordinary resource is also the sibling Rule 2 asks for: "holds no
  # resolved digest" is an absence, and a pass that died at the first violation
  # would fail on the check beside it rather than passing on the vacuum.
  Scenario: A scope collected before it can be attached leaves the next scan free to retry
    Given a lidar scan backed by an image registry
    And the registry holds "attach/app:latest" at the digest "sha256:attached"
    And the pipeline has the image resource "attached-image" reading "attach/app:latest"
    And the pipeline has the resource "unaffected-resource"
    And the garbage collector deletes the scope before the scan can attach it
    When the scan runs
    Then the scan completed without error
    And a check was enqueued for "unaffected-resource"
    And the checks enqueued counter went up by 1
    And the resource "attached-image" holds no resolved digest
    When the scan runs again
    Then the scan completed without error
    And the resource "attached-image" resolved to the digest "sha256:attached"

  # ==========================================================================
  # The number on the dashboard
  # ==========================================================================

  # ChecksEnqueued is the counter an operator watches to know the cluster is
  # still checking anything at all, and it is emitted once per tick. It counts
  # checks CREATED, not checks attempted, and the difference is the whole
  # value: a check that was skipped because the previous one is still running
  # is not throughput, and counting it turns a cluster whose checks are all
  # stuck into a cluster whose dashboard looks busy.
  #
  # The in-flight resource is a real unfinished check build, which is what a
  # check running longer than the scan interval actually is. Both resources are
  # in one scan so the counter has to discriminate rather than merely count.
  #
  # Reddened by: hoisting `metric.Metrics.ChecksEnqueued.Inc()` out of the
  # `else` in scanner.go's check(), so it fires for the skipped one too.
  Scenario: The checks-enqueued counter counts checks created, not checks skipped
    Given a lidar scan that was given no image resolver
    And the pipeline has the resource "fresh-resource"
    And the pipeline has the resource "already-checking"
    And a check for the resource "already-checking" is already in flight
    When the scan runs
    Then the scan completed without error
    And the checks enqueued counter went up by 1
    And a check was enqueued for "fresh-resource"
    And no check was enqueued for "already-checking"

  # ==========================================================================
  # What did not come across, and why
  # ==========================================================================
  #
  # DISPOSITION — the DEBUG-versus-ERROR log level on the two foreign-key
  # guards. scanner_test.go asserts, four times, that a collected scope is
  # logged at DEBUG and not at ERROR. Read the code and the guard changes
  # nothing else: both arms log and return, the row is untouched either way,
  # and the next tick behaves identically. It is a pure log-level assertion,
  # and it does have a consequence — an expected race logged as an error every
  # time the collector runs is what teaches an operator to ignore errors — but
  # there is no outcome to assert and no vocabulary in this corpus for
  # asserting a log line. It stays in Go. The scenarios above take the half
  # that IS an outcome: that the failed pass left the clock alone.
  #
  # DISPOSITION — "does not schedule a check for an already-cancelled empty
  # enumeration". An empty database and a cancelled context, asserting that
  # nothing happened. Both halves are vacuous on their own and there is no
  # sibling that could make them otherwise: with no resources there is nothing
  # a cancelled scan could have checked, and with a cancelled context there is
  # nothing an empty scan could have checked. The cancellation paths in
  # scanResources are real, but nothing observable distinguishes them from the
  # scan simply having no work.
  #
  # DISPOSITION — "reads persisted pipeline state through a separately
  # constructed factory". That spec tests the ginkgo fixture, not lidar. Its
  # brine equivalent is the jetbridge-db resource, which every scenario in this
  # file uses.
  #
  # DISPOSITION — the exact resource_config row identity
  # (`freshType.ResourceConfigID()` equalling a config the test looked up
  # separately). The observable consequence of getting it wrong is that the
  # digest lands somewhere the resource does not read, and every "resolved to
  # the digest" assertion above reads the version THROUGH the resource's own
  # config and scope. Asserting the id as well would pin a surrogate key.
  #
  # DISPOSITION — "a registry-image resource whose source names no repository
  # is not resolved". This was a row of each of the two outlines above, and
  # both rows are gone, because neither could redden for the guard its case
  # name pointed at. The guard is
  #
  #     repository, _ := source["repository"].(string)
  #     if repository == "" { logger.Error("missing-repository-in-source", nil); return }
  #
  # in resolveResource and again in resolveResourceType, and it is unreachable
  # by outcome: the resolver it protects opens with the SAME refusal.
  # atc/imageresolver/resolver.go's Resolve begins
  # `if repository == "" { return "", fmt.Errorf("empty repository") }`, and
  # atc/imageresolver/resolver_test.go's TestResolver_EmptyRepository is where
  # that is pinned — one layer down, where it belongs. Delete lidar's guard and
  # control reaches Resolve, which refuses on its first line, and the
  # `if err != nil { ... return }` two lines below returns from the same
  # function at the same point; the tag read and the auth struct in between are
  # pure local reads with no effect. Both paths log at ERROR and change no row.
  # The scenario is green either way, and so is a cluster.
  #
  # The double is not what stood in the way, though it did hide it. The
  # registry in steps/resource_checking.go mirrors that same first line, so it
  # refuses the empty repository too — which is what an audit measured when it
  # dropped the production guard and watched the row stay green. But teaching
  # the double to ANSWER for the empty repository would not have rescued the
  # row. It would have made it green on a state no registry can produce.
  # Production refuses twice over: imageresolver on its first line, and
  # go-containerregistry behind it — `name.ParseReference(":latest")` takes the
  # NewTag branch, which reads the base as "" and hands it to NewRepository,
  # which answers "a repository name must be specified" (name/tag.go and
  # name/repository.go, v0.21.2), so ParseReference ends at "could not parse
  # reference: :latest". A scenario written over a registry that answered
  # anyway would be green on a state the system cannot reach, which is the
  # trap the DEFECT below describes, walked into deliberately.
  #
  # What the removed rows still carried that was real — that a resolve the
  # registry refuses does not cost the resource behind it — is exactly the
  # "the registry cannot answer for it" row of the same outline, which reaches
  # the same `return` from the line below. Nothing was lost with them.
  #
  # Read, not measured, on the production half: confirming it directly would
  # mean mutating scanner.go, which this migration does not do. What was run is
  # `go test ./atc/imageresolver/ -run TestResolver_EmptyRepository`, which
  # passes, and which is the whole of the behaviour lidar's guard duplicates.

  # ==========================================================================
  # DEFECT found while writing this file
  # ==========================================================================
  #
  # resolveResource's check interval never fires for a natively resolved
  # resource. The guard reads
  #
  #     if interval > 0 && !rs.LastCheckEndTime().IsZero() && ...
  #
  # and Resource.LastCheckEndTime() is only ever populated by scanResource when
  # `last_check_build_id` or `in_memory_build_id` is non-null on the row — see
  # buildData.populate in atc/db/resource.go, where the end time is assigned
  # only inside useLastCheckBuild. Native resolution creates no check build of
  # any kind: resolveResource calls UpdateLastCheckEndTime on the scope and
  # nothing else, so last_check_build_id stays null and the resource reports
  # the zero time on every subsequent tick, whatever its check_every says.
  #
  # The consequence: every registry-image resource in a cluster with native
  # resolution enabled is re-resolved on EVERY lidar tick — one registry
  # request per image resource per tick, forever, which is the exact cost the
  # interval exists to bound. An operator who sets `check_every: 24h` on an
  # image resource gets no effect at all.
  #
  # Resource types are not affected. resourceTypesQuery reads
  # `ro.last_check_end_time` straight out of the joined scope with no build
  # involved, so resolveResourceType's guard works — which is why the scenario
  # above is written for a type and not for a resource.
  #
  # MEASURED, not reasoned about: the scenario above was first written with
  # both a resource and a type, the registry was moved between the two scans,
  # and the type kept `sha256:type-first` while the resource came back as
  # `sha256:second`. The lidar log for the second pass shows
  # `resolve-resource-type.skip-interval-not-elapsed` and no corresponding
  # skip for the resource.
  #
  # scanner_test.go's "skips a persisted native resource whose nonzero interval
  # has not elapsed" passes because its fixture calls resource.CreateBuild and
  # then UpdateLastCheckStartTime(checkBuild.ID(), ...) by hand, which sets the
  # build id the production path never sets. It is green on a state production
  # cannot produce, which is why nobody has seen this.
  #
  # NOT FIXED HERE — this migration does not touch production.
