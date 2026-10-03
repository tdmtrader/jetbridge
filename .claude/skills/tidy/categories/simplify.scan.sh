#!/usr/bin/env bash
# Scan for the tidy `simplify` card (.claude/skills/tidy/categories/simplify.md).
# Run from the repo root with bash (zsh does not split $X):
#   bash .claude/skills/tidy/categories/simplify.scan.sh
# Installs staticcheck 2026.2.1 (v0.8.1) into /tmp/tidy-simplify outside the repo
# (never `go run` it: with GOOS set, that builds a binary this host cannot run),
# writes the gate helper /tmp/tidy-simplify/g.sh, then runs the admitted codes over
# four builds (darwin live,hangar_live; darwin untagged, for the //go:build !live
# file; linux live,hangar_live; windows without tests) merged with -merge. Prints
# `key<TAB>file:line<TAB>message` per hit, green tier first; UNANALYSED packages
# go to stderr. Exits non-zero with empty stdout if staticcheck or a build fails.
D=/tmp/tidy-simplify; mkdir -p $D
cat > $D/g.sh <<'EOF'
# Every default-check finding for package $1 in each build that compiles it, positions stripped.
SC=/tmp/tidy-simplify/staticcheck
{ "$SC" -fail '' -tags live,hangar_live "$1" | sed 's/^/darwin-live /'
  if go list -tags live,hangar_live -f '{{.IgnoredGoFiles}}' "$1" | grep -q go; then
    "$SC" -fail '' "$1" | sed 's/^/darwin /'
    GOOS=linux "$SC" -fail '' -tags live,hangar_live "$1" | sed 's/^/linux-live /'
    GOOS=windows "$SC" -fail '' -tests=false "$1" | sed 's/^/windows /'
  fi; } | sed -E 's/:[0-9]+:[0-9]+:/:/' | sort
EOF
cat > $D/scan.sh <<'EOF'
set -euo pipefail
D=/tmp/tidy-simplify; SC=$D/staticcheck; V='staticcheck 2026.2.1 (0.8.1)'
[ "$("$SC" -version 2>/dev/null)" = "$V" ] ||   # v0.8.1 needs go >= 1.26; GOTOOLCHAIN=auto fetches it
  GOTOOLCHAIN=auto GOBIN=$D go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
[ "$("$SC" -version)" = "$V" ]
C=S1000,S1002,S1003,S1004,S1005,S1006,S1008,S1009,S1010,S1012,S1017,S1019,S1020,S1023,S1024,S1028,S1029,S1031,S1032,S1033,S1035,S1037,S1038,S1039,ST1019
X=$(go list -tags live,hangar_live -f '{{if .IgnoredGoFiles}}{{.ImportPath}}{{end}}' ./...)
[ -n "$X" ]   # never empty while tools.go exists: empty means go list broke
rm -f $D/*.bin
"$SC" -f binary -checks $C -tags live,hangar_live ./...            > $D/1-darwin-live.bin
"$SC" -f binary -checks $C $X                                      > $D/2-darwin.bin   # the //go:build !live file
GOOS=linux "$SC" -f binary -checks $C -tags live,hangar_live ./... > $D/3-linux-live.bin
GOOS=windows "$SC" -f binary -tests=false -checks $C $X            > $D/4-windows.bin  # windows test files do not compile
"$SC" -merge -fail '' $D/*.bin > $D/raw.txt || grep -q '(compile)$' $D/raw.txt
awk '/^-: # /{p = $3; sub(/_test$/, "", p); sub(/^github\.com\/concourse\/concourse\/?/, "", p); print (p == "" ? "." : p)}' \
  $D/raw.txt | sort -u > $D/unanalysed.txt
[ ! -s $D/unanalysed.txt ] || { echo 'UNANALYSED (hits dropped; name them in the log row):'; cat $D/unanalysed.txt; } >&2
awk -F: 'FILENAME == ARGV[1] {bad[$0]; next}
  !/\((S1[0-9][0-9][0-9]|ST1019)\)$/ || /should use for range instead of for \{ select \{\} \}/ {next}
  /^(topgun|testflight|integration|testhelpers\/otel|atc\/db\/migration\/migrations)\/|fakes\/|(^|\/)vendor\// {next}
  {d = $1; if (!sub(/\/[^\/]*$/, "", d)) d = "."; if (!(d in bad)) print}' $D/unanalysed.txt $D/raw.txt \
  | while IFS=: read -r f l _ msg; do
      code=${msg##*(}; code=${code%)}
      if [ "$code" = ST1019 ]; then site=$(echo "$msg" | cut -d'"' -f2)
      else
        site=$(awk -v L="$l" 'NR <= L && /^func /{s = $0} NR < L && /^}/{s = ""} NR == L{print s; exit}' "$f" \
          | sed -E 's/^func (\(([A-Za-z_][A-Za-z0-9_]* )?\*?([A-Za-z0-9_]+)(\[[^]]*\])?\) )?([A-Za-z0-9_]+).*/\3.\5/; s/^\.//')
        site=${site:-L$l}
      fi
      tier=0
      case $code in S1000|S1017|S1028|S1037|ST1019) tier=1;; esac
      case $msg in *'{ return false }; return true'*) tier=1;; esac   # S1008 that negates
      printf '%s\t%s:%s@%s\t%s:%s\t%s\n' "$tier" "$code" "$site" "$f" "$f" "$l" "${msg# }"
    done | sort -s -t$'\t' -k1,1n | cut -f2-
EOF
bash $D/scan.sh
