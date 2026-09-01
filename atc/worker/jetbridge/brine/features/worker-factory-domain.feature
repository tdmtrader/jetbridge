Feature: Worker registration and visibility persist through the worker factory

  Source: all 20 specs in atc/db/worker_factory_test.go. The original twelve
  profiles cover insert/update, base resource types, lookup, visibility, and
  complete worker listing; save-types plus get also cover the source's separate
  persisted-resource-types assertion. The five owner profiles below cover the
  remaining seven specs with real build and check-session owners and real
  container rows.

  Scenario Outline: Worker factory profile <profile> produces <result>
    When the real worker factory handles profile "<profile>"
    Then the worker factory result is "<result>"

    Examples:
      | profile         | result                                                                                                           |
      | save-new        | name=some-name;state=running;version=1.0.0                                                                        |
      | save-types      | count=2                                                                                                          |
      | remove-type     | count=1                                                                                                          |
      | replace-image   | changed=true;other-stable=true                                                                                   |
      | replace-version | changed=true;other-stable=true                                                                                   |
      | update-version  | before-nil=true;after=2.0.0                                                                                      |
      | get             | found=true;name=some-name;ephemeral=true;containers=140;volumes=550;platform=some-platform;tags=some,tags;types=2 |
      | missing         | found=false;nil=true                                                                                             |
      | visible         | some-name,team-a-worker                                                                                          |
      | visible-empty   | count=0                                                                                                          |
      | workers         | second,some-name                                                                                                 |
      | workers-empty   | count=0                                                                                                          |
      | owner-check     | first,second,third                                                                                                |
      | owner-creating  | mine=1;other-team=0                                                                                               |
      | owner-created   | mine=1;other-team=0                                                                                               |
      | owner-missing   | count=0                                                                                                           |
      | build-counts    | first=1;second=1;workers=2                                                                                        |
