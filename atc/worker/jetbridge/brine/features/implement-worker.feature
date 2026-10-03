@implement @review-linux
Feature: Producing a patch from an edit-only session without retaining session credentials

  Run the real Linux worker, its real workspace tool server, tmpfs, Git and
  the jb commands that capture a snapshot and apply its change. Only the
  model process is substituted: it makes its edits through the worker's
  workspace tools, as Codex does under the edit-only policy, and reports
  each call. No network model calls are made. These scenarios do not claim
  a platform Run, credential handoff or Kubernetes acceptance; the Go tests
  in agent/implement run the real pinned Codex.

  Background:
    Given a committed repository and an implementation brief
    And the implementation snapshot is captured from the command line

  Scenario: An edit-only session publishes a patch that applies as a local commit
    When the implement worker receives model output "edit"
    Then the published change applies as a local commit on a new branch
    And the implement session leaves no credential files or credential-bearing output
    And the captured implementation snapshot is unchanged

  Scenario Outline: A session outside the edit-only policy publishes nothing
    When the implement worker receives model output <mode>
    Then the implement worker fails without publishing a change
    And the implement session leaves no credential files or credential-bearing output
    And the captured implementation snapshot is unchanged

    Examples:
      | mode              |
      | "forbidden-shell" |
      | "native-edit"     |

  # The model asks the workspace server to overwrite the staged credential
  # beside the workspace. The server refuses before anything changes, and the
  # model stops (so nothing is published) if it does not; the change it made
  # inside the workspace is published.
  Scenario: An edit outside the workspace is refused before it changes anything
    When the implement worker receives model output "edit-outside-scratch"
    Then the published change applies as a local commit on a new branch
    And the implement session leaves no credential files or credential-bearing output
    And the captured implementation snapshot is unchanged

  Scenario: A later invocation cannot overwrite a published change
    When the implement worker receives model output "edit"
    Then the published change applies as a local commit on a new branch
    And the existing change is protected from a second invocation

  Scenario: A session reads review findings it was given and records the review Run
    Given the implementation snapshot is recaptured carrying the findings of review Run 3
    When the implement worker receives model output "edit"
    Then the published change applies as a local commit on a new branch
    And the model read the review findings through the workspace tools
    And the published change records review Run 3 as the findings it addresses
    And the implement session leaves no credential files or credential-bearing output
    And the captured implementation snapshot is unchanged
