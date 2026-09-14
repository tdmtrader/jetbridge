@authentication
Feature: Durable fly authentication
  A user logs in once and subsequent commands renew that login without asking
  for another password. Logging out ends that client's authorization.

  Scenario Outline: A login renews and persists credentials between fly processes
    Given an authentication server with a private pipeline
    When fly logs in using "<flow>"
    And fly renews the login in a second process
    Then the refreshed fly login reads the private pipeline
    And fly has persisted the replacement refresh credential

    Examples:
      | flow     |
      | password |
      | browser  |

  Scenario: Logging out invalidates the saved fly refresh credential
    Given an authentication server with a private pipeline
    When fly logs in using "password"
    And fly logs out
    Then the saved fly refresh credential is rejected by the issuer

  Scenario: Concurrent fly processes preserve a usable rotated login
    Given an authentication server with a private pipeline
    When fly logs in using "password"
    And two fly processes renew the same login concurrently
    Then both fly processes read the private pipeline
    And a later fly process can use the saved login

  Scenario: A refresh credential is bound to its issuing fly client
    Given an authentication server with a private pipeline
    When fly logs in using "password"
    Then a different OAuth client cannot renew that fly login

  Scenario: Browser renewal restores a usable session after the access cookie expires
    Given an authentication server with a private pipeline
    When the browser logs in to Sky
    And the browser loses its expired access and CSRF cookies
    Then a foreign or missing Origin cannot renew the browser login
    When the browser renews its login from the same origin
    Then the replacement CSRF token authorizes a pipeline mutation
