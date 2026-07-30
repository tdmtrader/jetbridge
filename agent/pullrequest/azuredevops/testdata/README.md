# Azure DevOps REST 7.1 adapter fixtures

These fixtures were derived from the documented Microsoft Azure DevOps REST
7.1 response shapes retrieved on 2026-07-29 and 2026-07-30:

- Pull Requests — Get Pull Request:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request?view=azure-devops-rest-7.1
- Pull Request Iterations — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1
- Pull Request Threads — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1
- Pull Request Reviewers — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/list?view=azure-devops-rest-7.1
- Refs — List and Update Refs:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/list?view=azure-devops-rest-7.1
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/refs/update-refs?view=azure-devops-rest-7.1
- Pull Requests — Get Pull Requests and Create:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-requests?view=azure-devops-rest-7.1
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/create?view=azure-devops-rest-7.1
- Pull Request Statuses — List and Create:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-statuses/list?view=azure-devops-rest-7.1
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-statuses/create?view=azure-devops-rest-7.1
- Pull Request Threads and Comments — Create:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/create?view=azure-devops-rest-7.1
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-thread-comments/create?view=azure-devops-rest-7.1

Azure DevOps adapter: contract-tested against REST 7.1; not live-validated.

The values are synthetic, but the field names, collection envelopes, vote
property bags, iteration contexts, ref-update arrays, and enum spellings
follow the official contracts. Mutation tests additionally pin these
documented details:

- ref list filters use `heads/...`, while returned and updated ref names use
  full `refs/heads/...` names;
- ref creation sends a zero old object ID and `staleOldObjectId` is a failed
  exact lease;
- pull-request collection descriptions are not trusted as complete, so exact
  markers and heads are recovered from a detail read;
- validation statuses carry an exact operation context and iteration ID; and
- summaries are new thread roots, while review replies bind the exact root
  comment inside a sealed `/threads/{threadId}/comments` endpoint. Create
  payloads explicitly select Text comments (`commentType: 1`) and an Active
  summary thread (`status: 1`).

No fixture contains a real organization, repository, identity, or credential.
