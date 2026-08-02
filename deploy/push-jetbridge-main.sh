#!/bin/sh
set -eu
repo=${1-.}
git -C "$repo" rev-parse --is-inside-work-tree | grep -Fx true >/dev/null
git -C "$repo" fetch origin main
remote_main=$(git -C "$repo" rev-parse FETCH_HEAD)
git -C "$repo" merge-base --is-ancestor "$remote_main" HEAD || {
  echo 'FATAL: origin/main is not an ancestor of the release commit' >&2
  exit 1
}
git -C "$repo" push origin HEAD:refs/heads/main
