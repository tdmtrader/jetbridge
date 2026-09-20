@live-kubernetes
Feature: Artifact reads use a real producer and an independent peer

  # Distinct node/pod addresses share a port. The peer has its own emptyDir.
  # This is daemon failure, not simulated node failure or automatic mirroring.
  Scenario Outline: Producer availability determines whether the peer is contacted
    Given a task using Kubernetes
    When the artifact is read with producer "<producer>" and peer "<peer>"
    Then artifact delivery preserves the exact bytes and peer request contract

    Examples:
      | producer | peer    |
      | running  | present |
      | stopped  | present |
      | stopped  | absent  |
