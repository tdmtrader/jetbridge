@PW-03
Feature: Watching one pod on a real Kubernetes API server

  PROTOTYPE. The rest of the suite drives client-go's fake clientset, which
  does not honour field selectors — so covering PW-03 there required WatchBus,
  a hand-written reimplementation of API-server selector filtering.
  Here the real kube-apiserver enforces the selector, and there is no double.

  # A namespace with two pods. The neighbour changes first, so an unscoped
  # watch hands the step the wrong pod's phase.
  @PW-03
  Scenario: A step is never told about somebody else's pod
    Given a real cluster running pods "watched-pod" and "noisy-neighbour"
    When the runtime reads its pod, then "noisy-neighbour" fails and "watched-pod" starts running
    Then the real API server told the runtime only about its own pod, now "Running"
