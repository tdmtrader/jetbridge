# Azure DevOps REST 7.1 observation fixtures

These fixtures were derived from the documented Microsoft Azure DevOps REST
7.1 response shapes retrieved on 2026-07-29:

- Pull Requests — Get Pull Request:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request?view=azure-devops-rest-7.1
- Pull Request Iterations — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-iterations/list?view=azure-devops-rest-7.1
- Pull Request Threads — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-threads/list?view=azure-devops-rest-7.1
- Pull Request Reviewers — List:
  https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-request-reviewers/list?view=azure-devops-rest-7.1

Azure DevOps adapter: contract-tested against REST 7.1; not live-validated.

The values are synthetic, but the field names, collection envelopes, vote
property bags, iteration contexts, and enum spellings follow the official
contracts. No fixture contains a real organization, repository, identity, or
credential.
