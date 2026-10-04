Feature: Running and operating a queue from the command line

  Scenario: An operator admits a change while the runner holds the lease and the runner queues it on its next step
    Given a queue config file for an empty queue
    And a runner holds the queue's lease
    When the operator admits change "a" at a commit in their repository
    Then the admit succeeds saying the runner will queue it
    And the queue status does not list "a" yet
    When the runner takes its next step
    Then the queue status lists "a" as queued
    And the runner still holds its lease

  Scenario: A queue config with the log notifier builds its driver
    Given a queue config file that names the log notifier writing to standard output
    When the runner is built from it
    Then the runner announces events to that log

  Scenario: A credential inside a reason is hidden in status
    Given a queue paused for a reason that holds a URL with a password
    When the status is read
    Then it shows the URL with the password hidden
