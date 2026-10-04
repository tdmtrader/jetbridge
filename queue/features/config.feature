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

  Scenario: batch.order is refused as an unknown key
    Given a queue config file that sets batch.order
    When the config file is loaded
    Then loading is refused naming the unknown key

  Scenario: A URL holding a credential is refused naming the setting
    Given a queue config file whose repository address holds a user and password
    When the config file is loaded
    Then loading is refused naming the repository address setting
    And the refusal does not show the password

  Scenario: The pause cool-down defaults to five minutes and zero turns auto-resume off
    Given a queue config file with no pause section, one that sets pause.cooldown to 30s and one that sets it to 0s or less than 0s
    When the config file is loaded
    Then the cool-down is five minutes, thirty seconds, none, and a negative one is refused
