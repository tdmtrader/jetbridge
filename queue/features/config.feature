Feature: One config file describes one queue
  Each queue is described by its own file, and the file is checked strictly
  before anything runs, so a typo or a missing setting is never guessed past.

  Scenario: An unknown key is refused naming the nearest
    Given a queue config file that sets "batch.maxx" to 4
    When the config file is loaded
    Then loading is refused
    And the refusal names "batch.maxx" and suggests "batch.max"

  Scenario: A missing repository is refused
    Given a queue config file with no repository address
    When the config file is loaded
    Then loading is refused
    And the refusal says the repository address is required
