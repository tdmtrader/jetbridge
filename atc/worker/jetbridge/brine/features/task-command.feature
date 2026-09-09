Feature: Running a task command

  A task step runs the command the pipeline author wrote and puts its output in
  the build log. The command runs under an in-pod supervisor so that a web
  restart resumes the running command instead of starting a second copy in a
  dirty workspace.

  Source: k8s_runtime_behavioral_spec_20260331 — PE-08 (exec mode command
  execution), and supervisor.go's build-survival contract.

  These scenarios replace nine `expectSupervisedExec(...)` call sites, which
  asserted that an assembled string was `sh -c <script>`, contained the
  shell-quoted user command, and contained `trap '' HUP`. None of that runs the
  command. Everything below does.

  # Completion also records the status the next web reads and the worker label
  # used by the reaper. A delayed fake-kubelet startup lets this same supervised
  # task verify the operator's duration metric, including a reset before Wait.
  @PE-01 @PE-08
  Scenario: A task's startup is timed and its output reaches the build log
    Given a jetbridge worker that really runs task commands
    When a task "echo-task" runs "echo hello world"
    Then the build log contains "hello world"
    And the task exits 0
    And the finished step left exit status "0" on its container
    And the step's pod is labelled for the worker that owns it
    And the recorded pod startup duration is at least 1 milliseconds
    And the supervisor state belongs to this task workspace
    And the pod is a placeholder, not the step's command
    And the pod is still on the cluster afterwards

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
    Given a jetbridge worker that really runs task commands
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
  @PE-08
  Scenario: A failing command's exit code reaches the consumer
    Given a jetbridge worker that really runs task commands
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
    Given a jetbridge worker that really runs task commands
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
    Given a jetbridge worker that really runs task commands
    When a task "hijacked" runs "echo ran >> $WORKSPACE/runs; cat $WORKSPACE/runs"
    And the web restarts and a different command "echo ran >> $WORKSPACE/runs; cat $WORKSPACE/runs; echo fresh-state" is executed
    Then the build log contains "ran" exactly 2 time(s)
    And the build log contains "fresh-state"
    And the task exits 0
