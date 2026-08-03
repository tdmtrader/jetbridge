#!/bin/sh
set -eu
set +x

image=${1-}
source_commit=${2-}
repo=${3-home-infra}
file=apps/concourse-live-tested-image.env
image_re='^registry.home/jetbridge@sha256:[a-f0-9]{64}$'
source_re='^[a-f0-9]{40}$'

printf '%s\n' "$image" | grep -Eq "$image_re" || {
  echo 'FATAL: tested image must be an immutable registry.home/jetbridge sha256 digest' >&2
  exit 1
}
printf '%s\n' "$source_commit" | grep -Eq "$source_re" || {
  echo 'FATAL: source commit must be 40 lowercase hex' >&2
  exit 1
}
git -C "$repo" rev-parse --is-inside-work-tree | grep -Fx true >/dev/null
test -d "$repo/apps"
test -z "$(git -C "$repo" status --porcelain)"

tmp=$(mktemp "$repo/.live-tested-image.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
printf 'SOURCE_COMMIT=%s\nTESTED_IMAGE=%s\n' "$source_commit" "$image" > "$tmp"

if test -f "$repo/$file" && cmp -s "$repo/$file" "$tmp"; then
  exit 0
fi
mv "$tmp" "$repo/$file"

changed=$(git -C "$repo" status --porcelain --untracked-files=all)
case "$changed" in
  " M $file"|"?? $file") ;;
  *)
    echo "FATAL: attestation update changed unexpected files: $changed" >&2
    exit 1
    ;;
esac

git -C "$repo" add -- "$file"
git -C "$repo" diff --cached --quiet && exit 0
git -C "$repo" -c user.name='Concourse GitOps' -c user.email='concourse-gitops@tdmtrader.local' commit \
  -m 'chore(deploy): attest live-tested web image' \
  -m "source_commit=${source_commit}" \
  -m "image=${image}"
