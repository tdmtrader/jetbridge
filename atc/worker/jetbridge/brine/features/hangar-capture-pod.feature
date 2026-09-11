Feature: What a capture-selected task's Pod says

  Everything here is readable off the Pod the cluster hands back, which is the
  half of this track brine is the right runner for. What a real kubelet DOES
  with that Pod is a K3s flow under a build tag and stays in Go, and so is
  every crash half around a start or a finish.

  The whole file needs the control scenario in it. An unchanged-pod assertion
  passes on a worker that builds no capture pods at all, so it is only
  meaningful beside them — which is why the ordinary pod is asserted here
  rather than in a file of its own, and why Phase 9 cites it as the AC 20
  regression twin instead of writing a second copy.

  One scenario in this family is still in ../pending/: the strict-input fetch,
  which belongs to Phase 9. The scheduling block below arrived in Phase 8, which
  is where a worker can be told which facets its cohort serves.

  @HOP-1 @HOP-3 @HOP-12
  Scenario: Selecting capture for a declared output puts the hold init container before every writer
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it takes an input at "/tmp/build/src"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture pod carries 2 init containers
    And the capture control init runs before every writer

  # The control the whole file needs.
  #
  # The worker has the output plane ON, and that is the whole point of the
  # scenario: the claim is that turning the plane on changes nothing for a step
  # that captures nothing, and a control built on a plane-OFF worker cannot say
  # it. With the plane off, the mutation that emits the capture init whenever
  # the plane is on and nothing is captured left this 6/6 green while its Go
  # twin (TestTheOutputPlaneChangesNoOrdinaryPodWhenNothingIsCaptured) reddened.
  #
  # Reddened by: Container.buildPod adding the capture control init when
  # DurableOutputCapture is nil, and equally by adding it whenever the output
  # plane is enabled.
  @HOP-1 @HOP-59
  Scenario: A task whose output is not selected for capture builds the pod it builds today
    Given a jetbridge worker with an artifact store and the output plane on
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    When the container runs
    Then the pod has 3 volumes
    And the pod carries no output-plane container
    And every mount in the pod names exactly one of its volumes
    And the step sees a volume mounted at "/tmp/build/result"

  # Convention 5: "no credential" passes on a worker that ignores its
  # configuration entirely, so the PRESENCE half is the line above it, in the
  # same scenario, and it is asserted first.
  #
  # TWO mutations, because the presence line carries "the only" and is therefore
  # itself an exclusivity claim. brine stops at the first red step, so a
  # mutation that puts the grant in a second container reddens the PRESENCE line
  # and the absence line below it is never evaluated. The plan's single
  # `Reddened by:` was off by one line for exactly that reason.
  #
  # Reddened by: buildPod copying the source-control grant env into the main
  # container — the PRESENCE line reddens ("the source-control grant is carried
  # by [hangar-capture-control main]").
  #
  # Reddened by, for the absence line's own vector: buildPod copying the grant
  # into the main container under the LEGACY name HANGAR_CAPTURE_CAPABILITY —
  # the presence line stays green, because the grant's own variable is still in
  # exactly one container, and the absence line reddens. Its point is why that
  # check scans a SET of names: a rename that moved the credential to a new
  # variable would sail past a scan that only knew the old one.
  @HOP-24
  Scenario: The capture control init is the only container that carries the source-control grant
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture control init is the only container carrying the source-control grant
    And the task's containers carry no Hangar credential

  @HOP-1
  Scenario: Exactly one declared output is selected, and a second selection is refused
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And it produces an output at "/tmp/build/report"
    And its output "result" is captured when the step succeeds
    And a second output "report" is also selected for capture
    When the capture pod is built
    Then the pod build is refused saying "exactly one declared output"

  @HOP-3 @HOP-58
  Scenario: A capture pod carries the base control handshake and the exact Downward API pod and node fields
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture pod carries the base control handshake and the Downward API pod and node fields

  # A COUNT, not membership. The migration's own GAP rows warn that brine's
  # mount steps are membership by default, and "every mount resolves to a
  # declared Volume" is only a real claim when the number is pinned too.
  @HOP-12 @HOP-20
  Scenario: A capture pod's mounts all resolve to a declared Volume
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it takes an input at "/tmp/build/src"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture pod declares 5 mounts, and every one resolves to a declared Volume
    And the captured output is mounted at the incarnation the daemon reserved

  # ----------------------------------------------------------------------
  # The scheduling block. A ready label is a HINT and never authority: the
  # authenticated handshake and the activation epoch row are. What a label buys
  # is that the pod does not land somewhere the hold could never be
  # acknowledged.
  # ----------------------------------------------------------------------

  # Reddened by: BuildAffinity emitting only concourse.dev/hangar-output-v1 and
  # dropping concourse.dev/hangar-execution-control-v1 — this reddens on the
  # SECOND line (the count falls to 1) while the ordinary-pod control above
  # stays green.
  #
  # The node line is the Phase 4 round-2 ruling 3: a reservation is issued by
  # ONE daemon and the directory it named exists on ONE node, so a capture pod
  # that lands anywhere else mounts an empty hostPath.
  #
  # Reddened by, for the node line's own vector: BuildAffinity dropping the
  # kubernetes.io/hostname expression — the two label lines above it stay green.
  @HOP-58
  Scenario: A capture pod requires both ready labels, and neither alone admits it
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    And the worker's cohort is ready for "concourse.dev/hangar-output-v1"
    When the capture pod is built
    Then the capture pod requires 2 ready labels
    And the capture pod is admitted only by a node carrying "concourse.dev/hangar-execution-control-v1"
    And the capture pod is admitted only by the node "hangar-node-a"

  # An absence with its control in the same scenario (convention 5): "no capture
  # pod" passes on a worker that builds no pods at all, so the refusal's own
  # text is asserted first and an ORDINARY pod off the same worker last.
  #
  # The refusal is at ADMISSION and not an omission in the Pod. A worker that
  # quietly built the ordinary pod would produce a step that ran, succeeded and
  # captured nothing, leaving the predeclared handoff unresolved until its
  # deadline — and there is no cache-tier fallback to degrade into.
  #
  # Reddened by: Container.buildPod dropping the OutputPlaneEnabled arm, so a
  # base-only cohort builds the capture pod anyway — the refusal line reddens
  # and the ordinary-pod control stays green.
  @HOP-58 @HOP-59
  Scenario: A worker whose output facet is not enabled builds no capture pod, while the base-only cohort still builds an ordinary one
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    And the worker's cohort is ready for "concourse.dev/hangar-execution-control-v1"
    When the capture pod is built
    Then the pod build is refused saying "output facet is not enabled"
    And no capture pod is built
    And the same worker still builds an ordinary pod for a step that captures nothing

  # A node can carry the output label while its daemons speak for another
  # activation epoch — a rolling upgrade, a half-finished rotation, a node back
  # from a long drain. A capture admitted against that cohort would be signed by
  # a key this control plane does not pin, so the label alone admits nothing.
  #
  # Control LAST here rather than first, because brine stops at the first red
  # step: the refusal's text is what distinguishes "refused for the epoch" from
  # "refused for anything", and the matching-epoch control is what distinguishes
  # it from "this worker admits nothing at all".
  #
  # Reddened by: Container.buildPod dropping the activation-epoch arm — the
  # refusal line reddens and the matching-epoch control stays green.
  @HOP-58
  Scenario: A ready label without a matching handshake admits nothing, while the handshaken cohort admits
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    And the worker's cohort is ready for "concourse.dev/hangar-output-v1"
    And the daemon cohort has not handshaked
    When the capture pod is built
    Then the pod build is refused saying "speaks for epoch"
    And no capture pod is built
    And the same worker admits a capture whose epoch matches its cohort
