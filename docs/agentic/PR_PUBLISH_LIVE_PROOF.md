# Provider-Native Pull Request Live Proof

> **Current result:** not run. The required GitHub environment variables were
> absent during this implementation session, and the environment-gated live
> test has not yet been added. This document is the required proof protocol,
> not evidence that the matrix passed.

## Scope

The provider-neutral contract is exercised by the shared GitHub
fixture/conformance suites. A future environment-gated test must perform the
external proof only inside an explicitly authorized GitHub repository.

GitHub is the only supported forge. The provider-neutral `Observer`/`Mutator`
seam is retained, but no second adapter ships.

## GitHub prerequisites

The live test remains skipped unless all three variables are present:

```text
JETBRIDGE_GITHUB_PR_TEST_REPOSITORY
JETBRIDGE_GITHUB_PR_TEST_READ_TOKEN
JETBRIDGE_GITHUB_PR_TEST_WRITE_TOKEN
```

`JETBRIDGE_GITHUB_PR_TEST_REPOSITORY` must identify an existing, dedicated
repository in `owner/name` form. The test does not create repositories or
broaden token permissions. The read and write tokens must be distinct.

The repository owner must authorize creation and cleanup of branches whose
names begin with the test's printed Jetbridge prefix and creation/closure of
the corresponding pull request. Cleanup is limited to those recorded
identifiers in that repository. A failed cleanup is reported for operator
action rather than retried against a broader target.

## Required proof matrix

One live run must record:

- exact source-branch create and idempotent retry;
- provider-native PR create and operation-marker recovery;
- suppression while review feedback remains pending;
- one submitted/completed review batch;
- adoption of an external commit without overwriting it;
- rejection of a stale source lease;
- target-head refresh and full validation boundary;
- conflict transition when the authorized fixture can create one;
- exact validation status recovery;
- conditional reapproval authority;
- idempotent summary and thread replies; and
- forge-native terminal observation without a Jetbridge completion call.

The test prints only safe repository, branch, PR, operation, and commit
identifiers. It must never print token bytes or credential-bearing URLs.

## Implementation run

When the live test is added, it must run only if the prerequisites are already
available to the test process. Otherwise it must report each missing variable
and skip once; fixture and conformance suites remain the only recorded
evidence.

Record the eventual authorized run below without including credentials:

```text
Date:
GitHub repository:
Test commit:
Created source branch:
Created pull request:
Matrix result:
Cleanup result:
Operator:
```
