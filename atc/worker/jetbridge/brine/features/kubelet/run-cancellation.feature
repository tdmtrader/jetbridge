@run-kubelet
Feature: Run cancellation converges on a real kubelet

  Run only by hack/test-run-kubelet, against the single disposable K3s node it
  creates. Real PostgreSQL, output daemon, kubelet, SPDY and worker are used,
  and missing cluster evidence fails. Sidecar and hijack writers under the
  Hangar seal are not covered here (spark run_cancellation_sidecar_hijack_k3s).

  Scenario Outline: No process or check begins after the fence
    Given a Run on a disposable kubelet
    When the kubelet worker starts a prepared "<kind>" after the fence
    Then the kubelet ran no Pod and the node recorded no start

    Examples:
      | kind  |
      | task  |
      | check |

  Scenario Outline: An active command converges to an exact acknowledgement
    Given a Run on a disposable kubelet
    When cancellation interrupts a running "<command>" command on the kubelet
    Then the command stops with an exact acknowledgement and keeps its Pod

    Examples:
      | command        |
      | ordinary       |
      | TERM-resistant |

  Scenario: A provisional source survives an unreachable node daemon
    Given a Run on a disposable kubelet
    When cancellation meets a selected-output producer whose output daemon is down
    Then the provisional source and its Pod survive until the node answers
