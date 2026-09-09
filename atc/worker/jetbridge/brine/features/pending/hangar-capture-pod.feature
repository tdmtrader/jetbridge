Feature: What a capture-selected task's Pod says

  NOT RUN YET. This file lives under features/pending/, which the .brine
  manifest's `features: "features/*.feature"` glob deliberately does not match.
  Its scenarios are checked — `brine check features/pending` walks every chain,
  and ../../steps/vocabulary_test.go holds every phrase here to the same
  undefined/dead/shadowing guards the running corpus gets — but they are not
  executed and they are not coverage. Each moves up one directory when the
  phase named in its steps' failure has landed. See ../README.md.

  Everything here is readable off the Pod the cluster hands back. What a real
  kubelet DOES with that Pod is a K3s flow under a build tag and stays in Go.

  @HOP-1 @HOP-3 @HOP-12
  Scenario: Selecting capture for a declared output puts the hold init container before every writer
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture pod carries 2 init containers
    And the capture control init runs before every writer

  # The control the whole file needs. An unchanged-pod assertion passes on a
  # worker that builds no capture pods at all, so it is only meaningful beside
  # them — which is why it lives here rather than in a file of its own, and why
  # Phase 9 lists it as the AC 20 regression twin without writing a second copy.
  #
  # Reddened by: Container.buildPod adding the capture control init when
  # DurableOutputCapture is nil.
  @HOP-1 @HOP-59
  Scenario: A task whose output is not selected for capture builds the pod it builds today
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    When the container runs
    Then the pod has 1 volumes
    And every mount in the pod names exactly one of its volumes
    And the step sees a volume mounted at "/tmp/build/result"

  # Convention 5: "no credential" passes on a worker that ignores its
  # configuration entirely, so the PRESENCE half is the line above it, in the
  # same scenario, and it is asserted first.
  #
  # Reddened by: buildPod copying the source-control grant env into the main
  # container — the absence line reddens and the presence line above stays green.
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
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the capture pod declares 2 mounts, and every one resolves to a declared Volume

  # Reddened by: BuildAffinity emitting only concourse.dev/hangar-output-v1 and
  # dropping concourse.dev/hangar-execution-control-v1 — this reddens on its
  # second affinity line while the ordinary-pod control above stays green.
  @HOP-58
  Scenario: A capture pod requires both ready labels, and neither alone admits it
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    And the worker's cohort is ready for "hangar-output-v1"
    When the capture pod is built
    Then the capture pod requires 2 ready labels
    And the capture pod is admitted only by a node carrying "concourse.dev/hangar-execution-control-v1"

  # An absence with its control in the same scenario: "no capture pod" passes on
  # a worker that builds no pods at all, so the ordinary pod is asserted first.
  @HOP-58 @HOP-59
  Scenario: A worker whose output facet is not enabled builds no capture pod, while the base-only cohort still builds an ordinary one
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    When the container runs
    Then the pod has 1 volumes
    And the step sees a volume mounted at "/tmp/build/result"

  @HOP-58
  Scenario: A ready label without a matching handshake admits nothing, while the handshaken cohort admits
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    And the worker's cohort is ready for "hangar-output-v1"
    And the daemon cohort has not handshaked
    When the capture pod is built
    Then no capture pod is built

  # The AC 20 twin that is not a pod shape: the output plane must not redirect a
  # strict input. Its control is the strict-input presence half
  # ../container-pod.feature:367-373 and :442-446 already assert, which is why
  # this scenario states the bucket rather than restating those.
  #
  # Reddened by: BuildFetchInitContainers routing a strict-input HangarTree
  # through the output publisher's namespace instead of the strict-input bucket.
  @HOP-19 @HOP-59
  Scenario: A capture pod's strict-input fetch still reads the strict-input bucket
    Given a jetbridge worker with an artifact store
    And a task container "build" built from image "busybox"
    And it produces an output at "/tmp/build/result"
    And its output "result" is captured when the step succeeds
    When the capture pod is built
    Then the pod's fetch init container reads from the bucket "strict-input"
