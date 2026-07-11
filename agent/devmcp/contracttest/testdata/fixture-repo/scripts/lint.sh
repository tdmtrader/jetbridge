#!/bin/sh
# Deterministic lint findings: "ran and found problems" = status failed.
echo "src/app.sh:1: warning: found a lint problem"
exit 1
