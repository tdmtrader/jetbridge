@PE-01 @PE-11
Feature: A container across runs

  A step's container outlives any one execution of it. A check runs on a timer
  against the same container; a web restart re-attaches to a step already in
  flight. What the runtime remembers, and what it refuses to reuse, is the
  difference between a resumed build and a broken one.

  Source: k8s_runtime_behavioral_spec_20260331 — PE-01, PE-11.
  Migrated from container_test.go.

  # PE-01. A check's pause pod ends when its sleep expires. The next check must
  # get a fresh one — exec-ing into a finished pod fails in a way that looks
  # like the resource itself is broken, which sends the user debugging the
  # wrong thing entirely.
  @PE-01
  Scenario Outline: A finished pod is replaced, not reused
    Given a check step whose previous pod is "<phase>"
    When the check runs again
    Then the step gets a live pod, not the dead one

    Examples:
      | phase     |
      | Succeeded |
      | Failed    |

  # PE-11. The property store is the runtime's in-process memory of a step's
  # result, and it is the first place Attach looks — before it asks Kubernetes
  # anything at all.
  @PE-11
  Scenario: What a container records can be read back
    Given a container that has recorded "my-key" as "my-value"
    Then reading it back yields "my-key" as "my-value"

  # PE-12. The most consequential recovery path in the runtime. When the web
  # restarts mid-build it re-attaches to steps already in flight; if it cannot
  # tell that a step finished, it runs it a second time — in a workspace that
  # already contains its outputs.
  @PE-12
  Scenario: A step the runtime still remembers is not run again
    Given a step the runtime still remembers finishing with exit code 0
    Then the step is recovered as having exited 0

  # After a restart the in-process memory is gone and the pod annotation is the
  # only surviving record of the result.
  @PE-12 @PE-11
  Scenario Outline: After a restart the pod's own record is enough
    Given a web restart, and a pod annotated as having finished with exit code <code>
    Then the step is recovered as having exited <code>

    Examples:
      | code |
      | 0    |
      | 3    |

  # And when nothing recorded the result, re-attaching must FAIL — reporting
  # success would mark an unfinished step complete and let the build proceed on
  # outputs that were never produced.
  @PE-12
  Scenario: With no record of the result the step is run again rather than assumed
    Given a web restart, and a pod with no record of having finished
    Then the step cannot be recovered and must be run again
