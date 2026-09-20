@live-kubernetes
Feature: Reading recorded artifacts from a real node

  The actual Node supplies its address. Worker-created volumes retain their
  database identity and output handles; the real daemon serves owned storage.
  Layout cases write through the producer pod's actual mounts before recording.

  Scenario: A finished step's output can be read back by name from the node that ran it
    Given a jetbridge worker with a real node's artifact daemon
    And the ATC cannot go looking for daemons it was not told about
    And the "task" step "build-42" ran on the artifact node
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    When the worker records where the step's outputs went
    Then the output "build-42-output-result" reads back as "compiled binary"

  Scenario: A task's outputs are read back from the directories its own pod wrote them to
    Given a jetbridge worker with a real node's artifact daemon
    And the "task" step "build-42" ran on the artifact node
    And it writes "the compiled binary" to its output "binary" in the volume "build-42-output-binary"
    And it writes "the test report" to its output "report" in the volume "build-42-output-report"
    And the bytes reached the node through the mounts the step's own pod gave it
    When the worker records where the step's outputs went
    Then the output "build-42-output-binary" reads back as "the compiled binary"
    And the output "build-42-output-report" reads back as "the test report"

  Scenario: A get step's resource is read back from the directory its own pod wrote it to
    Given a jetbridge worker with a real node's artifact daemon
    And the "get" step "get-42" ran on the artifact node
    And it fetched "the fetched resource" into its working directory, which is the volume "get-42-dir"
    And the bytes reached the node through the mounts the step's own pod gave it
    When the worker records where the step's outputs went
    Then the output "get-42-dir" reads back as "the fetched resource"

  Scenario: A recorded volume lookup preserves database identity and delivery
    Given a jetbridge worker with a real node's artifact daemon
    And an artifact volume "rc-9" the worker can look up
    And the node's daemon holds the artifact "rc-9" containing "the cached resource"
    When the worker looks up "rc-9" with "recorded" location and "published" discovery
    Then the lookup has database identity "yes" and delivers "the cached resource"

  Scenario: A step's output is copied to an independent peer
    Given a jetbridge worker whose outputs are mirrored to an independent peer
    And the "task" step "build-42" ran on the artifact node
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    When the worker records where the step's outputs went
    Then the independent peer holds a copy of the output "result" containing "compiled binary"

  Scenario: Every output is copied, not just the first
    Given a jetbridge worker whose outputs are mirrored to an independent peer
    And the "task" step "release" ran on the artifact node
    And its output "binary" is the volume "release-output-binary" holding "the compiled binary"
    And its output "report" is the volume "release-output-report" holding "the test report"
    And its output "logs" is the volume "release-output-logs" holding "the build logs"
    When the worker records where the step's outputs went
    Then the independent peer holds a copy of the output "binary" containing "the compiled binary"
    And the independent peer holds a copy of the output "report" containing "the test report"
    And the independent peer holds a copy of the output "logs" containing "the build logs"
    And the output "release-output-binary" reads back as "the compiled binary"
    And the output "release-output-report" reads back as "the test report"
    And the output "release-output-logs" reads back as "the build logs"

  Scenario: A refused mirror does not lose a finished output
    Given a jetbridge worker whose outputs are mirrored to an independent peer
    And the "task" step "build-42" ran on the artifact node
    And its output "result" is the volume "build-42-output-result" holding "compiled binary"
    When the worker records the outputs using equivalent handle "build-42/."
    Then recording succeeds despite the mirror refusal
    And the output "build-42-output-result" reads back as "compiled binary"

  Scenario: A cache registered for a get step is copied to an independent peer
    Given a jetbridge worker whose outputs are mirrored to an independent peer
    And the "get" step "get-handle" ran on the artifact node
    And it fetched "the fetched resource" into its working directory, which is the volume "get-handle-dir"
    And the bytes reached the node through the mounts the step's own pod gave it
    When the worker registers the resource cache "rc-42" for that step's output
    Then registering the cache succeeded
    And the independent peer holds a copy of the output "dir" containing "the fetched resource"
