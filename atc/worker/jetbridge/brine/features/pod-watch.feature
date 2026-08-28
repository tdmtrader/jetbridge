@PW-01 @PW-02 @PW-03 @PW-04 @PW-05 @PW-06 @PW-07
Feature: Keeping sight of a pod while a step runs

  A step that stops hearing about its pod hangs until it times out. So the
  runtime's watch has one job — keep telling the step what the pod is doing —
  and it has to keep doing it when the Kubernetes connection drops, when it
  reconnects, and when it cannot reconnect at all.

  Source: k8s_runtime_behavioral_spec_20260331 — PW-01 to PW-07. Migrated from
  watch_test.go, which carried no requirement identifiers. NOT whole: PW-03
  (field-selector scoping) has no scenario — see the note below.

  # PW-01, PW-02: the first answer comes from a direct read, not from the
  # watch, so a pod that changed before the watch existed is not missed.
  Scenario: The first answer does not wait for a watch event
    Given a real cluster running pod "watch-pod"
    When the runtime asks the real cluster what its pod is doing
    Then the runtime is really told the pod is "Pending"

  Scenario: Subsequent changes arrive as they happen
    Given a real cluster running pod "watch-pod"
    When the runtime asks the real cluster what its pod is doing
    And the pod really becomes "Running"
    Then the runtime is really told the pod is "Running"

  # PW-04, PW-05: the reconnect delivers a change that landed during the gap.
  #
  # CORRECTION — this comment used to claim the scenario "would fail if the
  # runtime reconnected from scratch". That is false, and a review caught it:
  # the watch reactor here returns the second fake watcher on any call
  # regardless of ListOptions, and nothing captures ResourceVersion, so a
  # from-scratch reconnect passes this scenario unchanged. What it does prove
  # is CONTINUITY — the step is still told about the change — which is the
  # part a hanging step would lose. The resource-version mechanism itself is
  # not covered.
  Scenario: A dropped connection does not lose the change that happened during it
    Given a pod "reconnect-pod" that the runtime is watching
    And the connection to Kubernetes drops and comes back
    When the runtime asks what the pod is doing
    And the watch connection is interrupted and the pod becomes "Running"
    Then the runtime is told the pod is "Running"

  # PW-06: when the watch cannot be re-established the runtime falls back to
  # reading directly rather than giving up on the step.
  Scenario: A watch that cannot be re-established still reports the pod
    Given a pod "fallback-pod" that the runtime is watching
    And the connection to Kubernetes drops and cannot be re-established
    When the runtime asks what the pod is doing
    And the watch connection is interrupted and the pod becomes "Running"
    Then the runtime is told the pod is "Running"

  # PW-07: eviction, node failure, spot preemption and a human with kubectl all
  # arrive as the same event, and the step has to be told rather than hanging.
  Scenario: A pod deleted out from under the step is reported, not waited on
    Given a real cluster running pod "doomed-pod"
    When the runtime asks the real cluster what its pod is doing
    And the pod is really deleted out from under the step
    Then the runtime is really told the pod was deleted

  Scenario: Cancelling a build that is already waiting stops the wait
    Given a real cluster running pod "hanging-pod"
    When the runtime asks the real cluster what its pod is doing
    And the build is cancelled while the runtime waits on the real cluster
    Then the runtime really stops waiting

  # The other cancellation branch, and the one a real cluster cannot reach.
  # Above, the runtime is cancelled while its watch is still delivering, so the
  # loop comes round and the check at the top of it notices. Here the channel is
  # empty and stays empty: the runtime is blocked INSIDE the read, and only the
  # select's own cancellation case can interrupt it.
  #
  # Removing that case leaves the scenario above green — measured, not assumed —
  # because a live API server never goes quiet long enough to hold the runtime
  # there. A step in this state ignores cancellation and hangs until its build
  # times out, so the fake is not a shortcut here; an empty channel is the
  # condition under test.
  Scenario: Cancelling a build stops a wait that has nothing to wait on
    Given a pod "silent-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the build is cancelled while the runtime is still waiting
    Then the runtime is told to stop waiting

  # A watch delivers a burst of updates faster than the step consumes them.
  # What must never happen is the step settling on a stale one — it would wait
  # on a pod state the cluster has already left behind.
  Scenario: A burst of updates does not leave the step on a stale state
    Given a real cluster running pod "rapid-pod"
    When the runtime asks the real cluster what its pod is doing
    And the pod really goes "Running" then "Succeeded" before the runtime looks
    Then the runtime is really told the pod is "Succeeded"

  # The other half of PW-04: not just that the runtime reconnects, but that it
  # resumes from where it left off. This connection replays what happened
  # during the gap only to a watcher that names the version it last saw —
  # which is what a real API server does. A reconnect from scratch is told
  # nothing, and the step waits for a pod that has already finished.
  @PW-04 @PW-05
  Scenario: A reconnect resumes from the last version, so the finish is not missed
    Given a pod "resume-pod" that the runtime is watching
    And the connection replays only what happened after the version it is given
    When the runtime asks what the pod is doing
    And the pod finishes while the connection is down
    Then the runtime is told the pod is "Succeeded"

  # Not everything on a watch channel is a pod.
  Scenario: A watch error is stepped over, not mistaken for a pod
    Given a pod "erroring-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the watch reports an error instead of a pod
    And the pod becomes "Running"
    Then the runtime is told the pod is "Running"
