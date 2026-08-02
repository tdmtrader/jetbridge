#!/bin/sh
set -eu
set +x

image=${1-}
source_commit=${2-}
runner_version=${3-}
repo=${4-home-infra}
file=apps/concourse.yaml
# The deployed reference must name a registry the cluster can pull. GHCR is
# where the image is verified, but that package is private and neither the
# web pod nor the ServiceAccount ATC-created agent pods inherit holds a
# credential for it, so a ghcr.io value here puts every agent pod into
# ErrImagePull. The pipeline proves both registries carry the same digest
# before calling this.
image_re='^registry.home/agent-runner@sha256:[a-f0-9]{64}$'
source_re='^[a-f0-9]{40}$'
version_re='^[0-9]+\.[0-9]+\.[0-9]+$'
printf '%s\n' "$image" | grep -Eq "$image_re" || {
  echo 'FATAL: image must be an immutable registry.home/agent-runner sha256 digest' >&2
  exit 1
}
printf '%s\n' "$source_commit" | grep -Eq "$source_re" || { echo 'FATAL: source commit must be 40 lowercase hex' >&2; exit 1; }
printf '%s\n' "$runner_version" | grep -Eq "$version_re" || { echo 'FATAL: runner version must be major.minor.patch' >&2; exit 1; }
git -C "$repo" rev-parse --is-inside-work-tree | grep -Fx true >/dev/null
test -f "$repo/$file"
test -z "$(git -C "$repo" status --porcelain)"

target_re='^[[:space:]]*-[[:space:]]*\{[[:space:]]*name:[[:space:]]*CONCOURSE_AGENT_STEP_IMAGE,[[:space:]]*value:[[:space:]]*"[^"]*"[[:space:]]*\}[[:space:]]*$'
matches=$(grep -Ec "$target_re" "$repo/$file" || true)
test "$matches" = 1 || { echo "FATAL: target mapping count is $matches, want 1" >&2; exit 1; }
old_line=$(grep -E "$target_re" "$repo/$file")
indent=$(printf '%s\n' "$old_line" | sed 's/^\([[:space:]]*\).*/\1/')
new_line="${indent}- { name: CONCOURSE_AGENT_STEP_IMAGE, value: \"${image}\" }"
test "$old_line" = "$new_line" && exit 0

tmp=$(mktemp "$repo/.agent-runner-image.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
awk -v old="$old_line" -v new="$new_line" '{ if ($0 == old) print new; else print }' "$repo/$file" > "$tmp"
mv "$tmp" "$repo/$file"
test "$(git -C "$repo" diff --name-only)" = "$file"
test "$(git -C "$repo" diff --numstat -- "$file" | awk '{print $1 ":" $2}')" = '1:1'
git -C "$repo" add -- "$file"
git -C "$repo" diff --cached --quiet && exit 0
git -C "$repo" -c user.name='Concourse GitOps' -c user.email='concourse-gitops@tdmtrader.local' commit \
  -m "chore(deploy): pin agent runner v${runner_version}" \
  -m "source_commit=${source_commit}" \
  -m "image=${image}"
