Feature: A hook script on main regenerates files before a land
  The repository can keep a hook script on main. The queue reads it from main,
  never from a queued change. It asks the script which files it owns, the test
  job runs it on the candidate, and the queue lands what it regenerated.

  Scenario: Changes that both touch a generated file compose, keeping main's copy for the hook to regenerate
    Given main has a hook script that owns a generated file, and changes "a" and "b" both edit that file
    When the batch "a", "b" is composed onto main
    Then the candidate composes, the hook was only asked which files it owns

  Scenario: The files a hook owns are asked of main's script, never of one a queued change brings
    Given change "b" rewrites the hook script to own a file "a" and "b" both edit
    When the batch "a", "b" is composed onto main
    Then "b" is named as the conflict

  Scenario: A hook script that cannot say what it owns gives no verdict
    Given main has a hook script that exits with status 3
    When the change "a" is composed onto main
    Then composing fails with no verdict

  Scenario: With no hook script on main a land composes as it does without one
    Given the config names a hook script that main does not have
    When two changes that edit the same file are composed onto main
    Then the second is named as the conflict and no hook ran

  Scenario: A hook script is a path in the repository and excludes a hook command
    Given a queue config file whose hook script is absolute or climbs out, or comes with a hook command or owned paths
    When the config file is loaded
    Then loading is refused

  Scenario: A land refreshes the generated boundaries before it reaches main
    Given main has a hook script, and the test job published the hook's commit on the candidate
    When the candidate passes and lands
    Then main is the hook's commit, holding the regenerated file

  Scenario: A candidate whose hook has not run does not land while main has a hook script
    Given main has a hook script and the candidate has no hooked commit
    When the candidate passes and lands
    Then the land is refused, nobody is ejected and main is unchanged

  Scenario: A hooked commit that is not one commit on the candidate does not land
    Given the hooked commit of the candidate is made on main instead
    When the candidate passes and lands
    Then the land is refused, nobody is ejected and main is unchanged

  Scenario: A hooked commit that changes a file the hook does not own does not land
    Given the hooked commit of the candidate writes a file the hook does not own
    When the candidate passes and lands
    Then the land is refused, nobody is ejected and main is unchanged

  Scenario: With no hook script on main the candidate lands as it is
    Given the config names a hook script that main does not have
    When the candidate passes and lands
    Then main is the candidate
