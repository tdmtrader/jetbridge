# Example: a queue on one JetBridge pipeline

`sandbox.yaml` is a complete config with placeholder URLs. Edit the
`repository` and `runner` values first, and put a bearer token in the file
named by `runner.credential`.

1. **Create a pipeline whose job tests the candidate.** The resource must
   follow the branch named by `repository.candidate` (`queue-next`), and the
   job must `get` it with `trigger: false` and run your tests:

   ```yaml
   resources:
   - name: candidate
     type: git
     source: {uri: https://git.example.invalid/team/repo.git, branch: queue-next}
   jobs:
   - name: test-candidate
     plan:
     - get: candidate
       trigger: false
     - task: test
       config:
         platform: linux
         image_resource: {type: registry-image, source: {repository: golang}}
         inputs: [{name: candidate}]
         run: {path: sh, args: [-c, "cd candidate && make test"]}
   ```

2. **Run the queue** from any host that can push to the repository:
   `queue run --config sandbox.yaml`. Events print as JSON lines.
3. **Admit a passing change.** From a clone that has the commit:
   `queue admit --config sandbox.yaml add-docs <sha>`. Within a step or two you
   see `batch-started`, `verdict` with `pass`, then `landed`, and main has
   advanced by a fast-forward.
4. **Admit a failing change** the same way, under a new id (`break-tests`).
   The batch goes red and is bisected; a `verdict` of `fail` on the change alone
   is followed by `ejected`. The change is not requeued.
5. **Read status:** `queue status --config sandbox.yaml` prints JSON with
   `Queued`, `InFlight`, `Landed`, `Ejected`, `Paused`, `Refused` and `Flakes`.
   `queue stats` prints the counts and `queue view` the panel's JSON. Here
   `add-docs` is under `Landed` and `break-tests` under `Ejected`.
