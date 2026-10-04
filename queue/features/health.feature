Feature: The queue reports when it is unhealthy

  A pipeline job runs the health command on a timer; it goes red while the queue
  is stuck paused, overdue to resume, or has a run in flight too long. It exits
  0 when healthy, 3 when unhealthy, and any other non-zero, printing nothing, when
  the state cannot be read.

  Scenario: A healthy queue passes
    Given a queue that is running normally
    When the operator checks its health
    Then the check passes

  Scenario: An unreadable queue state is unknown, not unhealthy, and prints nothing
    Given a queue whose stored state cannot be read
    When the operator checks its health
    Then the check fails as unknown, neither healthy nor unhealthy, and prints nothing

  Scenario: The help names the exit codes
    When the operator asks for the command's help
    Then it names exit 0 healthy, 3 unhealthy, and any other non-zero as state unread

  Scenario: Health reads the resource's source, from a file or on stdin, as well as a config file
    Given a running queue, and the resource's source for it
    When the operator checks its health with that source in a file, on stdin, or with the config file
    Then each passes, and giving both a source and a config file is refused

  Scenario: A pause that nothing resumes is unhealthy after five minutes
    Given a queue paused by a failing land six minutes ago
    When the operator checks its health
    Then the check fails saying nothing resumes the pause

  Scenario: An overdue auto-resume is unhealthy
    Given a queue paused for no verdict ten minutes ago with a five minute cool-down
    When the operator checks its health
    Then the check fails saying the auto-resume is overdue

  Scenario: A run in flight too long is unhealthy
    Given a run that started two hours ago
    When the operator checks its health
    Then the check fails naming the run and how long it has been in flight
