@review @review-linux
Feature: The review image can publish into a real reserved output

  This checks the configured image UID with Linux permissions against the
  actual daemon reservation. Kubelet mount enforcement is tested in live CI.

  Scenario: The image user can publish a report without changing source permissions
    Given a Run producer and a ready output node
    Then the review image user can write its reserved output
