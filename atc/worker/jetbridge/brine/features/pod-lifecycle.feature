@PE-09 @PE-10 @RF-08 @RF-10 @RF-11 @RF-12 @RF-13 @RF-15 @SC-08 @SC-09 @SC-10
Feature: What a step gets back from its pod

  A build step hands Kubernetes a command and then waits. This is everything it
  gets back: an exit status, a cleaned-up cluster, and — when the pod does not
  come back at all — a build log that says whose fault that was.

  ../features/failure-priority.feature covers naming the failure. This file
  covers the rest of what process_test.go asserted: the ordinary result, the
  diagnostics that go with a bad one, the deadlines, the transient errors the
  runtime absorbs, and the sidecars that must not outlive the step.

  Source: k8s_runtime_behavioral_spec_20260331 (PE-09, PE-10, RF-08, RF-10 to
  RF-13, RF-15, SC-08 to SC-10). Migrated from process_test.go, which carried
  no requirement identifiers.

  # PE-09. The exit status is the step's whole result, and the pod going away
  # afterwards is what keeps a busy cluster from filling with finished pods.
  @PE-09
  Scenario: A step whose pod succeeds reports success and leaves nothing behind
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "wait-success" is running
    When the pod ends with the main container exiting 0
    Then the step reports exit status 0
    And the pod has been removed from the cluster

  # A non-zero exit is a failed step, NOT a failed runtime: the error return is
  # reserved for the runtime being unable to say what happened. Conflating the
  # two is what turns "your tests failed" into "errored".
  @PE-09
  Scenario: A step whose command fails reports the exit code rather than an error
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "wait-nonzero" is running
    When the pod ends with the main container exiting 1
    Then the step reports exit status 1

  # PE-10. Aborting a build has to take the pod with it, or the node keeps
  # paying for work nobody is waiting for.
  #
  # NOTE, recorded rather than smoothed over: Process.Wait selects between the
  # cancellation and the poll goroutine, and only the cancellation branch
  # deletes the pod. Both can be ready at once, so this scenario depends on the
  # cancellation branch winning — which it does, because the poll goroutine has
  # not been scheduled yet when the select runs. The ginkgo test it came from
  # relies on exactly the same thing. If this ever goes intermittently red, the
  # race is real and the fix belongs in process.go, not here.
  @PE-10
  Scenario: A cancelled build takes its pod with it
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "wait-cancelled" is running
    When the build is cancelled while the step is waiting
    Then the step fails saying "context canceled"
    And the pod has been removed from the cluster

  # RF-10. A pull failure the user can act on names the image they got wrong
  # and shows that scheduling was fine, so they do not go looking at the
  # cluster.
  @RF-10
  Scenario: An image that cannot be pulled names the image and the scheduling that worked
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "diag-imagepull" is running
    When the main container cannot pull the image "nonexistent:latest" after being scheduled onto "node-1"
    Then the step fails saying "ImagePullBackOff"
    And the build log shows "nonexistent:latest"
    And the build log shows "Condition: PodScheduled=True"

  # RF-10. The step's own container is fine; something running alongside it is
  # not. Naming which container failed is the difference between a fixable
  # build and a mystery.
  #
  # process_test.go has this case TWICE under one name ("includes sidecar
  # container status in diagnostics", lines 446 and 490), differing only in the
  # sidecar and image names. It is one requirement with two sets of fixture
  # strings, so it migrates as one outline with two rows.
  @RF-10 @SC-08
  Scenario Outline: A sidecar that cannot start is named in the diagnostics
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "diag-sidecar" built from image "docker:///busybox"
    And a sidecar "<sidecar>" runs "<image>" alongside it
    And the described container starts
    When the sidecar "<sidecar>" cannot pull the image "<image>" while the main container is still being created
    Then the step fails saying "ImagePullBackOff"
    And the build log shows "<sidecar>"
    And the build log shows "<image>"

    Examples:
      | sidecar       | image               |
      | my-sidecar    | bad-sidecar:latest  |
      | redis-sidecar | redis:bad-tag       |

  # RF-10/RF-11. An eviction is the cluster's decision. The build log has to
  # carry the node and the kubelet's own reason, or the user starts debugging
  # a pipeline that was never wrong.
  #
  # process_test.go's "writes eviction reason to stderr" is the same assertion
  # without the node, and is covered here and by failure-priority.feature's
  # RF-05 scenario between them; it is not repeated as a third scenario.
  @RF-10 @RF-11
  Scenario: An evicted step names the node it was evicted from and why
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "diag-nodename" is running
    When the node "gke-pool-spot-a1b2c3" evicts the pod for running out of "ephemeral-storage"
    Then the build log shows "Node: gke-pool-spot-a1b2c3"
    And the build log shows "Evicted"
    And the build log shows "low on resource: ephemeral-storage"

  # RF-10. Killed once is bad luck; killed twice is a memory limit that is too
  # low, and only the restart history distinguishes them.
  @RF-10
  Scenario: A repeatedly OOM-killed container shows its message and its restart history
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "diag-restarts" is running
    When the main container on node "node-1" is killed twice for exceeding "512Mi"
    Then the step fails saying "OOMKilled"
    And the build log shows "Node: node-1"
    And the build log shows "OOMKilled (exit code 137)"
    And the build log shows "container exceeded 512Mi memory limit"
    And the build log shows "RestartCount: 2"
    And the build log shows "Last termination: OOMKilled"

  # RF-11. The node was out of disk and it was a spot instance — two facts that
  # together say "this will happen again, move the workload" and that nothing
  # in the pod's own status can tell you.
  @RF-11
  Scenario: An eviction from a pressured spot node explains both
    Given a jetbridge worker on a fake Kubernetes cluster
    And the cluster has a spot node "gke-spot-node-1" that is short of disk
    And a task container "diag-nodepressure" is running
    When the node "gke-spot-node-1" evicts the pod for running out of "ephemeral-storage"
    Then the build log shows "DiskPressure=True"
    And the build log shows "disk usage exceeds threshold"
    And the build log shows "spot/preemptible instance"
    And the build log shows "cloud.google.com/gke-spot=true"

  # RF-11. A drain is planned maintenance. Saying so stops the user filing a
  # bug against their pipeline.
  @RF-11
  Scenario: An eviction from a draining node says the node was cordoned
    Given a jetbridge worker on a fake Kubernetes cluster
    And the cluster has a cordoned node "draining-node-1"
    And a task container "diag-cordoned" is running
    When the node "draining-node-1" evicts the pod for running out of "memory"
    Then the build log shows "cordoned (unschedulable)"
    And the build log shows "node may be draining"

  # RF-11. Diagnostics are best-effort. A node that has already been deleted
  # must degrade to a line in the log, not to a second failure on top of the
  # first.
  @RF-11
  Scenario: A node that no longer exists does not cost a second failure
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "diag-nonode" is running
    When the node "nonexistent-node" evicts the pod for running out of "memory"
    Then the build log shows "nonexistent-node"
    And the build log shows "unable to fetch details"

  # RF-08. A pod that never starts must not hold a build open forever, and the
  # deadline it hit is worth nothing without the pod's state alongside it.
  #
  # process_test.go splits this over two Its ("times out waitForRunning after
  # the configured duration" and "writes diagnostics to stderr on timeout")
  # which build the same pod and assert one thing each; they are one scenario
  # here. The worker is deliberately impatient: on the five-minute default this
  # scenario would not fail, it would hang.
  @RF-08
  Scenario: A pod that never starts times out with its state in the log
    Given a jetbridge worker that gives a pod only a moment to start
    And a task container "startup-timeout" is running
    When the pod is scheduled but never reaches Running
    Then the step fails saying "timed out"
    And the build log shows "Pod Failure Diagnostics"

  # RF-15. The exec transport reports a severed connection. Only the runtime
  # can go back and find out that the pod was OOM-killed underneath it, and
  # without that the user is left with "unable to upgrade connection".
  @RF-15
  Scenario: An exec severed by an OOM kill reports the OOM, not the broken connection
    Given a jetbridge worker whose exec is severed by an out-of-memory kill
    And a task container "exec-diag-oom" is running
    When the pod reaches Running on node "gke-spot-node-1" and the step execs into it
    Then the step fails saying "exec in pod"
    And the build log shows "Pod Failure Diagnostics"
    And the build log shows "OOMKilled"
    And the build log shows "Node: gke-spot-node-1"

  # RF-15. When even the post-mortem read fails, saying so is better than
  # saying nothing — "the pod is gone" is itself the diagnosis.
  @RF-15
  Scenario: An exec severed by a vanished pod says the pod is gone
    Given a jetbridge worker whose exec is severed after the pod disappears
    And a task container "exec-diag-gone" is running
    When the pod reaches Running on node "node-1" and the step execs into it
    Then the step fails saying "exec in pod"
    And the build log shows "pod no longer exists"

  # RF-12. API servers drop calls. A build that fails on one of them is a build
  # that fails for no reason the user can act on.
  @RF-12
  Scenario Outline: A step survives transient failures to read its pod
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "transient-tolerated" is running
    When the pod succeeds but the next <failures> status reads fail
    Then the step reports exit status 0

    Examples:
      | failures |
      | 1        |
      | 2        |

  # RF-13. Tolerance has a limit; past it the build has to be told, not left
  # hanging on a cluster that is not answering.
  @RF-13
  Scenario: A cluster that stops answering fails the step rather than hanging
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "transient-exhausted" is running
    When every read of the pod status fails
    Then the step fails saying "consecutive API errors"

  # process_test.go's third transient-error case, "resets error count after a
  # successful API call", is NOT migrated as its own scenario, and the reason
  # is worth recording. It installs a reactor scripted fail/fail/succeed/fail/
  # fail/succeed to prove the consecutive counter resets. It does not: the
  # initial-sync loop in watch.go RETURNS on its first success, so reads 4 and
  # 5 never happen and the counter is never re-armed. The test is the
  # two-failure case above wearing a different name — which is why that case
  # is an Examples row here rather than a fourth scenario. There is a reset in
  # the watch-reconnect path (watch.go's consecutiveWatchErrors), but no test
  # in process_test.go exercised it and none is invented here.

  # The operator's seam, not the user's: an image nobody can pull is a registry
  # or credentials problem, and it shows up as a rate on a dashboard long
  # before anyone reads the build that hit it.
  Scenario: An unpullable image is counted for the operator
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "metric-imagepull" is running
    When the image cannot be pulled, with the failure counters read either side
    Then the metered step fails saying "ImagePullBackOff"
    And the image pull failure count has gone up by 1

  # How long pods take to start is the number that tells an operator their
  # cluster is under-provisioned before any build fails.
  #
  # DRIFT, recorded rather than copied: process_test.go asserts
  # `K8sPodStartupDuration.Max()` is `>= 0`. Gauge.Max() returns the current
  # value — 0 — when nothing has been recorded, so that assertion passes
  # whether or not the runtime ever writes the gauge. It cannot fail. The
  # scenario below makes the pod take a measurable moment to come up so the
  # recorded value can be distinguished from an unrecorded one, which is the
  # assertion the original was reaching for.
  Scenario: A pod that takes a moment to start has that moment recorded
    Given a jetbridge worker that execs into its pods
    And a task container "metric-startup" is running
    When the pod takes a moment to reach Running while the step waits
    Then the recorded pod startup duration is at least 1 milliseconds

  # SC-10. The pod outlives the step's own command whenever a sidecar is still
  # running, so somebody has to take it away. If nobody does, a postgres
  # sidecar runs until the reaper notices, on a node the build has finished
  # with.
  @SC-10
  Scenario: A step whose sidecar is still running still finishes, and its pod goes
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-lifecycle" built from image "docker:///busybox"
    And a sidecar "postgres" runs "postgres:15" alongside it
    And the described container starts
    When the main container exits 0 while the sidecar "postgres" keeps running
    Then the step reports exit status 0
    And the pod has been removed from the cluster

  @SC-10
  Scenario: A failing step with a running sidecar still reports its own exit code
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-nonzero" built from image "docker:///busybox"
    And a sidecar "redis" runs "redis:7" alongside it
    And the described container starts
    When the main container exits 42 while the sidecar "redis" keeps running
    Then the step reports exit status 42

  # SC-08. A sidecar that never starts is a step that never starts. Failing
  # fast is the difference between a clear error and a build that sits at the
  # startup deadline.
  @SC-08
  Scenario: A sidecar that cannot start fails the step instead of stalling it
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-failfast" built from image "docker:///busybox"
    And a sidecar "bad-image" runs "nonexistent:latest" alongside it
    And the described container starts
    When the sidecar "bad-image" cannot pull the image "nonexistent:latest" while the main container is still being created
    Then the step fails saying "ImagePullBackOff"

  # SC-09. The other half of SC-08, and the one that matters more: once the
  # step's command has exited, a broken sidecar is not the user's problem and
  # must not turn a green build red.
  @SC-09
  Scenario: A sidecar that breaks after the step has finished does not fail it
    Given a jetbridge worker on a fake Kubernetes cluster
    And a task container "sidecar-late-failure" built from image "docker:///busybox"
    And a sidecar "bad-image" runs "nonexistent:latest" alongside it
    And the described container starts
    When the sidecar "bad-image" cannot pull the image "nonexistent:latest" after the main container exited 0
    Then the step reports exit status 0
