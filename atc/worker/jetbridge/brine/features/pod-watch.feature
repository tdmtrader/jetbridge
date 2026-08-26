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
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    Then the runtime is told the pod is "Pending"

  Scenario: Subsequent changes arrive as they happen
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod becomes "Running"
    Then the runtime is told the pod is "Running"

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
    Given a pod "doomed-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod is deleted out from under the step
    Then the runtime is told the pod was deleted

  Scenario: Cancelling the build stops the wait
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the build is cancelled
    Then the runtime is told to stop waiting

  # A watch delivers a burst of updates faster than the step consumes them.
  # What must never happen is the step settling on a stale one — it would wait
  # on a pod state the cluster has already left behind.
  Scenario: A burst of updates does not leave the step on a stale state
    Given a pod "rapid-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the pod becomes "Running"
    And the pod becomes "Succeeded"
    Then the runtime is told the pod is "Succeeded"

  # PW-03. The watch is scoped to one pod by field selector. A namespace runs
  # many pods, and a step told about somebody else's pod acts on a phase that
  # has nothing to do with it.
  #
  # The earlier attempt at this scenario could not fail and was deleted: it
  # relied on Get(name), which returns our pod by name whether the watch is
  # scoped or not. This one puts the neighbour's event on the WATCH, through a
  # connection that applies the runtime's own field selector the way an API
  # server would. Drop the selector and the neighbour's "Failed" arrives
  # first, and that is what the step is told.
  @PW-03
  Scenario: A step is never told about somebody else's pod
    Given a pod "watch-pod" that the runtime is watching
    And the connection to Kubernetes carries every pod in the namespace
    When the runtime asks what the pod is doing
    And another pod "noisy-neighbour" in the namespace reports "Failed"
    And the pod becomes "Running"
    Then the runtime is told the pod is "Running"

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

  # Cancellation that arrives while the runtime is ALREADY waiting. The
  # scenario above cancels first and asks second, which the non-blocking check
  # at the top of Next's loop catches; this is the only path that exercises
  # the blocked case. Remove that guard and a build cannot be cancelled at
  # all — it hangs until its timeout.
  Scenario: Cancelling a build that is already waiting stops the wait
    Given a pod "hanging-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the build is cancelled while the runtime is still waiting
    Then the runtime is told to stop waiting

  # Not everything on a watch channel is a pod.
  Scenario: A watch error is stepped over, not mistaken for a pod
    Given a pod "erroring-pod" that the runtime is watching
    And the connection to Kubernetes is steady
    When the runtime asks what the pod is doing
    And the watch reports an error instead of a pod
    And the pod becomes "Running"
    Then the runtime is told the pod is "Running"
