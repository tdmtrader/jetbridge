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
