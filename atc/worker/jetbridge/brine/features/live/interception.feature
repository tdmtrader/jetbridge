@live-kubernetes
Feature: Intercepting real step pods

  Existing pods execute under a real kubelet. These cases test lookup and
  interception, not the worker's creation of the original task pod.
  The raw-handle decoy distinguishes database handles from metadata-based
  pod names. A completed pod keeps the annotation a restarted web reads.

  Scenario: Intercepting a step attaches to the pod the step created
    Given a task using Kubernetes
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception succeeds
    And the operator sees "interactive"
    And the cluster still holds only the pod "my-pipeline-unit-test-b42-task-550e8400"

  Scenario: An interception whose pod is gone says so rather than fabricating one
    Given a task using Kubernetes
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    And that pod has since been reaped
    And a decoy pod named after the handle is running
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception fails saying "has no pod to intercept"
    And the cluster still holds only the pod "550e8400-e29b-41d4-a716-446655440000"

  Scenario: An interception does not replace a pod that already exited
    Given a task using Kubernetes
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    And that pod has since finished with exit status "0"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "echo interactive"
    Then the interception fails saying "already exited"
    And the pod "my-pipeline-unit-test-b42-task-550e8400" still records exit status "0"

  Scenario: An intercepted command's exit code reaches the operator
    Given a task using Kubernetes
    And the worker can exec into pods
    And a task step of build 42 of "my-pipeline/unit-test" was recorded under the opaque handle "550e8400-e29b-41d4-a716-446655440000"
    And the step created the pod "my-pipeline-unit-test-b42-task-550e8400"
    When the operator intercepts the container "550e8400-e29b-41d4-a716-446655440000" and runs "exit 130"
    Then the intercepted command exits 130

  # A pod nobody has a row for is not a container. Reporting it as one would
  # let a caller hijack a pod Concourse cannot account for.
  @RC-01 @orphan-pod
  Scenario: A pod with no container row is not a container
    Given a task using Kubernetes
    And the worker can exec into pods
    And the cluster is running a pod "orphan-pod" that no container row refers to
    When the container "orphan-pod" is looked up
    Then the container is not found


  Scenario Outline: A task hijack forwards its command and terminal options — <session>
    Given a task using Kubernetes
    And the worker can exec into pods
    And a Bash-capable pod "hijack-pod" is ready for interception
    When the operator hijacks "hijack-pod" using "<path>" with arguments "<args>" and terminal "<terminal>"
    Then the hijack preserves its pod and forwards "<quoted>" once with terminal "<terminal>"

    Examples:
      | session | path      | args       | terminal | quoted                      |
      | login   | /bin/bash | -l         | none     | '/bin/bash' '-l'            |
      | tty     | /bin/bash |            | one      | '/bin/bash'                 |
      | pipe    | /bin/sh   | -c,echo hi | none     | '/bin/sh' '-c' 'echo hi'    |
