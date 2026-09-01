Feature: Config API behavior through production HTTP and PostgreSQL

  Current source inventory: 104 leaves in atc/api/config_test.go. These 73 rows
  cover behaviors observable through a real TCP listener, production rata routes,
  production auth/access verification, PostgreSQL teams/pipelines, and production
  credential managers. Injected database failures, scanner call counts, and
  call-only negative assertions remain in Go and are not claimed.

  Scenario Outline: API config strict <case>
    Given the strict production config API executes profile "<profile>"
    Then the strict config API "<kind>" observation is "<expected>"

    Examples:
      | case | profile | kind | expected |
      | source-1 | get/malformed-instance-vars | status | 400 |
      | source-2 | get/malformed-instance-vars | content-type | application/json |
      | source-3 | get/malformed-instance-vars | body-sha256 | b689bcd3293c72972ebb8e8adeb43bc2d8d9e834f43178f8bbfe148dd3767f9a |
      | source-6 | get/success | status | 200 |
      | source-7 | get/success | headers | application/json;version=1 |
      | source-8 | get/success | body-sha256 | 182e0c437caa0431d291a2ceffc14fcfea60700656208221cc490bc1cbee928d |
      | source-10 | get/archived | status | 404 |
      | source-11 | get/pipeline-missing | status | 404 |
      | source-13 | get/team-missing | status | 404 |
      | source-15 | get/unauthenticated | status | 401 |
      | source-16 | put/invalid-identifier | full-sha256 | 37c1f6646c7f702272dd862c32f6dd55a60d44538bd84a8cc2055c648f7a65e5 |
      | source-17 | put/empty-identifier | full-sha256 | b978ddd2454e2bd2d3ab1788cd76dc0a83c7305f90ee6414ec858d2a1164f73b |
      | source-18 | put/malformed-json | status | 400 |
      | source-19 | put/malformed-json | content-type | application/json |
      | source-20 | put/malformed-json | body-sha256 | a71bfc75849ff1f020d5ddf174f5ed5851fe92880b18d8fd4e913145960b6e4e |
      | source-22 | put/malformed-yaml | status | 400 |
      | source-23 | put/malformed-yaml | content-type | application/json |
      | source-24 | put/malformed-yaml | body-sha256 | a71bfc75849ff1f020d5ddf174f5ed5851fe92880b18d8fd4e913145960b6e4e |
      | source-26 | put/valid-json | status | 200 |
      | source-28 | put/valid-json | content-type | application/json |
      | source-32 | put/create-json | status | 201 |
      | source-34 | put/invalid-config | status | 400 |
      | source-35 | put/invalid-config | content-type | application/json |
      | source-36 | put/invalid-config | body-sha256 | aeb378f275bf02aa3a7f4aa979080a2a13921109fdd653dc3414a5b3d43d365f |
      | source-38 | put/valid-yaml | status | 200 |
      | source-40 | put/valid-yaml | content-type | application/json |
      | source-42 | put/suspicious-yaml | status | 200 |
      | source-43 | put/suspicious-yaml | content-type | application/json |
      | source-44 | put/suspicious-yaml | pipeline-sha256 | 34aac065570fc340afc03c7405e9b6e602ae2096977f87d3654f4b9d732144f4 |
      | source-45 | put/creds/resource-type/credential-present | pipeline-sha256 | cfb6bd820de5879548d6266d07561be7ac55198943ee3fffdf715ab7f13e645f |
      | source-46 | put/creds/resource-type/credential-present | status | 200 |
      | source-48 | put/creds/resource-type/credential-missing | status | 400 |
      | source-49 | put/creds/resource-source/credential-present | pipeline-sha256 | 81d6269f1ac902b49099ed9d41bac8b296013cd2b95b3e2706078b4cc9c8bf00 |
      | source-50 | put/creds/resource-source/credential-present | status | 200 |
      | source-52 | put/creds/resource-source/credential-missing | status | 400 |
      | source-53 | put/creds/webhook-token/credential-present | pipeline-sha256 | f43ba57ecdccb47d70fd7348a7f7016c6775778a0c9504b3cf74393bdc755634 |
      | source-54 | put/creds/webhook-token/credential-present | status | 200 |
      | source-56 | put/creds/webhook-token/credential-missing | status | 400 |
      | source-57 | put/creds/task-params/credential-present | pipeline-sha256 | 6cd30385fe8154b8695908bcd2b803187b71942f61fb2ce6353ed33582ef76df |
      | source-58 | put/creds/task-params/credential-present | status | 200 |
      | source-60 | put/creds/task-params/credential-missing | status | 400 |
      | source-61 | put/creds/task-vars/credential-present | pipeline-sha256 | ad9d1128e5ab32dc7c01b42acf04e3f8726c4d1f86c5b99de1aa1ce1ba646871 |
      | source-62 | put/creds/task-vars/credential-present | status | 200 |
      | source-64 | put/creds/task-vars/credential-missing | status | 400 |
      | source-65 | put/creds/nested-task-vars/credential-present | pipeline-sha256 | c680f749ade5e9f119d1922dda5648e5c97545bd489ef58330b37b4508c02ba4 |
      | source-66 | put/creds/nested-task-vars/credential-present | status | 200 |
      | source-68 | put/creds/nested-task-vars/credential-missing | status | 400 |
      | source-69 | put/inline-creds/credential-present | pipeline-sha256 | 07b714230e00ca2a844acb4fb05d5681f1e31c0c8b4ce4dc743a7bb059d13cb5 |
      | source-70 | put/inline-creds/credential-present | status | 200 |
      | source-71 | put/inline-creds/credential-missing | status | 400 |
      | source-72 | put/inline-creds/credential-missing | body-sha256 | c508aed15a833f2a403fc5a92d8b9705f914e925a35fb1371eabfd4b171614d1 |
      | source-73 | put/inline-creds/no-manager | status | 400 |
      | source-74 | put/inline-creds/no-manager | body-sha256 | c508aed15a833f2a403fc5a92d8b9705f914e925a35fb1371eabfd4b171614d1 |
      | source-75 | put/create-yaml | status | 201 |
      | source-79 | put/invalid-config | status | 400 |
      | source-80 | put/invalid-config | content-type | application/json |
      | source-81 | put/invalid-config | body-sha256 | aeb378f275bf02aa3a7f4aa979080a2a13921109fdd653dc3414a5b3d43d365f |
      | source-83 | put/malformed-instance-vars | status | 400 |
      | source-84 | put/malformed-instance-vars | content-type | application/json |
      | source-85 | put/malformed-instance-vars | body-sha256 | b689bcd3293c72972ebb8e8adeb43bc2d8d9e834f43178f8bbfe148dd3767f9a |
      | source-87 | put/valid-instance-vars | pipeline-sha256 | f1c1a721ae0751b534febcd9fad2aaeaee1e53fa8446cd5495aa4b7352482154 |
      | source-88 | put/team-missing | status | 404 |
      | source-90 | put/unsupported-content | status | 415 |
      | source-92 | put/top-level-extra | status | 200 |
      | source-93 | put/top-level-extra | content-type | application/json |
      | source-94 | put/top-level-extra | pipeline-sha256 | a2bd8444afb55b4746065d64d26f414b5d28fd4ed87457a187d355896ad6fecf |
      | source-95 | put/nested-extra | status | 400 |
      | source-96 | put/nested-extra | content-type | application/json |
      | source-97 | put/nested-extra | body-sha256 | a82d9814c9da61e80525fa655c7ecf74c41faf9b7aa593a24305062f4e1fd0a9 |
      | source-99 | put/malformed-version | status | 400 |
      | source-100 | put/malformed-version | content-type | application/json |
      | source-101 | put/malformed-version | body-sha256 | fe2da663aed37e2ed88250fd89ed6b9b7d420165cf54d1f2ecd331ced2a29e74 |
      | source-103 | put/unauthenticated | status | 401 |
