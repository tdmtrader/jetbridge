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

  Three scenarios in this family are still in ../pending/: the scheduling block
  (both ready labels, the un-enabled facet, the handshake) belongs to Phase 8,
  which is where a worker can be told its facet is off, and the strict-input
  fetch scenario to Phase 9.

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
  # Reddened by: Container.buildPod adding the capture control init when
  # DurableOutputCapture is nil.
  @HOP-1 @HOP-59
  Scenario: A task whose output is not selected for capture builds the pod it builds today
    Given a jetbridge worker with an artifact store
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
