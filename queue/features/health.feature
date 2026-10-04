Feature: The queue reports when it is unhealthy

  A pipeline job runs the health command on a timer; it goes red while the queue
  is damaged, stuck paused, overdue to resume, or has a run in flight too long.

  Scenario: A healthy queue passes
    Given a queue that is running normally
    When the operator checks its health
    Then the check passes

  Scenario: A damaged queue state is unhealthy
    Given a queue whose stored state cannot be read
    When the operator checks its health
    Then the check fails saying the state is damaged

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
