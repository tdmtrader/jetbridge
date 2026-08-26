Feature: Closing the loop — a whole step, the durable tier, and the artifact index

  Three suites close here.

  `integration_test.go` drove whole workflows through a worker whose executor
  was a recording spy, and then asserted the SHAPE OF A STRING that nothing
  ever ran — seventeen sites, nine of them `expectSupervisedExec(...)`. The
  worker below runs the command. Every assertion is on what came back: the
  build log, the exit status, the row the next web reads, the pods on the
  cluster.

  `storage_daemonset_durable_test.go` asserted `d.restores.Load() == 0` to
  mean "it did not warm", and `got.DurableKey == durableKey` to mean "the key
  survived the trip". Neither is observable by anyone. What a get step
  experiences is whether the bytes arrive; what an operator experiences is the
  four warm counters, which are production output. Both are here, and the
  restore counter is not.

  `artifact_locator_test.go` needed no re-thinking: the seam is a data
  structure and the tests already asserted its answers.

  Source: coverage_matrix.md Addendum 2 — "replace the recording double with a
  working one, and assert the round trip".

  # ==========================================================================
  # A step from end to end
  # ==========================================================================

  # The last clause of "runs a task step end-to-end: create container → run →
  # wait → exit". The exit status on the container row is not decoration: it is
  # what a web that restarted after the command finished reads to decide the
  # build already succeeded. The pod's worker label is what `kubectl get pods
  # -l` and the reaper select on.
  Scenario: A finished step leaves its exit status where a restarted web will find it
    Given a jetbridge worker driving a whole step from end to end
    When the step "task-abc123" runs "echo hello world" and finishes
    Then the finished step reported exit 0
    And the step's output reached the build log as "hello world"
    And the finished step left exit status "0" on its container
    And the step's pod is labelled for the worker that owns it

  # The reattach case. Web 1 finished the command but died before recording
  # the status, so the pod survives with no completion annotation. Two things
  # must then be true, and the ginkgo case proved the second by comparing two
  # recorded command slices for byte equality — which cannot distinguish
  # "re-execed on the surviving pod" from "re-execed on a brand new one".
  # Counting the pods can.
  Scenario: A web that restarted mid-step reuses the pod instead of scheduling a second one
    Given a jetbridge worker driving a whole step from end to end
    When the step "task-reattach" runs "echo make release" and finishes
    And the web dies before the exit status is recorded and a new web takes over
    And the new web runs the same step again
    Then attaching was refused saying "no completion status"
    And the cluster is running exactly 1 pod for the step
    And the finished step reported exit 0

  # "detects pod eviction in a resource get step". The claim worth keeping is
  # not the diagnostics — pod-lifecycle.feature and failure-priority.feature
  # already have those — it is that an eviction is a TYPED, RETRYABLE
  # interruption rather than a plain error. That is a different build
  # classification, and no feature file said so before this one.
  Scenario: An evicted step is a retryable interruption, not a failed build
    Given a jetbridge worker driving a whole step from end to end
    When the node evicts the step "get-evicted" before its command runs
    Then the step was interrupted rather than failed, because it was "evicted"
    And the diagnostics in the build log explain "Pod Failure Diagnostics"
    And the diagnostics in the build log explain "Evicted"

  # DISPOSITION — "handles task failure with non-zero exit code" is
  # task-command.feature's "A failing command's exit code reaches the
  # consumer", which additionally proves the exit code came from a command
  # that really ran rather than from a preset field on a fake. Not duplicated.

  # DISPOSITION — the pause-pod clauses of the end-to-end case (image
  # "ubuntu:22.04", command `sh -c "trap 'exit 0' TERM; sleep 86400 & wait"`)
  # are container-run.feature's "With an exec transport the pod is a
  # placeholder the step runs inside" and "the pod is a placeholder, not the
  # step's command", stated as behavior rather than as a literal argv.

  # DISPOSITION — "runs a get step followed by a put step with the resource
  # protocol" is step-integration.feature's "A get step's request reaches its
  # resource and the answer comes back" plus "A put step's inputs are mounted
  # where its resource expects them". Its one unique assertion,
  # `io.ReadAll(fakeExecutor.execCalls[0].stdin) == getStdin`, is Addendum 2's
  # streaming class: the round trip already covers it, because the answer that
  # comes back is the answer to the request that went in.

  # DISPOSITION, WITH A FINDING — "returns an error when the context is
  # cancelled during exec-mode task" and its resource-step twin never cancel a
  # context. They set `fakeExecutor.execErr = context.Canceled` and assert the
  # error surfaces and the pod survives. That is an exec FAILURE, not a
  # cancellation, and the two paths behave differently: a genuinely cancelled
  # step deletes its pod (pod-lifecycle.feature, "A cancelled build takes its
  # pod with it") while a failed one keeps it (container-run.feature, "A failed
  # step's pod is kept for the operator"). Both real behaviors are already
  # covered under their true names. Migrating these two would have imported a
  # mislabelled test into the new format.

  # DISPOSITION — "mounts input volumes from a get step and output volumes for
  # a task" and "passes inputs from a get step to a put step via volume
  # mounts" are container-pod.feature's "A step sees its working directory and
  # every input" and its output/scratch siblings.

  # DISPOSITION — "creates a pod with sidecars that share volume mounts" and
  # "runs a task with multiple sidecars" are container-pod.feature's five
  # sidecar scenarios, which cover env, ports, shared mounts, working
  # directory and multiplicity.

  # DISPOSITION — "LookupVolume returns DaemonSetVolume" asserts a Go type.
  # worker.feature's standing rule is that a volume is never asserted to be of
  # a particular type; its behavioral content — a persisted volume is found by
  # its handle and still carries its row — is step-integration.feature's "A
  # looked-up artifact still carries its database row".

  # DISPOSITION — "detects ImagePullBackOff" and "detects CrashLoopBackOff"
  # are failure-priority.feature's "An image pull failure is reported ahead of
  # the exit code" and "A terminal waiting state fails the pod immediately",
  # plus pod-lifecycle.feature's diagnostics scenarios.

  # ==========================================================================
  # The durable resource-cache tier
  # ==========================================================================

  # A cache already on a node must never trigger a warm: going to object
  # storage for something already on disk is the whole cost of the tier with
  # none of its benefit.
  #
  # The store is EMPTY in this scenario. That is the discriminator, and it is
  # stronger than the restore counter it replaces: an implementation that
  # warmed before probing would find nothing in the store and report a miss,
  # so "found" cannot be reached by warming.
  #
  # This is also the "a ready endpoint is still used" case — the endpoint the
  # cluster publishes carries an explicit ready condition — so the filter
  # below cannot pass by excluding everything.
  Scenario: A cache already on the node is served without touching the store
    Given an artifact daemon with a durable store behind it
    And the node already holds the resource cache "rc-42" containing "already on this node"
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content"
    Then the resource cache is found
    And the cached artifact reads "already on this node"
    And the warm counters read 1 local, 0 hit, 0 miss, 0 suppressed

  # Silence is the protocol: no content key means the ATC is not offering this
  # cache to the durable tier, so no request may be made at all. The store
  # holds the object here, so any request that did go out would be visible in
  # the counters.
  Scenario: A cache with no content key is never offered to the durable store
    Given an artifact daemon with a durable store behind it
    And the durable store holds "resource-caches/rc-content" containing "in the bucket"
    When a get step looks up the resource cache "rc-42" offering no content key
    Then the resource cache is not found
    And the operator sees no warm activity at all

  # A daemon that predates the tier never advertises it, and the ATC must then
  # issue zero requests to a route that does not exist — otherwise every cache
  # miss during a rolling upgrade costs an extra failed round trip.
  #
  # The store holds the object, so asking would SUCCEED. "Not found" is
  # therefore the strongest available statement that nothing was asked.
  Scenario: A daemon that predates the durable tier is never asked to warm
    Given an artifact daemon with a durable store behind it
    And the daemon predates the durable tier
    And the durable store holds "resource-caches/rc-content" containing "in the bucket"
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content"
    Then the resource cache is not found
    And the operator sees no warm activity at all

  # The payoff path: nothing local, a content key, a capable daemon. The
  # ginkgo case asserted `restores == 1`; what a get step experiences is the
  # bytes.
  Scenario: A cache that is only in durable storage is restored and its bytes reach the build
    Given an artifact daemon with a durable store behind it
    And the durable store holds "resource-caches/rc-content" containing "restored from the bucket"
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content"
    Then the resource cache is found
    And the cached artifact reads "restored from the bucket"
    And the warm counters read 0 local, 1 hit, 0 miss, 0 suppressed

  # A failed warm must not be retried on every scheduler tick. attemptGet
  # re-enters every GetResourceLockInterval while waiting for the resource
  # lock, and a get step's own timeout does not bound the warm, so an
  # unreachable bucket would otherwise cost a full warm timeout every few
  # seconds indefinitely.
  #
  # One miss then three suppressions is the split that tells an operator a
  # degraded bucket is being CONTAINED rather than retried into the ground.
  # Without suppression the same five lookups read 0/0/5/0.
  Scenario: A failed warm is not retried on every scheduler tick
    Given an artifact daemon with a durable store behind it
    And the durable store cannot be reached
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content" 5 times over
    Then the resource cache is not found
    And the warm counters read 0 local, 0 hit, 1 miss, 4 suppressed

  # The producing half. The ginkgo case asserted the daemon received the same
  # `durable_key` string it was handed; what that string is FOR is that the
  # object can be found again by content on a node that never had it.
  Scenario: An artifact filed under a content key can be restored on a node that never had it
    Given an artifact daemon with a durable store behind it
    And the node already holds the resource cache "rc-42" containing "produced by an earlier build"
    When the ATC registers the resource cache "rc-42" under content key "resource-caches/rc-abc"
    And the node's own copy of "rc-42" is reclaimed
    And a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-abc"
    Then the resource cache is found
    And the cached artifact reads "produced by an earlier build"

  # ...and the defect the content key exists to prevent. A cache with no
  # content key is addressed by "rc-42", a Postgres row id. Filing THAT in
  # permanent storage would mean a later build's unrelated row 42 restores
  # this build's bytes. The lookup below offers the row id as the content key
  # on purpose: if registration filed the object under it, this would be a hit.
  Scenario: An artifact with no content key is never filed under its row id
    Given an artifact daemon with a durable store behind it
    And the node already holds the resource cache "rc-42" containing "produced by an earlier build"
    When the ATC registers the resource cache "rc-42" offering no content key
    And the node's own copy of "rc-42" is reclaimed
    And a get step looks up the resource cache "rc-42" offering content key "rc-42"
    Then the resource cache is not found

  # An endpoint the API has marked not-ready is a pod that is terminating or
  # failing its probe. Binding an artifact read to one is a read against a pod
  # that is going away. EndpointSlice reports these alongside ready ones, and
  # discovery previously flattened every address regardless.
  #
  # The pod HOLDS the cache and would answer 200. Not-found is only reachable
  # by never asking it.
  Scenario: A daemon pod the API marked not ready is not a cache hit
    Given an artifact daemon with a durable store behind it
    And the node already holds the resource cache "rc-42" containing "on a dying pod"
    And the API has marked the daemon pod not ready
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content"
    Then the resource cache is not found

  # A volume bound from a probe or a warm must carry the daemon client, or
  # peer fallback is dead: an alias swept between the probe and the read
  # becomes a bare 404 and a red build while the bytes sit on another node
  # that was never asked.
  #
  # The ginkgo case asserted `dsv.daemonClient != nil`, an unexported field on
  # a struct — unreachable from outside the package and, more to the point,
  # not something a build can experience. What a build experiences is below.
  # The node's copy and the peer's copy hold DIFFERENT text, so the assertion
  # names which one served the read.
  Scenario: A cache hit whose alias vanishes before the read still gets its bytes
    Given an artifact daemon with a durable store behind it
    And the node already holds the resource cache "rc-42" containing "swept away mid-read"
    And a peer still holds a mirrored copy of "rc-42" containing "served by the peer"
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content", and the node's alias vanishes before the bytes are read
    Then the resource cache is found
    And the cached artifact reads "served by the peer"

  Scenario: A warmed cache whose alias vanishes before the read still gets its bytes
    Given an artifact daemon with a durable store behind it
    And the durable store holds "resource-caches/rc-content" containing "swept away mid-read"
    And a peer still holds a mirrored copy of "rc-42" containing "served by the peer"
    When a get step looks up the resource cache "rc-42" offering content key "resource-caches/rc-content", and the node's alias vanishes before the bytes are read
    Then the resource cache is found
    And the cached artifact reads "served by the peer"

  # DISPOSITION — "the four counters partition every lookup exactly once" is
  # not a scenario of its own. Its three table rows are the local-hit, warm-hit
  # and miss-then-suppressed scenarios above, and the partition itself — the
  # four counters summing to the number of lookups — is asserted by the
  # counter step every time it runs. Stating it a fourth time would restate
  # the same three lookups.

  # ==========================================================================
  # The artifact index
  # ==========================================================================

  Scenario: An artifact is found on the node that recorded it
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1"
    And the artifact "def" is recorded on node "node-2"
    Then the artifact "abc" is held on node "node-1"
    And the artifact "def" is held on node "node-2"

  # The directory is the daemon key the next step's init container fetches
  # from, so losing it is as bad as losing the node.
  Scenario: An artifact remembers the directory it was stored in
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1" in directory "container-abc/output"
    Then the artifact "abc" is held on node "node-1"
    And the artifact "abc" is stored in directory "container-abc/output"

  Scenario: An artifact nobody recorded is not held anywhere
    Given an empty artifact index
    Then the artifact "nonexistent" is not held anywhere

  Scenario: A collected artifact stops resolving
    Given an empty artifact index
    When the artifact "abc" is recorded on node "node-1"
    And the artifact "abc" is collected
    Then the artifact "abc" is not held anywhere

  # The ginkgo case spawned 300 goroutines over 26 COLLIDING keys and asserted
  # nothing at all — it was a race-detector probe, and this repository's unit
  # tier does not run with -race (CLAUDE.md: "do not use --race"), so it
  # proved nothing there either. Distinct keys turn it into a claim that can
  # fail: nothing recorded concurrently may be lost.
  Scenario: Records made at the same moment are all kept
    Given an empty artifact index
    When 100 artifacts are recorded at the same moment
    Then every artifact that was recorded is still held
