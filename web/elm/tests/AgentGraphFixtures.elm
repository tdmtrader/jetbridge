module AgentGraphFixtures exposing
    ( all
    , anonymizationAuditV3
    , codeReviewV3
    , logDiagnosisV3
    , measureReviewV3
    , mergeDeliveryV3
    , smallFixV3
    , versionUpgradeV3
    )

{-| The seven real serialized graphs the server produces for the shipped v3
seed workflows, copied verbatim from `agent/workflow/graph/testdata/*.json`.

These are the actual wire format, not a hand-written approximation of it. A
decoder that only ever sees synthetic fixtures agrees with the fixture author
rather than with the server, so every decoder assertion in this suite is made
against bytes the Go golden test also asserts on.
-}


all : List ( String, String )
all =
    [ ( "anonymization-audit-v3.json", anonymizationAuditV3 )
    , ( "code-review-v3.json", codeReviewV3 )
    , ( "log-diagnosis-v3.json", logDiagnosisV3 )
    , ( "measure-review-v3.json", measureReviewV3 )
    , ( "merge-delivery-v3.json", mergeDeliveryV3 )
    , ( "small-fix-v3.json", smallFixV3 )
    , ( "version-upgrade-v3.json", versionUpgradeV3 )
    ]


anonymizationAuditV3 : String
anonymizationAuditV3 =
    """
{
  "nodes": [
    {
      "id": "input:repository",
      "kind": "input",
      "display_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "id": "input:database",
      "kind": "input",
      "display_name": "database",
      "type_ref": "database-snapshot/v1"
    },
    {
      "id": "audit-anonymization",
      "kind": "agent",
      "display_name": "audit"
    },
    {
      "id": "output:findings",
      "kind": "output",
      "display_name": "findings",
      "type_ref": "audit-findings/v1"
    },
    {
      "id": "output:change",
      "kind": "output",
      "display_name": "change",
      "type_ref": "repository-change/v1",
      "optional": true
    }
  ],
  "edges": [
    {
      "from": "audit-anonymization",
      "to": "output:change",
      "port_name": "change",
      "type_ref": "repository-change/v1",
      "optional": true
    },
    {
      "from": "audit-anonymization",
      "to": "output:findings",
      "port_name": "findings",
      "type_ref": "audit-findings/v1"
    },
    {
      "from": "input:database",
      "to": "audit-anonymization",
      "port_name": "database",
      "type_ref": "database-snapshot/v1"
    },
    {
      "from": "input:repository",
      "to": "audit-anonymization",
      "port_name": "repository",
      "type_ref": "repository/v1"
    }
  ]
}
"""


codeReviewV3 : String
codeReviewV3 =
    """
{
  "nodes": [
    {
      "id": "input:before",
      "kind": "input",
      "display_name": "before",
      "type_ref": "repository/v1"
    },
    {
      "id": "input:after",
      "kind": "input",
      "display_name": "after",
      "type_ref": "repository/v1"
    },
    {
      "id": "review",
      "kind": "agent",
      "display_name": "review"
    },
    {
      "id": "output:review",
      "kind": "output",
      "display_name": "review",
      "type_ref": "review/v1"
    }
  ],
  "edges": [
    {
      "from": "input:after",
      "to": "review",
      "port_name": "after",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:before",
      "to": "review",
      "port_name": "before",
      "type_ref": "repository/v1"
    },
    {
      "from": "review",
      "to": "output:review",
      "port_name": "review",
      "type_ref": "review/v1"
    }
  ]
}
"""


logDiagnosisV3 : String
logDiagnosisV3 =
    """
{
  "nodes": [
    {
      "id": "input:logs",
      "kind": "input",
      "display_name": "logs",
      "type_ref": "log-bundle/v1"
    },
    {
      "id": "input:deployment",
      "kind": "input",
      "display_name": "deployment",
      "type_ref": "deployment-snapshot/v1",
      "optional": true
    },
    {
      "id": "diagnose-logs",
      "kind": "agent",
      "display_name": "diagnose"
    },
    {
      "id": "output:diagnosis",
      "kind": "output",
      "display_name": "diagnosis",
      "type_ref": "diagnosis/v1"
    }
  ],
  "edges": [
    {
      "from": "diagnose-logs",
      "to": "output:diagnosis",
      "port_name": "diagnosis",
      "type_ref": "diagnosis/v1"
    },
    {
      "from": "input:deployment",
      "to": "diagnose-logs",
      "port_name": "deployment",
      "type_ref": "deployment-snapshot/v1",
      "optional": true
    },
    {
      "from": "input:logs",
      "to": "diagnose-logs",
      "port_name": "logs",
      "type_ref": "log-bundle/v1"
    }
  ]
}
"""


measureReviewV3 : String
measureReviewV3 =
    """
{
  "nodes": [
    {
      "id": "input:candidate",
      "kind": "input",
      "display_name": "candidate",
      "type_ref": "review/v1"
    },
    {
      "id": "measure-review",
      "kind": "task",
      "display_name": "measure-review"
    },
    {
      "id": "output:measurements",
      "kind": "output",
      "display_name": "measurements",
      "type_ref": "measurements/v1"
    }
  ],
  "edges": [
    {
      "from": "input:candidate",
      "to": "measure-review",
      "port_name": "candidate",
      "type_ref": "review/v1"
    },
    {
      "from": "measure-review",
      "to": "output:measurements",
      "port_name": "measurements",
      "type_ref": "measurements/v1"
    }
  ]
}
"""


mergeDeliveryV3 : String
mergeDeliveryV3 =
    """
{
  "nodes": [
    {
      "id": "input:base",
      "kind": "input",
      "display_name": "base",
      "type_ref": "repository/v1"
    },
    {
      "id": "input:candidate",
      "kind": "input",
      "display_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "id": "input:target",
      "kind": "input",
      "display_name": "target",
      "type_ref": "repository/v1"
    },
    {
      "id": "merge-preflight",
      "kind": "task",
      "display_name": "merge-preflight"
    },
    {
      "id": "merge-prepare",
      "kind": "task",
      "display_name": "merge-prepare"
    },
    {
      "id": "dev-validation-merged-change-gates",
      "kind": "task",
      "display_name": "validate-merged-change"
    },
    {
      "id": "merge-approval",
      "kind": "await",
      "display_name": "merge-approval",
      "type_ref": "human-answer/v1",
      "decorations": [
        "timeout"
      ]
    },
    {
      "id": "land-merge",
      "kind": "publish",
      "display_name": "land-merge",
      "type_ref": "repository-change/v1"
    },
    {
      "id": "output:merged-change",
      "kind": "output",
      "display_name": "merged-change",
      "type_ref": "repository-change/v1"
    },
    {
      "id": "output:merge-report",
      "kind": "output",
      "display_name": "merge-report",
      "type_ref": "validation/v1"
    }
  ],
  "edges": [
    {
      "from": "dev-validation-merged-change-gates",
      "to": "land-merge",
      "port_name": "validation",
      "type_ref": "validation/v1"
    },
    {
      "from": "dev-validation-merged-change-gates",
      "to": "merge-approval",
      "port_name": "validation",
      "type_ref": "validation/v1"
    },
    {
      "from": "input:base",
      "to": "merge-preflight",
      "port_name": "base",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:base",
      "to": "merge-prepare",
      "port_name": "base",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:candidate",
      "to": "merge-preflight",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "input:candidate",
      "to": "merge-prepare",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "input:target",
      "to": "dev-validation-merged-change-gates",
      "port_name": "target",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:target",
      "to": "merge-preflight",
      "port_name": "target",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:target",
      "to": "merge-prepare",
      "port_name": "target",
      "type_ref": "repository/v1"
    },
    {
      "from": "merge-approval",
      "to": "land-merge",
      "port_name": "merge-approval",
      "type_ref": "human-answer/v1"
    },
    {
      "from": "merge-preflight",
      "to": "output:merge-report",
      "port_name": "merge-report",
      "type_ref": "validation/v1"
    },
    {
      "from": "merge-prepare",
      "to": "dev-validation-merged-change-gates",
      "port_name": "merged-change",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "merge-prepare",
      "to": "land-merge",
      "port_name": "merged-change",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "merge-prepare",
      "to": "merge-approval",
      "port_name": "merged-change",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "merge-prepare",
      "to": "output:merged-change",
      "port_name": "merged-change",
      "type_ref": "repository-change/v1"
    }
  ]
}
"""


smallFixV3 : String
smallFixV3 =
    """
{
  "nodes": [
    {
      "id": "input:repository",
      "kind": "input",
      "display_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "id": "input:work-item",
      "kind": "input",
      "display_name": "work-item",
      "type_ref": "work-item/v1"
    },
    {
      "id": "implement",
      "kind": "agent",
      "display_name": "implement"
    },
    {
      "id": "review",
      "kind": "agent",
      "display_name": "review"
    },
    {
      "id": "dev-validation-repository-gates",
      "kind": "task",
      "display_name": "validate"
    },
    {
      "id": "prepare-question",
      "kind": "agent",
      "display_name": "prepare-question"
    },
    {
      "id": "approval",
      "kind": "await",
      "display_name": "approval",
      "type_ref": "human-answer/v1",
      "decorations": [
        "timeout"
      ]
    },
    {
      "id": "output:change",
      "kind": "output",
      "display_name": "change",
      "type_ref": "repository-change/v1"
    },
    {
      "id": "output:report",
      "kind": "output",
      "display_name": "report",
      "type_ref": "opaque/v1"
    }
  ],
  "edges": [
    {
      "from": "dev-validation-repository-gates",
      "to": "prepare-question",
      "port_name": "validation",
      "type_ref": "validation/v1"
    },
    {
      "from": "implement",
      "to": "review",
      "port_name": "draft",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "input:repository",
      "to": "dev-validation-repository-gates",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:repository",
      "to": "implement",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:repository",
      "to": "review",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:work-item",
      "to": "implement",
      "port_name": "work-item",
      "type_ref": "work-item/v1"
    },
    {
      "from": "input:work-item",
      "to": "review",
      "port_name": "work-item",
      "type_ref": "work-item/v1"
    },
    {
      "from": "prepare-question",
      "to": "approval",
      "port_name": "approval-question",
      "type_ref": "question/v1"
    },
    {
      "from": "review",
      "to": "dev-validation-repository-gates",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "review",
      "to": "output:change",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "review",
      "to": "output:report",
      "port_name": "report",
      "type_ref": "opaque/v1"
    },
    {
      "from": "review",
      "to": "prepare-question",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    }
  ]
}
"""


versionUpgradeV3 : String
versionUpgradeV3 =
    """
{
  "nodes": [
    {
      "id": "input:repository",
      "kind": "input",
      "display_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "id": "input:request",
      "kind": "input",
      "display_name": "request",
      "type_ref": "upgrade-request/v1"
    },
    {
      "id": "upgrade",
      "kind": "agent",
      "display_name": "upgrade"
    },
    {
      "id": "review",
      "kind": "agent",
      "display_name": "review"
    },
    {
      "id": "dev-validation-repository-gates",
      "kind": "task",
      "display_name": "validate"
    },
    {
      "id": "prepare-question",
      "kind": "agent",
      "display_name": "prepare-question"
    },
    {
      "id": "approval",
      "kind": "await",
      "display_name": "approval",
      "type_ref": "human-answer/v1",
      "decorations": [
        "timeout"
      ]
    },
    {
      "id": "output:change",
      "kind": "output",
      "display_name": "change",
      "type_ref": "repository-change/v1"
    },
    {
      "id": "output:report",
      "kind": "output",
      "display_name": "report",
      "type_ref": "upgrade-report/v1"
    }
  ],
  "edges": [
    {
      "from": "dev-validation-repository-gates",
      "to": "prepare-question",
      "port_name": "validation",
      "type_ref": "validation/v1"
    },
    {
      "from": "input:repository",
      "to": "dev-validation-repository-gates",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:repository",
      "to": "review",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:repository",
      "to": "upgrade",
      "port_name": "repository",
      "type_ref": "repository/v1"
    },
    {
      "from": "input:request",
      "to": "review",
      "port_name": "request",
      "type_ref": "upgrade-request/v1"
    },
    {
      "from": "input:request",
      "to": "upgrade",
      "port_name": "request",
      "type_ref": "upgrade-request/v1"
    },
    {
      "from": "prepare-question",
      "to": "approval",
      "port_name": "approval-question",
      "type_ref": "question/v1"
    },
    {
      "from": "review",
      "to": "dev-validation-repository-gates",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "review",
      "to": "output:change",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "review",
      "to": "prepare-question",
      "port_name": "candidate",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "upgrade",
      "to": "output:report",
      "port_name": "report",
      "type_ref": "upgrade-report/v1"
    },
    {
      "from": "upgrade",
      "to": "review",
      "port_name": "draft",
      "type_ref": "repository-change/v1"
    },
    {
      "from": "upgrade",
      "to": "review",
      "port_name": "report",
      "type_ref": "upgrade-report/v1"
    }
  ]
}
"""
