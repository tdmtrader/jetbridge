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

  # Reddened by: BuildAffinity emitting only concourse.dev/hangar-output-v1 and
  # dropping concourse.dev/hangar-execution-control-v1 — this reddens on its
  # second affinity line while the ordinary-pod control above stays green.
  #
  # The node line is the Phase 4 round-2 ruling 3: a reservation is issued by
  # ONE daemon and the directory it named exists on ONE node, so a capture pod
  # that lands anywhere else mounts an empty hostPath. It is declared here so
  # Phase 8 inherits a line rather than a comment.
  #
  # Reddened by: BuildAffinity dropping the kubernetes.io/hostname expression —
  # this reddens on the node line while the two label lines above it stay green.
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
    And the capture pod is admitted only by the node "hangar-node-a"

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
