Feature: The volume repository persists and reconciles real PostgreSQL rows

  Source: assertion-bearing production behaviors in atc/db/volume_repository_test.go.
  Every scenario uses the production repository and factories against a fresh database.

  Scenario Outline: Volume repository profile <profile> has its exact result
    Given the real volume repository evaluates profile "<profile>"
    Then the volume repository observation is "<expected>"

    Examples:
      | profile                          | expected                                           |
      | team/task-cache                  | count=1;matches=true                               |
      | team/scoped                      | first=team-first-1,team-first-2;second=team-second-1 |
      | team/expired                     | first=team-first-1,team-first-2;second=team-second-1 |
      | orphan/exact                     | handles=child,orphan                               |
      | orphan/child                     | parent=false;child=true                            |
      | failed/count                     | count=1                                            |
      | destroying/list                  | handles=destroying-volume                          |
      | create/generic                   | team=true;worker=true                              |
      | find/base-created                | creating=false;created=true;handle=true            |
      | find/base-creating               | creating=true;created=false;handle=true            |
      | find/resource-cache              | found=true;handle=true                             |
      | remove-destroying/delete         | exists=false                                       |
      | remove-destroying/count          | count=1                                            |
      | remove-destroying/empty-delete   | exists=false                                       |
      | remove-destroying/empty-count    | count=1                                            |
      | remove-destroying/creating-delete | exists=true                                       |
      | remove-destroying/creating-count | count=0                                            |
      | remove-destroying/reported-delete | exists=true                                       |
      | remove-destroying/reported-count | count=0                                            |
      | remove-missing/no-expired        | count=0                                            |
      | remove-missing/expired-count     | count=1                                            |
      | remove-missing/expired-right     | handles=live,old-destroying,recent                 |
      | remove-missing/parent-count      | count=2                                            |
      | remove-missing/parent-right      | handles=alive                                      |
      | update-missing/creating          | three=false                                        |
      | update-missing/subset            | one=false;two=true;three-old=true                  |
      | update-missing/full              | one=false;two=false                                |
      | update-missing/clear             | one=false;two=false;three=false                    |
      | unknown/add                      | count=2;destroying=four,one,three                  |
      | unknown/preserve                 | count=2;created=true                               |
      | unknown/noop                     | count=0;destroying=one                             |
