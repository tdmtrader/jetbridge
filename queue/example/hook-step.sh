#!/usr/bin/env bash
# hook-step.sh: the test job's hook step, run before the tests on the candidate.
#
# Inputs, under the working directory:
#   candidate/  the queue resource's get of the candidate, with its history (HEAD = the candidate)
#   main/       a checkout of main, the candidate's base
# Env: HOOK_SCRIPT (compose.hook_script), HOOK_INPUTS (read-only inputs dir,
# passed to the hook as JBQ_HOOK_INPUTS), HOOK_TIMEOUT (seconds, default 900).
# Output, hook/: hook.bundle (one commit on the candidate, as refs/heads/hooked),
# candidate and commit (the two shas). Push refs/heads/hooked from the bundle to
# refs/mq/hooked/<candidate> with the verdict. candidate/ is left at that commit
# so the tests that follow test it.
#
# Exit 0: done, or no hook script on main (hook/ stays empty). Exit 1: the hook
# refused, or changed a file it does not own: a red test. Exit 75: the hook
# failed for now (75), timed out, or the inputs are wrong: no verdict.
set -uo pipefail
wd="$PWD" c="$PWD/candidate" out="$PWD/hook"
script="${HOOK_SCRIPT:?}" limit="${HOOK_TIMEOUT:-900}"
retry() { echo "hook-step: no verdict: $1" >&2; exit 75; }
refuse() { echo "hook-step: refused: $1" >&2; exit 1; }
export GIT_AUTHOR_NAME=merge-queue GIT_AUTHOR_EMAIL=merge-queue@localhost GIT_COMMITTER_NAME=merge-queue GIT_COMMITTER_EMAIL=merge-queue@localhost
mkdir -p "$out" || retry "no output dir"
cand="$(git -C "$c" rev-parse --verify 'HEAD^{commit}')" || retry "no candidate"
base="$(git -C "$wd/main" rev-parse --verify 'HEAD^{commit}')" || retry "no main"
git -C "$c" merge-base --is-ancestor "$base" "$cand" || retry "the candidate is not on main ${base:0:12}"
ent="$(git -C "$c" ls-tree "$base" -- "$script")"
[ -n "$ent" ] || { echo "hook-step: no hook script on main: nothing to run"; exit 0; }
[[ "$ent" =~ ^100(644|755)\ blob\  ]] || retry "the hook script on main is not a regular file"
h="$(mktemp -d "${TMPDIR:-/tmp}/hook-step.XXXXXX")" && mkdir "$h/empty" || retry "no scratch dir"
git -C "$c" cat-file blob "$base:$script" > "$h/hook" && chmod 0755 "$h/hook" || retry "cannot read the hook script from main"
owned="$(cd "$h/empty" && timeout -k 10 "$limit" "$h/hook" owned)" || retry "the hook cannot say which files it owns"
( cd "$c" && JBQ_HOOK_BASE="$base" JBQ_HOOK_HEAD="$cand" JBQ_HOOK_INPUTS="${HOOK_INPUTS:-}" timeout -k 10 "$limit" "$h/hook" run ) < /dev/null
rc=$?
case "$rc" in
  0) ;;
  75|124|137) retry "the hook exited $rc" ;;
  *) refuse "the hook exited $rc" ;;
esac
# .mq/ is the get's run details, never the hook's
git -C "$c" reset -q --soft "$cand" && git -C "$c" add -A && git -C "$c" rm -r -q --cached --ignore-unmatch -- .mq || retry "cannot read what the hook changed"
changed="$(git -C "$c" diff --cached --name-only --no-renames "$cand")" || retry "cannot read what the hook changed"
while IFS= read -r p; do
  [ -z "$p" ] || grep -qxF -- "$p" <<< "$owned" || refuse "the hook changed a file it does not own"
done <<< "$changed"
tree="$(git -C "$c" write-tree)" && hooked="$(git -C "$c" commit-tree "$tree" -p "$cand" -m "compose hook: regenerate files")" || retry "cannot commit the hook's files"
git -C "$c" update-ref refs/heads/hooked "$hooked" && git -C "$c" bundle create -q "$out/hook.bundle" "$cand..refs/heads/hooked" || retry "cannot write the bundle"
git -C "$c" reset -q --soft "$hooked" || retry "cannot move the candidate checkout"
printf '%s\n' "$cand" > "$out/candidate" && printf '%s\n' "$hooked" > "$out/commit" || retry "cannot write the shas"
echo "hook-step: the hook's commit $hooked on the candidate $cand"
