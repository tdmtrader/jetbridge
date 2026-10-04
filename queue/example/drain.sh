# mq1_drain CONFIG: print the queue's in-flight work for a safe rollback:
#   ROW <id> <sha40> <inflight|queued>, in-flight rows first, then queue order
#   PUSH <idle|mid-flight>, then LEASE <none|holder> [run-id]
# It is read-only. It prints nothing and returns non-zero unless the whole
# state was read. QUEUE names the queue binary (default: queue).
mq1_drain() {
  local cfg="${1:?usage: mq1_drain CONFIG}" out
  out="$("${QUEUE:-queue}" drain --config "$cfg")" || return 1
  printf '%s\n' "$out"
}
