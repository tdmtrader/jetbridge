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

  Scenario: batch.order defaults to proven-first, takes strict, and refuses an unknown value
    Given a queue config file with no batch.order, one that sets it to strict and one that sets it to fifo
    When the config file is loaded
    Then the order is proven-first, then strict, and fifo is refused naming the allowed values

  Scenario: A URL holding a credential is refused naming the setting
    Given a queue config file whose repository address holds a user and password
    When the config file is loaded
    Then loading is refused naming the repository address setting
    And the refusal does not show the password

  Scenario: The pause cool-down defaults to five minutes and zero turns auto-resume off
    Given a queue config file with no pause section, one that sets pause.cooldown to 30s and one that sets it to 0s or less than 0s
    When the config file is loaded
    Then the cool-down is five minutes, thirty seconds, none, and a negative one is refused

  Scenario: A compose hook is read as an argument list with a timeout
    Given a queue config file that sets compose.hook to a command and its arguments, and compose.hook_owned to path prefixes
    When the config file is loaded
    Then the hook is that argument list and its timeout defaults to fifteen minutes

  Scenario: A compose hook that is not an argument list or whose timeout is not positive is refused
    Given a queue config file whose compose.hook is a string, an empty list or starts with an empty word, or whose compose.hook_timeout is zero
    When the config file is loaded
    Then loading is refused
