#!/bin/sh
set -eu

if version=$(claude --version 2>&1); then
  :
else
  status=$?
  printf 'ERROR: Claude version command failed (status %s)\n' "$status" >&2
  exit 1
fi
case "$version" in 2.1.212*) ;; *) echo "ERROR: expected Claude 2.1.212" >&2; exit 1;; esac

if help=$(claude --help 2>&1); then
  :
else
  status=$?
  printf 'ERROR: Claude help command failed (status %s)\n' "$status" >&2
  exit 1
fi
for flag in --max-budget-usd --mcp-config --strict-mcp-config --append-system-prompt --output-format --verbose --dangerously-skip-permissions; do
  if ! printf '%s\n' "$help" | grep -F -- "$flag" >/dev/null; then
    printf 'ERROR: Claude help is missing required flag %s\n' "$flag" >&2
    exit 1
  fi
done
if max_turns_probe=$(claude --print --max-turns </dev/null 2>&1); then
  printf 'ERROR: Claude parser accepted a missing --max-turns argument\n' >&2
  exit 1
else
  status=$?
fi
case "$max_turns_probe" in
  *"option '--max-turns <turns>' argument missing"*)
    ;;
  *)
    printf 'ERROR: Claude parser did not report a missing argument for required flag --max-turns (status %s)\n' "$status" >&2
    exit 1
    ;;
esac
for binary in agent-runner function-runner agent-output; do
  if ! command -v "$binary" >/dev/null 2>&1; then
    printf 'ERROR: required Concourse binary missing: %s\n' "$binary" >&2
    exit 1
  fi
done

smoke_dir=$(mktemp -d "${TMPDIR:-/tmp}/agent-output-smoke.XXXXXX")
authority_file=/run/concourse/output-builder/authority.json
sidecar_pid=
cleanup() {
  if test -n "$sidecar_pid"; then
    kill "$sidecar_pid" 2>/dev/null || :
    wait "$sidecar_pid" 2>/dev/null || :
  fi
  rm -f -- "$authority_file"
  rm -rf -- "$smoke_dir"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$smoke_dir/work/review" "$smoke_dir/claude-config"
printf '%s\n' "{\"work_root\":\"$smoke_dir/work\",\"inputs\":{},\"outputs\":{\"review\":{\"port\":{\"name\":\"review\",\"type\":\"review/v1\"},\"mount_root\":\"$smoke_dir/work/review\"}}}" | install -D -m 0444 /dev/stdin "$authority_file"

agent-output serve > "$smoke_dir/agent-output.log" 2>&1 &
sidecar_pid=$!
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if curl --fail --silent --show-error http://127.0.0.1:7783/healthz >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$sidecar_pid" 2>/dev/null; then
    printf 'ERROR: managed output builder sidecar exited before becoming healthy\n' >&2
    exit 1
  fi
  sleep 1
done
if ! curl --fail --silent --show-error http://127.0.0.1:7783/healthz >/dev/null 2>&1; then
  printf 'ERROR: managed output builder sidecar did not become healthy\n' >&2
  exit 1
fi

mcp_add_output="$smoke_dir/mcp-add.txt"
if ! (ulimit -f 16; CLAUDE_CONFIG_DIR="$smoke_dir/claude-config" claude mcp add --scope user --transport http output-builder http://127.0.0.1:7783/mcp > "$mcp_add_output" 2>&1); then
  printf 'ERROR: Claude failed to register managed output builder MCP\n' >&2
  head -c 8192 "$mcp_add_output" >&2 || :
  exit 1
fi

mcp_output="$smoke_dir/mcp-list.txt"
if ! (ulimit -f 16; CLAUDE_CONFIG_DIR="$smoke_dir/claude-config" claude mcp list > "$mcp_output" 2>&1); then
  printf 'ERROR: Claude failed to list managed output builder MCP\n' >&2
  head -c 8192 "$mcp_output" >&2 || :
  exit 1
fi
if ! head -c 8192 "$mcp_output" | grep -E -x -- 'output-builder: .+ - (✓|✔) Connected' >/dev/null; then
  printf 'ERROR: managed output builder MCP is not connected\n' >&2
  head -c 8192 "$mcp_output" >&2 || :
  exit 1
fi
