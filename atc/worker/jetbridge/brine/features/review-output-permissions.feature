@review @review-linux
Feature: The review image can publish into a real held output

  This checks the configured image UID with Linux permissions against the
  actual step directory the daemon holds for the capture. Kubelet mount enforcement is tested in live CI.

  Scenario: The image user can publish a report without changing source permissions
    Given a Run producer and a ready output node
    Then the review image user can write its captured output
