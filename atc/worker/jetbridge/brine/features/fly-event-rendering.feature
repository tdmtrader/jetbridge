Feature: Fly renders the real build event stream for terminal users

  Source: all 37 specs in fly/eventstream/render_test.go. Frames cross a real
  SSE HTTP connection, the production event parser, and eventstream.Render.
  Assertions inspect terminal bytes, ANSI status color, timestamp prefixes,
  sidecar labeling, and the process exit status.

  Scenario Outline: Basic event profile <profile>
    Given fly renders the real SSE event profile "<profile>"
    Then fly output contains "<output>"
    And fly exits with status <exit>

    Examples:
      | profile          | output                                                  | exit |
      | log              | hello                                                   | 0    |
      | error            | oh no!                                                  | 0    |
      | initialize       | initializing                                            | 0    |
      | start            | running /some/script arg1 arg2                          | 0    |
      | finish           |                                                         | 42   |
      | finish-status    | succeeded                                               | 42   |
      | waiting          | no suitable workers found, waiting for worker...        | 0    |
      | selected         | selected worker:                                        | 0    |
      | sidecar-attached | sidecar 'log-emitter' attached                          | 0    |
      | sidecar-log      | [log-emitter]                                            | 0    |
      | sidecar-main-log | hello from main                                         | 0    |
      | unknown-event    | failed to parse next event                              | 255  |
      | unknown-event/ignore |                                                         | 0    |
      | missing-data     | missing event data: some-event version 1.0              | 255  |
      | missing-data/ignore |                                                         | 0    |

  Scenario Outline: Timestamped event profile <profile>
    Given fly renders the real SSE event profile "<profile>/time"
    Then fly output contains "<output>"
    And fly output has a timestamp prefix

    Examples:
      | profile          | output                                           |
      | log              | hello                                            |
      | initialize       | initializing                                     |
      | start            | running /some/script arg1 arg2                   |
      | finish-status    | succeeded                                        |
      | status-succeeded | succeeded                                        |
      | status-failed    | failed                                           |
      | status-errored   | errored                                          |
      | status-aborted   | aborted                                          |
      | waiting          | no suitable workers found                       |
      | selected         | selected worker:                                 |

  Scenario: Error events reserve the timestamp column without inventing a time
    Given fly renders the real SSE event profile "error/time"
    Then fly output contains "oh no!"
    And fly output has a blank timestamp prefix

  Scenario Outline: Status event <status>
    Given fly renders the real SSE event profile "status-<status>"
    Then fly renders "<status>" with status color "<color>"
    And fly exits with status <exit>

    Examples:
      | status    | color    | exit |
      | succeeded | green    | 0    |
      | failed    | red      | 1    |
      | errored   | bold-red | 2    |
      | aborted   | magenta  | 3    |

  Scenario: Main-container logs are not mislabeled as sidecar logs
    Given fly renders the real SSE event profile "sidecar-main-log"
    Then fly output does not contain "[log-emitter]"
