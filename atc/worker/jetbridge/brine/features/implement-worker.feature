@implement @review-linux
Feature: Producing a patch from an edit-only session without retaining session credentials

  Run the real Linux worker, tmpfs, Git and the jb commands that capture a
  snapshot and apply its change. Only the model process is substituted: it
  edits the workspace the way Codex's file-edit tool does and reports the
  edit. No network model calls are made. These scenarios do not claim
  detached Run, credential handoff or Kubernetes acceptance, nor that real
  Codex reports its edits in this shape.

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
      | mode                   |
      | "edit-outside-scratch" |
      | "forbidden-shell"      |
      | "malformed-edit"       |

  Scenario: A later invocation cannot overwrite a published change
    When the implement worker receives model output "edit"
    Then the published change applies as a local commit on a new branch
    And the existing change is protected from a second invocation
