Feature: Persisted resource identity and version history

  Source: exact real-PostgreSQL leaves from atc/db/resource_test.go. Each
  scenario uses production team, pipeline, resource, scope, and version paths.

  Scenario Outline: Resource database profile <profile> has observation <observation>
    When the production resource database evaluates profile "<profile>"
    Then the resource database observation is "<observation>"

    Examples:
      | profile                  | observation                                                                     |
      | resource-missing        | found=false;nil=true                                                            |
      | public-default          | public=false                                                                    |
      | public-true             | public=true                                                                     |
      | public-false            | public=false                                                                    |
      | filter-one-match        | error=false;found=true;count=1;refs=v2                                           |
      | filter-one-miss         | error=false;found=true;count=0;refs=                                             |
      | filter-two-match        | error=false;found=true;count=1;refs=v1                                           |
      | filter-two-miss         | error=false;found=true;count=0;refs=                                             |
      | page-first              | error=false;found=true;refs=v9,v8;newer=nil;older=v7/2                          |
      | page-to-middle          | error=false;found=true;refs=v6,v5;newer=v7/2;older=v4/2                         |
      | page-to-oldest          | error=false;found=true;refs=v1,v0;newer=v2/2;older=nil                          |
      | page-from-middle        | error=false;found=true;refs=v7,v6;newer=v8/2;older=v5/2                         |
      | metadata-returned       | error=false;found=true;count=1;ref=v9;metadata=name1:value1                     |
      | metadata-maintained     | error=false;found=true;count=1;ref=v9;metadata=name1:value1                     |
      | metadata-updated        | error=false;found=true;count=1;ref=v9;metadata=name-new:value-new               |
      | disabled-returned       | error=false;found=true;count=1;ref=v9;enabled=false                             |
      | metadata-update-visible | error=false;found=true;count=1;ref=v9;metadata=name1:value1                     |
      | check-order             | error=false;found=true;refs=v4,v3,v2,v1;newer=nil;older=nil                    |
      | check-order-from        | error=false;found=true;refs=v3,v2;newer=v4/2;older=v1/2                         |
      | check-order-to          | error=false;found=true;refs=v3,v2;newer=v4/2;older=v1/2                         |
