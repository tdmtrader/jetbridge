#!/usr/bin/env bash
# redundant-comment scan: writes the go/ast scanner to $T/rcscan.go, runs it
# into $T/queue.tsv (every eligible instance), and prints the first row whose
# key is not yet in redundant-comment.md's log. Runs from anywhere in the root
# module under bash or zsh. Export T first to keep the scanner for the Gate's
# re-scan. One row per key; a key's
# every site in its file is one instance, so the second column may list
# several lines. A file it cannot read or parse fails the scan (`SCAN FAILED`).
cd "$(git rev-parse --show-toplevel)"
T=${T:-$(mktemp -d)}; mkdir -p "$T"
cat > "$T/rcscan.go" <<'EOF'
// rcscan lists redundant-comment instances: go run rcscan.go
package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type cand struct {
	key, path, comment, code string
	line, tier               int
	lines                    []int
}

var (
	skip  = regexp.MustCompile(`^(hack/mcp-oauth-probe|atc/worker/jetbridge/brine|atc/db/migration/migrations)$|(^|/)(vendor|node_modules|testdata|[._][^/]*)$|fakes$`)
	gen   = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)
	shape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ,'-]*[A-Za-z0-9]\.?$|^[A-Za-z]\.?$`)
	first = regexp.MustCompile(`^(nolint|lint|go|line|export|extern|todo|fixme|note|xxx|hack|bug|deprecated)$`)
	// the only words a restating comment may add: articles, pronouns,
	// prepositions, conjunctions, copulas
	stop  = words("a an the and or of to in on for with by from as at is are be it its this that we our now then here into")
	trail = regexp.MustCompile(`[^ \t].*[ \t]//`)
)

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// split cuts s into lowercase runs of letters or of digits, a camel-case hump
// ending each run (ErrNotFound: err, not, found; HTTPServer: http, server;
// sha256: sha, 256).
func split(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := cur[len(cur)-1]
			if unicode.IsDigit(r) != unicode.IsDigit(prev) {
				flush()
			} else if unicode.IsUpper(r) && (unicode.IsLower(prev) || (unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func main() {
	var cands []cand
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(p)
		if d.IsDir() {
			if rel != "." && skip.MatchString(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if gen.Match(src) {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			return err
		}
		if out, err := format.Source(src); err != nil || string(out) != string(src) {
			return nil // gofmt-dirty: the gate needs a clean file
		}
		lines := strings.Split(string(src), "\n")
		line := func(n int) string { // 1-based; "" past the end
			if n < 1 || n > len(lines) {
				return ""
			}
			return lines[n-1]
		}
		// own-line single // comments by line
		own := map[int]string{}
		for _, g := range f.Comments {
			if len(g.List) != 1 {
				continue
			}
			t := g.List[0].Text
			pos := fset.Position(g.List[0].Pos())
			if !strings.HasPrefix(t, "// ") || strings.TrimSpace(line(pos.Line)[:pos.Column-1]) != "" {
				continue
			}
			own[pos.Line] = strings.TrimSpace(t[3:])
		}
		tier := 1
		if strings.HasSuffix(rel, "_test.go") {
			tier = 2
		}
		if strings.HasPrefix(rel, "topgun/") {
			tier = 3
		}
		ends := func(s string) bool { // a line that ends a chunk
			s = strings.TrimSpace(s)
			return s == "" || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "}") || strings.HasPrefix(s, "case ") || strings.HasPrefix(s, "default:")
		}
		assignsErr := func(s ast.Stmt) bool {
			var lhs []ast.Expr
			switch x := s.(type) {
			case *ast.AssignStmt:
				lhs = x.Lhs
			case *ast.DeclStmt:
				if gd, ok := x.Decl.(*ast.GenDecl); ok {
					for _, sp := range gd.Specs {
						if vs, ok := sp.(*ast.ValueSpec); ok {
							for _, n := range vs.Names {
								lhs = append(lhs, n)
							}
						}
					}
				}
			}
			for _, e := range lhs {
				if id, ok := e.(*ast.Ident); ok && id.Name == "err" {
					return true
				}
			}
			return false
		}
		var fn string
		var visit func(list []ast.Stmt)
		var body func(b *ast.BlockStmt)
		visit = func(list []ast.Stmt) {
			for i, s := range list {
				switch s.(type) {
				case *ast.CaseClause, *ast.CommClause, *ast.LabeledStmt:
				default:
					sl := fset.Position(s.Pos()).Line
					c, ok := own[sl-1]
					if !ok || !shape.MatchString(c) {
						break
					}
					ws := split(c)
					if len(ws) > 0 && first.MatchString(ws[0]) {
						break
					}
					// the code line with its string and rune literals blanked:
					// a comment may describe data; it restates only code
					cl := []rune(line(sl))
					ast.Inspect(s, func(n ast.Node) bool {
						if bl, ok := n.(*ast.BasicLit); ok && (bl.Kind == token.STRING || bl.Kind == token.CHAR) {
							from, to := fset.Position(bl.Pos()), fset.Position(bl.End())
							if from.Line == sl {
								stop := len(cl)
								if to.Line == sl && to.Column-1 < stop {
									stop = to.Column - 1
								}
								for k := from.Column - 1; k < stop; k++ {
									cl[k] = ' '
								}
							}
						}
						return true
					})
					code := words(strings.Join(split(string(cl)), " "))
					n := 0
					ok = true
					for _, w := range ws {
						if stop[w] {
							continue
						}
						n++
						if !code[w] {
							ok = false
						}
					}
					if !ok || n == 0 {
						break
					}
					// the chunk: this statement, plus its err check
					end := fset.Position(s.End()).Line
					if i+1 < len(list) && assignsErr(s) {
						nl := strings.TrimSpace(line(end + 1))
						if fset.Position(list[i+1].Pos()).Line == end+1 && (strings.HasPrefix(nl, "if err != nil {") || strings.HasPrefix(nl, "Expect(err)")) {
							end = fset.Position(list[i+1].End()).Line
						}
					}
					if !ends(line(end + 1)) {
						break
					}
					if trail.MatchString(line(sl-2)) || trail.MatchString(line(sl)) {
						break // gofmt realigns surviving trailing comments
					}
					cands = append(cands, cand{fn + "@" + rel + ":" + c, rel, c, strings.TrimSpace(line(sl)), sl - 1, tier, nil})
				}
				ast.Inspect(s, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.BlockStmt:
						if x != nil {
							visit(x.List)
						}
						return false
					case *ast.CaseClause:
						visit(x.Body)
						return false
					case *ast.CommClause:
						visit(x.Body)
						return false
					case *ast.FuncLit:
						body(x.Body)
						return false
					}
					return true
				})
			}
		}
		body = func(b *ast.BlockStmt) {
			if b != nil {
				visit(b.List)
			}
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				save := fn
				fn = "_"
				ast.Inspect(decl, func(n ast.Node) bool {
					if fl, ok := n.(*ast.FuncLit); ok {
						body(fl.Body)
						return false
					}
					return true
				})
				fn = save
				continue
			}
			fn = fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				t := fd.Recv.List[0].Type
				if st, ok := t.(*ast.StarExpr); ok {
					t = st.X
				}
				switch x := t.(type) {
				case *ast.IndexExpr:
					t = x.X
				case *ast.IndexListExpr:
					t = x.X
				}
				if id, ok := t.(*ast.Ident); ok {
					fn = id.Name + "." + fd.Name.Name
				}
			}
			body(fd.Body)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "SCAN FAILED:", err)
		os.Exit(1)
	}
	// one row per key: every site in the file with that key is the instance
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.path != b.path {
			return a.path < b.path
		}
		return a.line < b.line
	})
	rows := map[string]int{}
	var out []cand
	for _, c := range cands {
		if i, ok := rows[c.key]; ok {
			out[i].lines = append(out[i].lines, c.line)
			continue
		}
		rows[c.key] = len(out)
		c.lines = []int{c.line}
		out = append(out, c)
	}
	for _, c := range out {
		var ls []string
		for _, l := range c.lines {
			ls = append(ls, fmt.Sprint(l))
		}
		fmt.Printf("%s\t%s:%s\t%s\t%s\n", c.key, c.path, strings.Join(ls, ","), c.comment, c.code)
	}
}
EOF
go run "$T/rcscan.go" > "$T/queue.tsv" || { echo "SCAN FAILED" >&2; exit 1; }
card=.claude/skills/tidy/categories/redundant-comment.md
while IFS=$'\t' read -r key rest; do
  [ -f "$card" ] && grep -qF "| $key |" "$card" && continue
  printf '%s\t%s\n' "$key" "$rest"; exit 0
done < "$T/queue.tsv"
echo "queue empty" >&2
