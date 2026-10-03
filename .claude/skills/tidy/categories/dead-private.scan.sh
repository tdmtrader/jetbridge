#!/usr/bin/env bash
# dead-private scan: the candidate queue for categories/dead-private.md.
# Run: bash .claude/skills/tidy/categories/dead-private.scan.sh
# Stdout, one candidate per line, tier 1 first:
#   <tier> <file>:<line> <key> <span ranges> part|whole [<commit>]
# Stderr names every finding set aside and why. A `VOID:` line and exit 1
# mean no scan, never an empty queue. SC overrides the staticcheck path.
# Runs as written under bash or zsh, from anywhere in the root module.
cd "$(git rev-parse --show-toplevel)"
# staticcheck is pinned, not vendored. v0.8.1 builds only with go >= 1.26,
# which GOTOOLCHAIN=auto downloads once. No binary is no scan, never an empty queue.
SC=${SC:-$(go env GOPATH)/bin/staticcheck}
want='staticcheck 2026.2.1 (0.8.1)'
[ "$("$SC" -version 2>/dev/null)" = "$want" ] \
  || GOTOOLCHAIN=auto GOBIN=$(dirname "$SC") go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
[ "$("$SC" -version 2>/dev/null)" = "$want" ] || { echo "VOID: $SC is not $want" >&2; exit 1; }

# One U1000 pass in the host configuration, tests included; files with a build
# constraint are excluded below, so no other configuration has a say. Every
# line must be a U1000 finding and there must be some (the tree carries
# findings this card never takes, so zero means nothing was analysed). A
# `(compile)` or `(config)` line means a package went unanalysed, usually a
# build-cache entry deleted under it by a concurrent `go clean -cache`: retry
# once on a private cache, delete that cache, and give up if it fails again.
OUT=$(mktemp); trap 'rm -f "$OUT" "$OUT".*' EXIT
u1000() { "$SC" -checks U1000 ./... >"$OUT" 2>&1; ! grep -qv ' (U1000)$' "$OUT" && grep -q ' (U1000)$' "$OUT"; }
u1000 || { C=$(mktemp -d); (export GOCACHE=$C; u1000); r=$?; rm -rf "$C"; [ $r = 0 ]; } \
  || { echo "VOID: $(grep -v ' (U1000)$' "$OUT" | head -1)" >&2; exit 1; }

# The span of declaration line D (kind K, name I, type T): its doc comment and
# body, plus each method of T in the file. Prints "ok <s-e,...> <lines>
# part|whole" (whole: nothing but package, imports and comments would be left)
# or why it is set aside; writes the span's text to $SPAN.
cat >"$OUT.span.awk" <<'AWK'
function word(s, w) { return s ~ ("(^|[^A-Za-z0-9_])" w "([^A-Za-z0-9_]|$)") }
function trail(s) { return s !~ /^[ \t]*\/\// && s ~ /[^ \t].*[ \t]\/\/ / }
function piece(d,   s, e, j) {
  s = d; while (s > 1 && L[s-1] ~ /^\/\//) s--
  if (L[s-1] ~ /\*\/[ \t]*$/) return "block-comment"
  e = d
  if (L[d] ~ /\{$/ || (K == "func" && L[d] !~ /\}$/)) { while (e < n && L[e] != "}") e++ }
  else if (L[d+1] ~ /^[ \t]/ || L[d] ~ /[(\[,=]$/) return "multi-line"
  if (trail(L[s-1]) || trail(L[e+1]) || (s == d && e == d && trail(L[d]) && (L[s-1] != "" || L[e+1] != ""))) return "comment-alignment"
  for (j = s; j <= e; j++) { in_span[j] = 1; txt = txt "\n" L[j] }
  rng = rng (rng ? "," : "") s "-" e; cnt += e - s + 1
  return ""
}
{ L[++n] = $0 }
END {
  if (L[D] !~ /^(func|type|var|const) /) { print "in-block"; exit }
  if (K ~ /var|const/ && L[D] !~ ("^" K " " I "( |$)")) { print "multi-name"; exit }
  if (K ~ /var|const/ && L[D] ~ /=.*\(/) { print "initializer-call"; exit }
  r = piece(D); if (r != "") { print r; exit }
  if (K == "type") for (i = 1; i <= n; i++)
    if (L[i] ~ ("^func \\(([A-Za-z_][A-Za-z0-9_]* )?\\*?" T "(\\[[^]]*\\])?\\) ")) { r = piece(i); if (r != "") { print r; exit } }
  if (txt ~ /\/\/go:/) { print "directive"; exit }
  for (i = 1; i <= n; i++) if (!(i in in_span) && word(L[i], I)) { print "named-here:" i; exit }
  rest = 0
  for (i = 1; i <= n; i++) {
    if (L[i] ~ /^import \($/) { while (i < n && L[i] != ")") i++; continue }
    if (!(i in in_span) && L[i] !~ /^(package |import |\/\/|$)/) rest = 1
  }
  print "ok " rng " " cnt " " (rest ? "part" : "whole"); printf "%s", txt > SPAN
}
AWK

# The package's asserting functions: a body with an assertion call, or one
# that calls such a function, repeated until nothing new joins.
TOK='(Expect|Eventually|Consistently|Ω|WithOffset)\(|(^|[^A-Za-z0-9_.])Fail\(|(^|[^A-Za-z0-9_])(t|tb|b)\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\(|(^|[^A-Za-z0-9_])(assert|require)\.[A-Z]'
cat >"$OUT.assert.awk" <<'AWK'
/^func / { nm = $0; sub(/^func (\([^)]*\) )?/, "", nm); sub(/[^A-Za-z0-9_].*/, "", nm)
           cur = nm; B[cur] = B[cur] "\n" $0; if ($0 ~ /\}$/) cur = ""; next }
cur != "" { B[cur] = B[cur] "\n" $0; if ($0 == "}") cur = "" }
END {
  for (f in B) if (B[f] ~ ENVIRON["TOK"]) S[f] = 1
  do { ch = 0; alt = ""; for (s in S) alt = alt (alt ? "|" : "") s
       if (alt != "") for (f in B) if (!(f in S) && B[f] ~ ("(^|[^A-Za-z0-9_])(" alt ")\\(")) { S[f] = 1; ch = 1 }
  } while (ch)
  for (s in S) print s
}
AWK

cutoff=$(date -v-30d +%Y%m%d 2>/dev/null || date -d '30 days ago' +%Y%m%d)
sfx='_(aix|android|darwin|dragonfly|freebsd|illumos|ios|js|linux|netbsd|openbsd|plan9|solaris|wasip1|windows|386|amd64|arm|arm64|loong64|mips[a-z0-9]*|ppc64[a-z]*|riscv64|s390x|wasm)(_test)?\.go$'
grep -E ': (func|type|var|const) [^ ]+ is unused \(U1000\)$' "$OUT" \
  | grep -v -e '^vendor/' -e 'fakes/' -e '^atc/db/migration/migrations/' \
  | sed -E 's/^([^:]+):([0-9]+):[0-9]+: ([a-z]+) ([^ ]+) is unused \(U1000\)$/\1 \2 \3 \4/' \
  | while read -r f l kind name; do
      dir=$(dirname "$f"); id=${name##*.}; T=; [ "$kind" = type ] && T=${id}
      recv=$(printf '%s' "$name" | sed -nE 's/^\(?\*?([A-Za-z0-9_]+)(\[[^]]*\])?\)?\.[A-Za-z0-9_]+$/\1/p')
      # a method of a reported type is part of that type's instance
      [ -n "$recv" ] && grep -q "^$f:[0-9]*:[0-9]*: type $recv is unused" "$OUT" && continue
      key=${id}; [ -n "$recv" ] && key=$recv.${id}; key=$key@$f
      skip() { echo "$1 $key" >&2; }
      case ${id} in [a-z_]*) ;; *) continue ;; esac            # exported: surface
      printf '%s\n' "$f" | grep -qE "$sfx" && { skip constrained; continue; }
      grep -q -e '^//go:build' -e '^// +build' "$f" && { skip constrained; continue; }
      grep -q '^// Code generated .* DO NOT EDIT\.$' "$f" && { skip generated; continue; }
      [ -z "$(gofmt -l "$f")" ] || { skip gofmt-dirty; continue; }
      [ -n "$T" ] && grep -lE "^func \(([A-Za-z_][A-Za-z0-9_]* )?\*?${T}(\[[^]]*\])?\) " "$dir"/*.go \
        | grep -qvxF "$f" && { skip methods-elsewhere; continue; }
      r=$(awk -v D="$l" -v K="$kind" -v I="${id}" -v T="$T" -v SPAN="$OUT.span" -f "$OUT.span.awk" "$f")
      case $r in ok*) ;; *) skip "$r"; continue ;; esac
      read -r _ rng cnt whole <<<"$r"
      [ "$cnt" -le 60 ] || { skip "too-long:$cnt"; continue; }
      # no other file names it: code, strings, comments, docs, YAML, Elm, brine. Tidy
      # log rows are records, not references.
      other=$(git grep -lw -e "${id}" -- . ':!.claude/skills/tidy' | grep -vxF "$f" | head -1)
      [ -z "$other" ] || { skip "named:$other"; continue; }
      alt=$(TOK=$TOK awk -f "$OUT.assert.awk" "$dir"/*.go | paste -sd'|' -)
      if grep -qE "$TOK" "$OUT.span" || { [ -n "$alt" ] && grep -qE "(^|[^A-Za-z0-9_])($alt)\(" "$OUT.span"; }; then
        skip asserts; continue; fi
      case $f in topgun/*) tier=3 ;; *_test.go) tier=2 ;; *) tier=1 ;; esac
      if [ "$whole" = whole ]; then
        if [ $tier = 1 ]; then n=$(ls "$dir"/*.go | grep -vc '_test\.go$'); else n=$(ls "$dir"/*.go | wc -l); fi
        [ "$n" -gt 1 ] || { skip last-file; continue; }
        grep -q -e '^// Package ' -e '^//go:' -e '^import _ ' -e '^[[:space:]]_ "' "$f" && { skip file-carries-more; continue; }
      fi
      sha=
      if [ $tier = 1 ]; then
        # it once had a production caller, removed 30+ days ago by something other than a tidy
        ps=":(glob)$dir/*.go"; px=":(glob,exclude)$dir/*_test.go"
        h=$(git log --date=format:%Y%m%d --format='%h %cd %s' --pickaxe-regex -S"(^|[^A-Za-z0-9_])${id}([^A-Za-z0-9_]|\$)" -- "$ps" "$px")
        [ "$(printf '%s\n' "$h" | grep -c .)" -ge 2 ] || { skip born-dead; continue; }
        read -r sha when subject <<<"$(printf '%s\n' "$h" | head -1)"
        case $subject in tidy\(*) skip "tidy-newest:$sha"; continue ;; esac
        [ "$when" -lt "$cutoff" ] || { skip "recent:$sha"; continue; }
        git show --format= "$sha" -- "$ps" "$px" | grep -E '^-' | grep -vE '^-[[:space:]]*//' \
          | grep -vE "^-(func (\([^)]*\) )?${id}[[(]|func \([^)]*[ *]${id}(\[[^]]*\])?\) |type ${id} |var ${id} |const ${id} |[[:space:]]+${id}[[:space:]])" \
          | grep -qE "(^|[^A-Za-z0-9_])${id}([^A-Za-z0-9_]|\$)" || { skip "no-caller-removed:$sha"; continue; }
      fi
      echo "$tier $f:$l $key $rng $whole $sha"
    done | sort -k1,1n -k2,2V
