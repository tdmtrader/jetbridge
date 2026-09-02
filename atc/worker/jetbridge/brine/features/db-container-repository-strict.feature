Feature: Container repository production persistence

  Source: exact real-PostgreSQL leaves from atc/db/container_repository_test.go.

  Scenario Outline: Container repository profile <profile> has observation <observation>
    When the production container repository evaluates profile "<profile>"
    Then the container repository observation is "<observation>"

    Examples:
      | profile                    | observation                                             |
      | failed-count               | affected=1;failed=0;destroying=1;error=false             |
      | destroying-list           | handles=destroying-1;error=false                         |
      | missing-none              | affected=0;remaining=4;error=false                       |
      | missing-expired           | affected=1;expired=false;other=true;error=false           |
      | missing-running-count     | affected=1;error=false                                   |
      | missing-stalled-preserved | affected=1;running=false;stalled=true;error=false         |
      | remove-destroying-gone    | affected=1;exists=false;error=false                       |
      | remove-destroying-count   | affected=1;error=false                                    |
      | remove-empty-gone         | affected=1;exists=false;error=false                       |
      | remove-empty-count        | affected=1;error=false                                    |
      | remove-creating-stays     | affected=0;exists=true;error=false                        |
      | remove-creating-count     | affected=0;error=false                                    |
      | remove-ignored-stay       | affected=0;remaining=2;error=false                        |
      | remove-ignored-count      | affected=0;error=false                                    |
      | update-creating-unmarked  | h3-missing=false;error=false                              |
      | update-subset-marks       | h1=false;h2=true;h3=true;error=false                      |
      | update-full-unchanged     | h1=false;h2=false;error=false                             |
      | update-reported-clears    | h1=false;h2=false;h3=false;error=false                    |
      | unknown-adds              | affected=2;destroying=3;error=false                       |
      | unknown-noop              | affected=0;destroying=1;error=false                       |
      | excess-cap                | affected=2;states=destroying,destroying,created,created;error=false |
      | excess-hijack             | affected=1;states=created,destroying,created,created;error=false    |
      | excess-partition          | affected=2;a=destroying,created,created;b=destroying,created,created;error=false |
      | excess-small              | affected=0;states=created,created;error=false              |
