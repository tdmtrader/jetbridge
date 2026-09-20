@live-kubernetes
Feature: Watching one pod on a real Kubernetes API server

  The neighbour actually fails before the watched pod starts. Scheduling gates
  control ordering; only the kubelet reports phases. The original identity and
  phase assertion checks events returned by the production watcher.

  # PW-02: the watched pod advances from its initial Pending read to Running.
  # PW-03: the neighbour changes first, so an unscoped watch hands the step
  # the wrong pod's phase. One real lifecycle covers both contracts.
  @PW-02 @PW-03
  Scenario: A step is never told about somebody else's pod
    Given a Kubernetes cluster with gated pods "watched-pod" and "noisy-neighbour"
    When the runtime reads its pod, then "noisy-neighbour" fails and "watched-pod" starts running
    Then the runtime is told its pod is "Running"

  # An exec-released main really reaches Running and exits before the
  # runtime consumes its queued watch updates. The kubelet supplies both phases.
  @PW-02 @watch-burst
  Scenario: A burst of updates does not leave the step on a stale state
    Given a live cluster with gated pod "rapid-pod"
    When the runtime asks what its pod is doing
    And the pod goes "Running" then "Succeeded" before the runtime looks
    Then the runtime is told its pod is "Succeeded"

  # Only owned namespace watch permission is revoked. The real gated pod
  # starts after the established TCP watch closes; direct reads remain allowed.
  @PW-06 @watch-fallback
  Scenario: A watch that cannot be re-established still reports the pod
    Given a live cluster with gated pod "fallback-pod"
    And the runtime has revocable permission to watch its pod
    When the runtime asks what its pod is doing
    And the watch connection drops after its watch permission is revoked
    And the watched pod becomes "Running"
    Then the runtime is told its pod is "Running"

  # The runtime stream is closed while the kubelet changes the pod. Reconnect
  # only after UID-scoped deletion and NotFound: a fresh Get cannot recover it.
  @PW-04 @PW-05 @watch-replay
  Scenario Outline: A reconnect replays a change even after the pod disappears
    Given a live cluster with gated pod "reconnect-pod"
    When the runtime asks what its pod is doing
    And the watch is interrupted and the pod becomes "<phase>" before being deleted
    Then the runtime is told its pod is "<phase>"

    Examples:
      | phase     |
      | Running   |
      | Succeeded |

  # A real expired watch sends a non-pod Status and closes. It cannot send
  # a later Pod on that stream, and reconnecting at the same expired version
  # can never succeed. Recover from the API's current version.
  # Then replay a newer update after deletion proves the watch resumed
  # from that refreshed version instead of relying on another Get.
  Scenario: An expired watch recovers the current pod instead of waiting forever
    Given a real pod "erroring-pod" whose watch history can expire
    When the runtime asks what its pod is doing
    And the pod finishes before its watched history expires
    Then the runtime is told its pod is "Succeeded"
