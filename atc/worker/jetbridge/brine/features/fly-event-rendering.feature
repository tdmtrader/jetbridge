Feature: Fly renders production build event streams for terminal users

  Source: all 37 specs in fly/eventstream/render_test.go. Every scenario crosses
  a real TCP HTTP server, production SSEEventStream parsing, and eventstream.Render.

  Scenario: Log payload is rendered exactly
    Given fly renders the production SSE profile "log-output"
    Then fly terminal bytes match expectation "log-output"

  Scenario: Timestamped log payload stays on the timestamped line
    Given fly renders the production SSE profile "log-time"
    Then fly terminal bytes match expectation "log-time"

  Scenario: Error message is bold red and newline terminated
    Given fly renders the production SSE profile "error-output"
    Then fly terminal bytes match expectation "error-output"

  Scenario: Error message reserves a blank timestamp column
    Given fly renders the production SSE profile "error-time"
    Then fly terminal bytes match expectation "error-time"

  Scenario: Initialize task output is bold and newline terminated
    Given fly renders the production SSE profile "initialize-output"
    Then fly terminal bytes match expectation "initialize-output"

  Scenario: Initialize task output is timestamped
    Given fly renders the production SSE profile "initialize-time"
    Then fly terminal bytes match expectation "initialize-time"

  Scenario: Start task output contains the complete bold command
    Given fly renders the production SSE profile "start-output"
    Then fly terminal bytes match expectation "start-output"

  Scenario: Start task output is timestamped
    Given fly renders the production SSE profile "start-time"
    Then fly terminal bytes match expectation "start-time"

  Scenario: Finish task returns its task exit status
    Given fly renders the production SSE profile "finish-exit"
    Then fly exits with status 42

  Scenario: Status after finish is still rendered
    Given fly renders the production SSE profile "finish-status-output"
    Then fly terminal bytes match expectation "finish-status-output"

  Scenario: Status after finish preserves the task exit status
    Given fly renders the production SSE profile "finish-status-exit"
    Then fly exits with status 42

  Scenario: Zero-time status after finish reserves a blank timestamp column
    Given fly renders the production SSE profile "finish-status-time"
    Then fly terminal bytes match expectation "finish-status-time"

  Scenario: Succeeded status is green and newline terminated
    Given fly renders the production SSE profile "status-succeeded-output"
    Then fly terminal bytes match expectation "status-succeeded-output"

  Scenario: Succeeded status exits zero
    Given fly renders the production SSE profile "status-succeeded-exit"
    Then fly exits with status 0

  Scenario: Succeeded status output is timestamped
    Given fly renders the production SSE profile "status-succeeded-time"
    Then fly terminal bytes match expectation "status-succeeded-time"

  Scenario: Failed status is red and newline terminated
    Given fly renders the production SSE profile "status-failed-output"
    Then fly terminal bytes match expectation "status-failed-output"

  Scenario: Failed status exits one
    Given fly renders the production SSE profile "status-failed-exit"
    Then fly exits with status 1

  Scenario: Failed status output is timestamped
    Given fly renders the production SSE profile "status-failed-time"
    Then fly terminal bytes match expectation "status-failed-time"

  Scenario: Errored status is bold red and newline terminated
    Given fly renders the production SSE profile "status-errored-output"
    Then fly terminal bytes match expectation "status-errored-output"

  Scenario: Errored status exits two
    Given fly renders the production SSE profile "status-errored-exit"
    Then fly exits with status 2

  Scenario: Errored status output is timestamped
    Given fly renders the production SSE profile "status-errored-time"
    Then fly terminal bytes match expectation "status-errored-time"

  Scenario: Aborted status uses the production magenta color and a newline
    Given fly renders the production SSE profile "status-aborted-output"
    Then fly terminal bytes match expectation "status-aborted-output"

  Scenario: Aborted status exits three
    Given fly renders the production SSE profile "status-aborted-exit"
    Then fly exits with status 3

  Scenario: Aborted status output is timestamped
    Given fly renders the production SSE profile "status-aborted-time"
    Then fly terminal bytes match expectation "status-aborted-time"

  Scenario: Waiting for worker output is bold and newline terminated
    Given fly renders the production SSE profile "waiting-output"
    Then fly terminal bytes match expectation "waiting-output"

  Scenario: Waiting for worker output is timestamped
    Given fly renders the production SSE profile "waiting-time"
    Then fly terminal bytes match expectation "waiting-time"

  Scenario: Selected worker output includes the bold label and worker name
    Given fly renders the production SSE profile "selected-output"
    Then fly terminal bytes match expectation "selected-output"

  Scenario: Selected worker output is timestamped
    Given fly renders the production SSE profile "selected-time"
    Then fly terminal bytes match expectation "selected-time"

  Scenario: Sidecar attachment renders its bold header
    Given fly renders the production SSE profile "sidecar-attached-output"
    Then fly terminal bytes match expectation "sidecar-attached-output"

  Scenario: Sidecar log includes its name prefix and payload
    Given fly renders the production SSE profile "sidecar-log-output"
    Then fly terminal bytes match expectation "sidecar-log-output"

  Scenario: Main container log keeps its payload and has no sidecar prefix
    Given fly renders the production SSE profile "sidecar-main-output"
    Then fly terminal bytes match expectation "sidecar-main-output"

  Scenario: Unknown event reports a colored parse failure
    Given fly renders the production SSE profile "unknown-output"
    Then fly terminal bytes match expectation "unknown-output"

  Scenario: Unknown event exits 255
    Given fly renders the production SSE profile "unknown-exit"
    Then fly exits with status 255

  Scenario: Ignored unknown event errors exit zero
    Given fly renders the production SSE profile "unknown-ignore"
    Then fly exits with status 0

  Scenario: Missing event data reports generic and detailed parse failure
    Given fly renders the production SSE profile "missing-output"
    Then fly terminal bytes match expectation "missing-output"

  Scenario: Missing event data exits 255
    Given fly renders the production SSE profile "missing-exit"
    Then fly exits with status 255

  Scenario: Ignored missing-data errors exit zero
    Given fly renders the production SSE profile "missing-ignore"
    Then fly exits with status 0
