#!/bin/sh
# Emits output slowly enough that a server running with a short
# DEV_MCP_PROGRESS_INTERVAL must emit at least one progress notification.
for i in 1 2 3 4 5 6 7 8 9 10; do
  echo "running case $i"
  sleep 0.1
done
echo "10 cases passed"
exit 0
