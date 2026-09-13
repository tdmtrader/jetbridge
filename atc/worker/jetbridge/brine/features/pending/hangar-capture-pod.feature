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
