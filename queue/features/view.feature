Feature: The queue publishes its state for the panel
  The queue turns its state into the panel's generic view format, so a pipeline
  page can show what is testing, queued, landed and ejected without knowing the queue.

  Scenario: The queue publishes its state in the panel's view format
    Given a queue with one change testing, two queued and one landed
    When the queue's view is published
    Then the view names the view/v1 schema and has the four groups and the fixed stat tiles

  Scenario: A paused queue shows a banner with the reason
    Given a queue paused for a reason
    When the queue's view is published
    Then the view's banner gives the reason

  Scenario: A very long list is cut with a "+N more" row under the size cap
    Given a queue with thousands of landed changes
    When the queue's view is published
    Then the view is under 64 KiB and the landed list ends with a "+N more" row

  Scenario: An ejected flake is shown with its reason
    Given a change that was ejected and a flake that passed alone
    When the queue's view is published
    Then both appear under ejected, each with its reason

  Scenario: An empty queue publishes a valid view
    Given a queue with nothing in it
    When the queue's view is published
    Then every group is empty with a message and no row

  Scenario: A credential inside a reason is hidden in the view
    Given a queue paused for a reason that holds a URL with a password
    When the queue's view is published
    Then the view shows the URL with the password hidden

  Scenario: A list one row over the panel's row limit still ends with its marker
    Given a queue with 201 landed changes
    When the queue's view is published
    Then the landed list has 200 rows, the last of them the "+N more" marker
