#!/usr/bin/env bash
# Parse plan.md and output JSON array of tasks with metadata
# Usage: parse-plan.sh <path-to-plan.md>

set -euo pipefail

PLAN_PATH="${1:-}"

if [[ -z "$PLAN_PATH" ]]; then
  echo "Usage: parse-plan.sh <path-to-plan.md>" >&2
  exit 1
fi

if [[ ! -f "$PLAN_PATH" ]]; then
  echo "Error: File not found: $PLAN_PATH" >&2
  exit 1
fi

# Check if jq is available
if ! command -v jq &> /dev/null; then
  echo "Error: jq is required but not installed" >&2
  exit 1
fi

# Parse plan.md and extract tasks using awk compatible with BSD and GNU
# Output: JSON array of task objects
awk '
BEGIN {
  phase_num = 0
  phase_title = ""
  task_count = 0
  printf "["
  first = 1
}

# Match phase headers: "## Phase N: Title" or "### Phase N: Title"
/^##+ Phase [0-9]+/ {
  # Extract phase number using sub/gsub instead of match with array
  line = $0
  gsub(/^##+ Phase /, "", line)
  gsub(/:.*$/, "", line)
  phase_num = line + 0  # Convert to number

  # Extract phase title (everything after "Phase N:" up to checkpoint bracket or end)
  line = $0
  gsub(/^##+ Phase [0-9]+:? */, "", line)
  gsub(/ *\[checkpoint:.*\].*$/, "", line)
  gsub(/[[:space:]]+$/, "", line)  # Trim trailing whitespace
  phase_title = line
  next
}

# Match new format tasks: "- [ ] Task: Description" or "- [x] Task: Description sha"
# Also match plain format: "- [ ] Description" (without Task: prefix)
/^- \[[ x~]\] / {
  if (phase_num == 0) next  # Skip tasks before any phase

  line_number = NR

  # Determine status by checking the character inside brackets
  if ($0 ~ /^- \[x\]/) {
    status = "completed"
  } else if ($0 ~ /^- \[~\]/) {
    status = "in_progress"
  } else {
    status = "pending"
  }

  # Extract description (remove checkbox and optional "Task: " prefix)
  desc = $0
  gsub(/^- \[[ x~]\] (Task: )?/, "", desc)

  # Check for commit SHA at end (7-40 hex chars)
  commit_sha = "null"
  if (desc ~ / [a-f0-9]{7,40}$/) {
    # Extract the SHA
    n = split(desc, parts, " ")
    last_part = parts[n]
    if (last_part ~ /^[a-f0-9]{7,40}$/) {
      commit_sha = "\"" last_part "\""
      # Remove SHA from description
      gsub(/ [a-f0-9]{7,40}$/, "", desc)
    }
  }

  # Escape special characters for JSON
  gsub(/\\/, "\\\\", desc)
  gsub(/"/, "\\\"", desc)
  gsub(/\t/, "\\t", desc)

  escaped_phase = phase_title
  gsub(/\\/, "\\\\", escaped_phase)
  gsub(/"/, "\\\"", escaped_phase)

  # Output JSON object
  if (!first) printf ","
  first = 0
  printf "\n  {"
  printf "\"phase\": %d, ", phase_num
  printf "\"phaseTitle\": \"%s\", ", escaped_phase
  printf "\"lineNumber\": %d, ", line_number
  printf "\"status\": \"%s\", ", status
  printf "\"description\": \"%s\", ", desc
  printf "\"commitSha\": %s", commit_sha
  printf "}"

  task_count++
  next
}

# Match legacy format tasks: "### Task X.X: Description"
/^### Task [0-9]+(\.[0-9]+)?:/ {
  if (phase_num == 0) next

  line_number = NR

  # Extract description (remove ### Task X.X: prefix)
  desc = $0
  gsub(/^### Task [0-9]+(\.[0-9]+)?: */, "", desc)

  # Check for commit SHA at end
  commit_sha = "null"
  if (desc ~ / [a-f0-9]{7,40}$/) {
    n = split(desc, parts, " ")
    last_part = parts[n]
    if (last_part ~ /^[a-f0-9]{7,40}$/) {
      commit_sha = "\"" last_part "\""
      gsub(/ [a-f0-9]{7,40}$/, "", desc)
    }
  }

  # Legacy tasks need sub-item analysis to determine status
  # For now, mark as pending (status will be determined by subsequent sub-items)
  status = "pending"

  # Escape for JSON
  gsub(/\\/, "\\\\", desc)
  gsub(/"/, "\\\"", desc)

  escaped_phase = phase_title
  gsub(/\\/, "\\\\", escaped_phase)
  gsub(/"/, "\\\"", escaped_phase)

  if (!first) printf ","
  first = 0
  printf "\n  {"
  printf "\"phase\": %d, ", phase_num
  printf "\"phaseTitle\": \"%s\", ", escaped_phase
  printf "\"lineNumber\": %d, ", line_number
  printf "\"status\": \"%s\", ", status
  printf "\"description\": \"%s\", ", desc
  printf "\"commitSha\": %s", commit_sha
  printf "}"

  task_count++
  next
}

END {
  printf "\n]\n"
}
' "$PLAN_PATH"
