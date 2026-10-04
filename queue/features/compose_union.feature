Feature: Keyed-line lists named by the hook script are merged key by key
  Changes in one batch that both edit a "key = value" list the hook names merge by key.

  Scenario: Two rows in one batch that both touch file-length.toml both land
    Given main's hook script names file-length.toml, and changes "a" and "b" each add a different key to it
    When the batch "a", "b" is composed onto main
    Then the candidate composes and its list holds both keys

  Scenario: A key both rows change to different values is still a conflict
    Given main's hook script names file-length.toml, and changes "a" and "b" give one key two values
    When the batch "a", "b" is composed onto main
    Then "b" is named as the conflict

  Scenario: One key added with two values at separate places is a conflict, not two lines
    Given main's hook script names file-length.toml, and changes "a" and "b" add one key with two values far apart
    When the batch "a", "b" is composed onto main
    Then "b" is named as the conflict

  Scenario: A conflict in a file the hook does not list is still a conflict
    Given main's hook script names file-length.toml, and changes "a" and "b" both edit another file
    When the batch "a", "b" is composed onto main
    Then "b" is named as the conflict

  Scenario: With no hook script on main two rows adding to the list conflict as before
    Given main has no hook script, and changes "a" and "b" each add a different key to file-length.toml
    When the batch "a", "b" is composed onto main
    Then "b" is named as the conflict
