Feature: Worker registration and visibility persist through the worker factory

  Source: all 20 specs in atc/db/worker_factory_test.go. Every row uses a real
  PostgreSQL database, the production notification bus, production WorkerCache,
  production WorkerFactory, real container owners, and real container rows.

  Scenario Outline: Worker factory profile <profile> produces <result>
    When the real worker factory handles profile "<profile>"
    Then the worker factory result is "<result>"

    Examples:
      | profile                  | result                                                                                                                                                                                                                 |
      | save-existing-types      | equal=true                                                                                                                                                                                                             |
      | remove-type              | count=1                                                                                                                                                                                                                |
      | replace-image            | before=2;after=2;maps-differ=true;changed=true;other-stable=true                                                                                                                                                        |
      | replace-version          | before=2;after=2;maps-differ=true;changed=true;other-stable=true                                                                                                                                                        |
      | update-version           | before-nil=true;after=1.0.0                                                                                                                                                                                             |
      | save-new                 | name=some-name;state=running;version=1.0.0                                                                                                                                                                              |
      | save-new-types           | count=2                                                                                                                                                                                                                |
      | get                      | found=true;name=some-name;state=running;ephemeral=true;containers=140;volumes=550;types-equal=true;platform=some-platform;tags-equal=true;start=1565367209;version-nil=true                                               |
      | missing                  | found=false;nil=true                                                                                                                                                                                                    |
      | visible                  | count=3;names=some-name,some-new-worker,some-other-new-worker;excluded=true                                                                                                                                             |
      | visible-empty            | count=0                                                                                                                                                                                                                |
      | workers                  | count=2;names=some-name,some-new-worker                                                                                                                                                                                 |
      | workers-empty            | count=0                                                                                                                                                                                                                |
      | owner-check              | some-other-name,some-tagged-name,some-team-name                                                                                                                                                                        |
      | owner-creating-return    | count=1;name=default-worker                                                                                                                                                                                             |
      | owner-creating-other     | count=0                                                                                                                                                                                                                |
      | owner-created-return     | count=1;name=default-worker                                                                                                                                                                                             |
      | owner-created-other      | count=0                                                                                                                                                                                                                |
      | owner-missing            | count=0                                                                                                                                                                                                                |
      | build-counts             | default-worker=1;some-name=1;workers=2                                                                                                                                                                                  |
