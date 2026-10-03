#!/usr/bin/env bash
# guard-clause scan: writes the go/ast scanner to $T/guardscan.go, runs it into
# $T/queue.tsv (every eligible instance), and prints the first row whose key is
# not yet in guard-clause.md's log. Run from the repo root. Export T first to
# keep the scanner for `go run "$T/guardscan.go" -apply '<key>'` and the Gate.
T=${T:-$(mktemp -d)}
cat > "$T/guardscan.go" <<'EOF'
// guardscan lists guard-clause instances: go run guardscan.go [-apply KEY]
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type cand struct {
	key, path, guard, exit string
	ifLine, endLine, body  int
}

var (
	skip    = regexp.MustCompile(`^(topgun|hack/mcp-oauth-probe|atc/worker/jetbridge/brine|atc/db/migration/migrations)$|(^|/)(vendor|node_modules|testdata|[._][^/]*)$|fakes$`)
	errName = regexp.MustCompile(`(?i)^err|err$|error$`)
	report  = regexp.MustCompile(`^(Error|Errorf|Errorw|Errorln|Fail)$`)
	leave   = regexp.MustCompile(`^(Fatal|Panic|Fail|Skip|Exit$|Goexit$)`)
	flip    = map[token.Token]string{token.EQL: "!=", token.NEQ: "==", token.LSS: ">=", token.GEQ: "<", token.GTR: "<=", token.LEQ: ">"}
	lin     = build.Default
	dar     = build.Default
)

func die(err error) { fmt.Fprintln(os.Stderr, "guardscan:", err); os.Exit(1) }

func main() {
	lin.GOOS, lin.GOARCH, dar.GOOS, dar.GOARCH = "linux", "amd64", "darwin", "arm64"
	var cs []cand
	seen := map[string]int{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != "." && skip.MatchString(p) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		dir, name := filepath.Split(p)
		if ok, err := match(dir, name); err != nil || !ok {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			return err
		}
		if ast.IsGenerated(f) || bytes.Contains(src[:f.Package], []byte("//go:build")) {
			return nil
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				for _, c := range scan(fset, f, src, p, fd) {
					seen[c.key]++
					cs = append(cs, c)
				}
			}
		}
		return nil
	})
	if err != nil {
		die(err)
	}
	sort.SliceStable(cs, func(i, j int) bool {
		ti, tj := strings.HasSuffix(cs[i].path, "_test.go"), strings.HasSuffix(cs[j].path, "_test.go")
		return ti != tj && tj || ti == tj && cs[i].key < cs[j].key
	})
	applying := len(os.Args) == 3 && os.Args[1] == "-apply"
	for _, c := range cs {
		switch {
		case seen[c.key] > 1: // two sites share one key: neither is addressable
		case applying:
			if c.key == os.Args[2] {
				apply(c)
				return
			}
		default:
			fmt.Printf("%s\t%s:%d-%d\t%d\tif %s { %s }\n", c.key, c.path, c.ifLine, c.endLine, c.body, c.guard, c.exit)
		}
	}
	if applying {
		die(fmt.Errorf("no instance has the key %q", os.Args[2]))
	}
}

// match: the file is in the default build on both linux/amd64 and darwin/arm64.
func match(dir, name string) (bool, error) {
	for _, c := range []build.Context{lin, dar} {
		if ok, err := c.MatchFile(dir, name); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// apply: guard, exit and brace replace the if line; the body loses one tab; the old brace goes.
func apply(c cand) {
	src, err := os.ReadFile(c.path)
	if err != nil {
		die(err)
	}
	lines := strings.Split(string(src), "\n")
	ind := strings.TrimSuffix(lines[c.ifLine-1], strings.TrimLeft(lines[c.ifLine-1], "\t"))
	out := append(lines[:c.ifLine-1:c.ifLine-1], ind+"if "+c.guard+" {", ind+"\t"+c.exit, ind+"}")
	for _, l := range lines[c.ifLine : c.endLine-1] {
		out = append(out, strings.TrimPrefix(l, "\t"))
	}
	if err := os.WriteFile(c.path, []byte(strings.Join(append(out, lines[c.endLine:]...), "\n")), 0o644); err != nil {
		die(err)
	}
}

func scan(fset *token.FileSet, f *ast.File, src []byte, path string, fd *ast.FuncDecl) (out []cand) {
	text := func(n ast.Node) string {
		return string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	lines := strings.Split(string(src), "\n")
	fn := fd.Name.Name
	if fd.Recv != nil {
		t := fd.Recv.List[0].Type
		if s, ok := t.(*ast.StarExpr); ok {
			t = s.X
		}
		if ix, ok := t.(*ast.IndexExpr); ok {
			t = ix.X
		} else if ix, ok := t.(*ast.IndexListExpr); ok {
			t = ix.X
		}
		fn = text(t) + "." + fn
	}
	labels, defers := false, false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		b, isBranch := n.(*ast.BranchStmt)
		_, isLabel := n.(*ast.LabeledStmt)
		_, isDefer := n.(*ast.DeferStmt)
		labels = labels || isLabel || isBranch && b.Tok == token.GOTO
		defers = defers || isDefer
		return true
	})
	try := func(form string, dst *ast.BlockStmt, outer map[string]bool) {
		i := len(dst.List) - 1
		ifs, ok := dst.List[i].(*ast.IfStmt)
		if !ok || ifs.Init != nil || ifs.Else != nil {
			return
		}
		cond, guard := text(ifs.Cond), negate(ifs.Cond, text)
		ifLine, endLine, stmts := line(ifs.Pos()), line(ifs.Body.Rbrace), ifs.Body.List
		ind := strings.TrimSuffix(lines[ifLine-1], strings.TrimLeft(lines[ifLine-1], "\t"))
		body := endLine - ifLine - 1
		bad := guard == "" || strings.ContainsAny(cond, "|'`") || lines[ifLine-1] != ind+"if "+cond+" {" ||
			lines[endLine-1] != ind+"}" || body < 4 || body > 20 || len(stmts) < 2 || exits(stmts[len(stmts)-1])
		// The body handles a failure, so it is not the main path: an error, a comma-ok miss, a test report.
		ast.Inspect(ifs.Cond, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			bad = bad || ok && errName.MatchString(id.Name)
			return true
		})
		for _, o := range operands(ifs.Cond) {
			u, ok := o.(*ast.UnaryExpr)
			bad = bad || ok && u.Op == token.NOT && text(u.X) == "ok"
		}
		for _, s := range stmts {
			bad = bad || report.MatchString(callName(s))
		}
		ast.Inspect(ifs.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			bad = bad || ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "\n")
			return true
		})
		// No comment between the previous statement (or the block's brace) and the if, nor after the if.
		prev := dst.Lbrace
		if i > 0 {
			prev = dst.List[i-1].End()
		}
		for _, cg := range f.Comments {
			bad = bad || cg.Pos() > prev && cg.End() < ifs.Pos() || cg.Pos() > ifs.End() && cg.End() < dst.Rbrace
		}
		declared(dst.List[:i], outer)
		inner := declared(stmts, map[string]bool{})
		for k := range inner {
			bad = bad || outer[k]
		}
		if !bad {
			out = append(out, cand{form + ":" + fn + "@" + path + ":" + cond, path, guard, form, ifLine, endLine, body})
		}
	}
	if labels {
		return nil
	}
	if fd.Type.Results == nil && !defers && len(fd.Body.List) > 0 {
		params, fields := map[string]bool{}, append([]*ast.Field{}, fd.Type.Params.List...)
		if fd.Recv != nil {
			fields = append(fields, fd.Recv.List...)
		}
		if fd.Type.TypeParams != nil {
			fields = append(fields, fd.Type.TypeParams.List...)
		}
		for _, fld := range fields {
			for _, id := range fld.Names {
				params[id.Name] = true
			}
		}
		try("return", fd.Body, params)
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if l, ok := n.(*ast.ForStmt); ok && len(l.Body.List) > 0 {
			try("continue", l.Body, map[string]bool{})
		} else if l, ok := n.(*ast.RangeStmt); ok && len(l.Body.List) > 0 {
			try("continue", l.Body, map[string]bool{})
		}
		return true
	})
	return out
}

// callName: the called function's name ("panic", "Errorf", "Exit") when s is a call statement.
func callName(s ast.Stmt) string {
	if e, ok := s.(*ast.ExprStmt); ok {
		if call, ok := e.X.(*ast.CallExpr); ok {
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				return fun.Name
			case *ast.SelectorExpr:
				return fun.Sel.Name
			}
		}
	}
	return ""
}

// exits: the body already leaves, so the if is already a guard.
func exits(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	}
	name := callName(s)
	return name == "panic" || leave.MatchString(name)
}

// operands: the condition itself, or each operand of its top-level && chain.
func operands(e ast.Expr) []ast.Expr {
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.LAND {
		return append(operands(b.X), b.Y)
	}
	return []ast.Expr{e}
}

// negate: the exact negation by the card's table, or "" when the condition is not in it.
func negate(e ast.Expr, text func(ast.Node) string) string {
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.LAND { // De Morgan over a flat && chain
		l, r := negate(b.X, text), leaf(b.Y, text)
		if l == "" || r == "" {
			return ""
		}
		return l + " || " + r
	}
	return leaf(e, text)
}

func leaf(e ast.Expr, text func(ast.Node) string) string {
	isLen := func(x ast.Expr) bool {
		c, ok := x.(*ast.CallExpr)
		return ok && (text(c.Fun) == "len" || text(c.Fun) == "cap")
	}
	switch x := e.(type) {
	case *ast.UnaryExpr:
		if _, paren := x.X.(*ast.ParenExpr); x.Op == token.NOT && !paren {
			return text(x.X)
		}
	case *ast.Ident, *ast.SelectorExpr, *ast.CallExpr, *ast.IndexExpr:
		return "!" + text(e)
	case *ast.BinaryExpr:
		for _, o := range []ast.Expr{x.X, x.Y} {
			if b, ok := o.(*ast.BinaryExpr); ok && (flip[b.Op] != "" || b.Op == token.LAND || b.Op == token.LOR) {
				return ""
			}
		}
		switch {
		case x.Op == token.EQL || x.Op == token.NEQ:
			return text(x.X) + " " + flip[x.Op] + " " + text(x.Y)
		case x.Op == token.GTR && isLen(x.X) && text(x.Y) == "0":
			return text(x.X) + " == 0"
		case flip[x.Op] != "" && (isLen(x.X) || isLen(x.Y)):
			return text(x.X) + " " + flip[x.Op] + " " + text(x.Y)
		}
	}
	return ""
}

// declared adds every name a statement in list declares at that level (:=, var, const, type).
func declared(list []ast.Stmt, m map[string]bool) map[string]bool {
	for _, s := range list {
		if a, ok := s.(*ast.AssignStmt); ok && a.Tok == token.DEFINE {
			for _, l := range a.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
					m[id.Name] = true
				}
			}
		} else if d, ok := s.(*ast.DeclStmt); ok {
			for _, sp := range d.Decl.(*ast.GenDecl).Specs {
				if v, ok := sp.(*ast.ValueSpec); ok {
					for _, id := range v.Names {
						m[id.Name] = true
					}
				} else if t, ok := sp.(*ast.TypeSpec); ok {
					m[t.Name.Name] = true
				}
			}
		}
	}
	return m
}
EOF
go run "$T/guardscan.go" > "$T/queue.tsv" || echo 'SCAN FAILED: a red scan, not an empty queue'
# first candidate whose key is not already in this card's log
while IFS=$'\t' read -r key rest; do
  grep -qF "| $key |" .claude/skills/tidy/categories/guard-clause.md || { printf '%s\t%s\n' "$key" "$rest"; break; }
done < "$T/queue.tsv"
