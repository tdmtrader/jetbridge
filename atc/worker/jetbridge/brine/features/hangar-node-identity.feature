Feature: The artifact daemon obtains its node identity from Kubernetes

  @core-review @HOP-3 @HOP-7
  Scenario: A deployed daemon resolves the Node UID before opening its ledger
    Given a Run producer and a ready output node
    When its artifact daemon restarts with "derived" Kubernetes identity
    And its Run runtime prepares the producer
    Then its runtime control names the retained capture

  @core-review @HOP-3 @HOP-7
  Scenario: A Pod UID cannot be substituted for the Node UID
    Given a Run producer and a ready output node
    When its artifact daemon restarts with "mismatched" Kubernetes identity
    Then its artifact daemon refuses the wrong Kubernetes identity
