Feature: The log notifier writes each event as one line
  Every event the queue announces is appended to a log as one JSON line with
  its time, its kind and its details, so a person can read what the queue did.

  Scenario: An ejected change is written to the log with its reason
    Given a log notifier writing to a log
    When the queue announces that a change was ejected for failing its tests
    Then the log gains one line naming the ejection, the change and the reason

  Scenario: A started batch is written to the log with every change in full
    Given a log notifier writing to a log
    When the queue announces that a batch started
    Then the log line gives each change's id, commit, branch and admission time, and the run's base and changes

  Scenario: Concurrent events each get their own whole line
    Given a log notifier writing to a log
    When many events are announced at once
    Then the log has one whole line for each of them

  Scenario: A multiline reason stays on one line
    Given a log notifier writing to a log
    When the queue announces an ejection whose reason spans several lines
    Then the log gains exactly one line
