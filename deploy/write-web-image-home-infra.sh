#!/bin/sh
set -eu
set +x

image=${1-}
source_commit=${2-}
repo=${3-home-infra}
file=apps/concourse.yaml
image_re='^registry.home/jetbridge@sha256:[a-f0-9]{64}$'
source_re='^[a-f0-9]{40}$'

printf '%s\n' "$image" | grep -Eq "$image_re" || {
  echo 'FATAL: image must be an immutable registry.home/jetbridge sha256 digest' >&2
  exit 1
}
printf '%s\n' "$source_commit" | grep -Eq "$source_re" || {
  echo 'FATAL: source commit must be 40 lowercase hex' >&2
  exit 1
}
git -C "$repo" rev-parse --is-inside-work-tree | grep -Fx true >/dev/null
test -f "$repo/$file"
test -z "$(git -C "$repo" status --porcelain)"

image_block_re='^[[:space:]]*image:[[:space:]]*$'
test "$(grep -Ec "$image_block_re" "$repo/$file" || true)" = 1 || {
  echo 'FATAL: expected exactly one image mapping' >&2
  exit 1
}
image_indent=$(grep -E "$image_block_re" "$repo/$file" | sed 's/^\([[:space:]]*\).*/\1/')
child_indent="${image_indent}  "
repo_re="^${child_indent}repository:[[:space:]]*registry\.home/jetbridge[[:space:]]*$"
test "$(grep -Ec "$repo_re" "$repo/$file" || true)" = 1 || {
  echo 'FATAL: image mapping must contain exactly one registry.home/jetbridge repository' >&2
  exit 1
}
digest_re="^${child_indent}digest:[[:space:]]*.*$"
source_re_line="^${child_indent}sourceCommit:[[:space:]]*.*$"
tested_re="^${child_indent}testedDigest:[[:space:]]*.*$"
digest_matches=$(grep -Ec "$digest_re" "$repo/$file" || true)
source_matches=$(grep -Ec "$source_re_line" "$repo/$file" || true)
tested_matches=$(grep -Ec "$tested_re" "$repo/$file" || true)
test "$digest_matches" -le 1 && test "$source_matches" -le 1 && test "$tested_matches" -le 1 || {
  echo 'FATAL: image digest/sourceCommit mapping is ambiguous' >&2
  exit 1
}

digest=${image#*@}

image_tmp=$(mktemp "$repo/.web-image-values.XXXXXX")
tmp=$(mktemp "$repo/.web-image.XXXXXX")
cleanup() {
  rm -f -- "$image_tmp" "$tmp"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
awk -v image_indent="$image_indent" -v child_indent="$child_indent" -v digest="$digest" -v source="$source_commit" '
  function indent_of(line) { sub(/[^[:space:]].*$/, "", line); return line }
  $0 == image_indent "image:" { in_image = 1; print; next }
  in_image && indent_of($0) == image_indent && $0 !~ /^[[:space:]]*$/ {
    if (!wrote_digest) print child_indent "digest: " digest
    if (!wrote_source) print child_indent "sourceCommit: " source
    in_image = 0
  }
  in_image && $0 ~ ("^" child_indent "digest:[[:space:]]*") {
    print child_indent "digest: " digest
    wrote_digest = 1
    next
  }
  in_image && $0 ~ ("^" child_indent "sourceCommit:[[:space:]]*") {
    print child_indent "sourceCommit: " source
    wrote_source = 1
    next
  }
  in_image && $0 ~ ("^" child_indent "testedDigest:[[:space:]]*") {
    next
  }
  { print }
  END {
    if (in_image) {
      if (!wrote_digest) print child_indent "digest: " digest
      if (!wrote_source) print child_indent "sourceCommit: " source
    }
  }
' "$repo/$file" > "$image_tmp"

# The old imperative self-upgrade path left Argo configured to ignore the web
# container image. Once image.digest is GitOps-owned, that exact exception
# prevents the Deployment from changing while the DaemonSet advances. Remove
# only the matching apps/Deployment/cicd/concourse-web jsonPointer item. Keep
# every unrelated ignore item or pointer byte-for-byte, and fail closed if the
# target item is duplicated or structurally ambiguous.
awk '
  { line[NR] = $0 }
  END {
    total = NR
    for (i = 1; i <= total; i++) {
      if (line[i] ~ /^  ignoreDifferences:[[:space:]]*$/) {
        header_count++
        header = i
      }
    }
    if (header_count > 1) {
      print "FATAL: multiple spec.ignoreDifferences mappings are ambiguous" > "/dev/stderr"
      exit 1
    }
    if (header_count == 0) {
      for (i = 1; i <= total; i++) print line[i]
      exit 0
    }

    section_end = total + 1
    for (i = header + 1; i <= total; i++) {
      if (line[i] ~ /^  [^[:space:]][^:]*:[[:space:]]*/) {
        section_end = i
        break
      }
    }
    item_count = 0
    for (i = header + 1; i < section_end; i++) {
      if (line[i] ~ /^    -[[:space:]]+/) {
        item_count++
        item_start[item_count] = i
      }
    }
    item_start[item_count + 1] = section_end

    for (item = 1; item <= item_count; item++) {
      start = item_start[item]
      finish = item_start[item + 1] - 1
      groups = kinds = names = namespaces = json_headers = 0
      target_pointers = other_json_pointers = other_methods = 0
      json_header = target_line = 0
      in_json = 0

      for (i = start; i <= finish; i++) {
        current = line[i]
        if (current ~ /^    -[[:space:]]+group:[[:space:]]*"?apps"?[[:space:]]*$/ || current ~ /^      group:[[:space:]]*"?apps"?[[:space:]]*$/) groups++
        if (current ~ /^    -[[:space:]]+kind:[[:space:]]*"?Deployment"?[[:space:]]*$/ || current ~ /^      kind:[[:space:]]*"?Deployment"?[[:space:]]*$/) kinds++
        if (current ~ /^    -[[:space:]]+name:[[:space:]]*"?concourse-web"?[[:space:]]*$/ || current ~ /^      name:[[:space:]]*"?concourse-web"?[[:space:]]*$/) names++
        if (current ~ /^    -[[:space:]]+namespace:[[:space:]]*"?cicd"?[[:space:]]*$/ || current ~ /^      namespace:[[:space:]]*"?cicd"?[[:space:]]*$/) namespaces++

        if (current ~ /^      jsonPointers:[[:space:]]*$/) {
          json_headers++
          json_header = i
          in_json = 1
          continue
        }
        if (in_json && current ~ /^      [^[:space:]]/) in_json = 0
        if (in_json && current ~ /^        -[[:space:]]*"?\/spec\/template\/spec\/containers\/0\/image"?[[:space:]]*$/) {
          target_pointers++
          target_line = i
        } else if (in_json && current ~ /^        -[[:space:]]+/) {
          other_json_pointers++
        }
        if (current ~ /^      [A-Za-z][^:]*:[[:space:]]*/ && current !~ /^      (group|kind|name|namespace|jsonPointers):/) {
          other_methods++
        }
      }

      if (target_pointers > 0 && names > 0) {
        if (groups != 1 || kinds != 1 || names != 1 || namespaces != 1 || json_headers != 1 || target_pointers != 1) {
          print "FATAL: legacy concourse-web image ignore rule is structurally ambiguous" > "/dev/stderr"
          exit 1
        }
        target_items++
        if (other_json_pointers == 0 && other_methods == 0) {
          for (i = start; i <= finish; i++) skip[i] = 1
          removed_items++
        } else {
          skip[target_line] = 1
          if (other_json_pointers == 0) skip[json_header] = 1
        }
      }
    }

    if (target_items > 1) {
      print "FATAL: multiple legacy concourse-web image ignore rules are ambiguous" > "/dev/stderr"
      exit 1
    }
    if (item_count > 0 && removed_items == item_count) skip[header] = 1
    for (i = 1; i <= total; i++) if (!skip[i]) print line[i]
  }
' "$image_tmp" > "$tmp"

if cmp -s "$repo/$file" "$tmp"; then
  exit 0
fi
mv "$tmp" "$repo/$file"
test "$(git -C "$repo" diff --name-only)" = "$file"
git -C "$repo" add -- "$file"
git -C "$repo" diff --cached --quiet && exit 0
git -C "$repo" -c user.name='Concourse GitOps' -c user.email='concourse-gitops@tdmtrader.local' commit \
  -m 'chore(deploy): pin web image' \
  -m "source_commit=${source_commit}" \
  -m "image=${image}"
