@live-kubernetes
Feature: Running a task command

  A task step runs the command the pipeline author wrote and puts its output in
  the build log. The command runs under an in-pod supervisor so that a web
  restart resumes the running command instead of starting a second copy in a
  dirty workspace.

  Source: k8s_runtime_behavioral_spec_20260331 — PE-08 (exec mode command
  execution), and supervisor.go's build-survival contract.

  Commands run in real declared-image pods via production SPDY. The completion
  scenario also observes the actual request to preserve the exact supervised
  command and single-call contract under nil stdin, alongside the live behavior.
  Recovery reconstructs runtime handles; it does not restart a full ATC.

  # Completion also records the status the next web reads and the worker label
  # used by the reaper. The kubelet supplies startup transitions; the duration
  # metric is reset before Wait and never manufactured by a delayed status write.
  # The final check uses the original runtime object after actual pod deletion:
  # its production-written memory must work without annotation fallback.
  @PE-01 @PE-08 @PE-11 @PE-12
  Scenario Outline: A task logs its output and both current and restarted webs recover its completion
    Given a worker running supervised tasks from "<image>" on Kubernetes
    And the task belongs to pipeline "<pipeline>" job "<job>" build "<build>"
    When a task "<handle>" runs "<command>"
    Then the task exits 0
    And the build log contains "hello world"
    And the task forwards "'/bin/sh' '-c' '<command>'" once without stdin or a terminal
    And the finished step left exit status "0" on its container
    And the step's pod is labelled for the worker that owns it
    And the recorded pod startup duration is at least 1 milliseconds
    And the supervisor state belongs to this task pod
    And the pod is a placeholder, not the step's command
    And the pod is still on the cluster afterwards
    When the current web reattaches to the finished task
    Then the task exits 0
    When the web dies after the step finished and a new web takes over
    Then the task exits 0
    When the original web recovers the finished task after its pod is removed
    Then the task exits 0

    Examples:
      | image | handle | command | pipeline | job | build |
      | busybox:1.37.0 | 550e8400-e29b-41d4-a716-446655440000 | echo hello world | my-pipeline | unit-test | 42 |
      | ubuntu:22.04 | task-abc123 | echo hello world && exit 0 | | | |

  # The reason the command is shell-quoted at all. A naive assembly breaks on
  # spaces and operators, and comparing the assembled string to a literal
  # cannot tell you whether the shell will accept it. Plain spaces are already
  # covered by the initial echo hello world task; the rows vary other syntax.
  #
  # NOTE for whoever adds a row: a double quote cannot appear inside a
  # {string} argument — the capture ends at the quote and the step stops
  # matching, which surfaces as `missing_step` from `brine check`. A doc
  # string is the escape hatch, but a Scenario Outline cannot carry one per
  # row, so rows use shell single-quotes.
  @PE-08
  Scenario Outline: A command survives quoting and runs as written
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    When a task "quote-<slug>" runs "<command>"
    Then the build log contains "<output>"
    And the task exits 0

    Examples:
      | slug      | command                          | output      |
      | operator  | echo first && echo second        | second      |
      | quoted    | echo 'a b c'                     | a b c       |
      | path      | echo ./cmd/... -o /tmp/out       | ./cmd/...   |

  # PE-08's last clause: "Extract exit code from ExecExitError on command
  # failure." The immediate result and the status recovered by a new container
  # object are checked separately, so a broken writer or reader cannot hide.
  @PE-08 @PE-11 @PE-12
  Scenario: A failing command's exit code reaches the consumer
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    When a task "failing-task" runs "echo about to fail; exit 3"
    Then the build log contains "about to fail"
    And the task exits 3
    And the web dies after the step finished and a new web takes over
    Then the task exits 3

  # What the supervisor is FOR. The command appends a line each time it
  # actually runs, so the build log shows whether the re-exec resumed the
  # completed run or started a second one. No previous test asserted this;
  # `trap '' HUP` being present in a string does not. Both annotation-present
  # re-exec and missing-annotation recovery must resume this one command.
  Scenario: A web restart resumes a finished task instead of re-running it
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    When a task "survivor" runs "echo ran >> $WORKSPACE/runs; cat $WORKSPACE/runs"
    And the web restarts and the task is re-executed
    Then the build log contains "ran" exactly 1 time(s)
    And the task exits 0
    And the web dies before the exit status is recorded and a new web takes over
    And the new web runs the same step again
    Then attaching was refused saying "no completion status"
    And the cluster is running exactly 1 pod for the step
    And the task exits 0
    And the build log contains "ran" exactly 1 time(s)

  # The companion, and what makes the scenario above discriminating: state is
  # keyed on the process ID AND a hash of the command, so a DIFFERENT command
  # on the same container gets fresh state and really does run. Here the
  # counter reaches two, which is exactly what the resumed case must not do.
  Scenario: A different command on the same container runs rather than resuming
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    When a task "hijacked" runs "echo ran >> $WORKSPACE/runs; cat $WORKSPACE/runs"
    And the web restarts and a different command "echo ran >> $WORKSPACE/runs; cat $WORKSPACE/runs; echo fresh-state" is executed
    Then the build log contains "ran" exactly 2 time(s)
    And the build log contains "fresh-state"
    And the task exits 0

  @PE-01
  Scenario: A failed step's pod is kept for the operator, not cleaned up on the spot
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    When a task "kept-after-failure" runs "exit 42"
    Then the task exits 42
    And the pod is still on the cluster afterwards

  # A pre-created pod with a real init gate and sidecar controls observation
  # timing. This tests runtime lifecycle observation, not pod assembly or
  # automatic artifact input staging.
  @OE-01 @OE-02 @OE-05 @OE-07 @OE-08 @OE-09 @OE-10
  Scenario: A real startup records its lifecycle once and names the actual node
    Given a traced real pod is waiting in its init container
    When the real init container is released after the runtime starts watching
    Then the step exits 0
    And the "k8s.exec-process.wait-for-running" span records the event "pod.initialized"
    And the "k8s.exec-process.wait-for-running" span records the event "pod.scheduled"
    And the "k8s.exec-process.wait-for-running" span records the event "init.container.completed"
    And the "k8s.exec-process.wait-for-running" span records the event "sidecar.started"
    And the "k8s.exec-process.wait-for-running" span records the event "pod.scheduled" exactly 1 time(s)
    And the "k8s.exec-process.wait-for-running" span records the event "pod.phase.pending"
    And the "k8s.exec-process.wait-for-running" span records the event "pod.phase.running"
    And a pod startup duration was recorded
    And both startup observations preserve their lifecycle and actual node

  # A pre-existing OnFailure pod really fails twice and then recovers. This
  # tests deduplication by logical init name across real observations, not
  # production default restart policy or the artifact-fetch implementation.
  @OE-06
  Scenario: A failed init container is recorded as a failure, not a completion
    Given a real init fails repeatedly before recovering
    When the failed real init recovers after repeated observations
    Then the "k8s.exec-process.wait-for-running" span records the event "init.container.failed"
    And the "k8s.exec-process.wait-for-running" span records the event "init.container.failed" exactly 1 time(s)
    And the "k8s.exec-process.wait-for-running" span records the event "init.container.completed" exactly 0 time(s)

  # The actual init cannot unpack a missing archive. This verifies failure
  # diagnostics and failure-only tracing for a pre-existing pod, not the
  # daemon input-fetch protocol. Runtime creates/execs must remain zero after
  # fixture creation. The defensive Succeeded-plus-failed-init input belongs
  # to "does not replace a pod whose own init container failed" in
  # pause_pod_replacement_test.go, never a supplied API status.
  # This single failed-state observation does not
  # replace OE-06's repeated-observation deduplication contract above.
  @RF-14
  Scenario: A step whose inputs could not be staged says so
    Given a traced real pod is waiting to unpack a missing input
    When the real input init fails before the runtime waits for startup
    Then the step is told which init container failed, naming "fetch-input-0"
    And the "k8s.exec-process.wait-for-running" span records the event "init.container.failed" exactly 1 time(s)
    And the "k8s.exec-process.wait-for-running" span records the event "init.container.completed" exactly 0 time(s)

  # Scheduling is gated until the runtime watches. Kubelet status and pull
  # events are independently observed; PullAlways may reuse cached layers.
  @OE-04
  Scenario: The end of an image pull is recorded on the wait span
    Given a traced real pod is held by a scheduling gate
    When the pod is released to pull its image while the runtime watches
    Then the step exits 0
    And the "k8s.exec-process.wait-for-running" span records the event "image.pulled"


  # This is the no-daemon runtime.Volume API, not durable artifact publication.
  # Read the actual returned volume after the task exits; the pod must survive.
  Scenario: A finished task streams its returned output before collection
    Given a worker running supervised tasks from "busybox:1.37.0" on Kubernetes
    And the task in "/tmp/build/workdir" produces output "result" at "/tmp/build/workdir/result"
    When a task "output-extract-handle" runs "printf hello > /tmp/build/workdir/result/output.txt"
    Then the task exits 0
    And the returned output "result" at "/tmp/build/workdir/result" on pod "output-extract-handle" contains "output.txt" with "hello"

  # Application bytes are explicitly staged through a real pod-backed input;
  # this checks mounts and sidecar execution, not daemon init-container fetching.
  Scenario Outline: A task uses its mounted application without runtime input streaming
    Given a worker running supervised tasks from "<image>" on Kubernetes
    And the task mounts application "<artifact>" at "<directory>" with "<service>"
    When a task "<handle>" runs "<command>"
    Then the task forwards "'/bin/sh' '-c' '<command>'" once without stdin or a terminal
    And the task exits 0
    And the build log contains "<output>"

    Examples:
      | image | artifact | directory | service | handle | command | output |
      | node:18 | my-app | /tmp/build/workdir/my-app | PostgreSQL | task-sidecar | npm test | application-ok |
      | busybox | input-vol-1 | /tmp/build/workdir/my-input | no services | noop-stream-handle | echo done | done |
