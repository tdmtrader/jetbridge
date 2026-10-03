#!/usr/bin/env bash
# declaration-init scan (the card is declaration-init.md). Run from the repo root:
#   bash .claude/skills/tidy/categories/declaration-init.scan.sh
# Builds the declinit tool (go/types, via the root module's own golang.org/x/tools
# pin) in /tmp/tidy-declinit, writes every eligible instance, in pick order, to
# /tmp/tidy-declinit/queue.tsv, and prints the count and the first ten lines.
# The built tool also makes the edit and the gate's listing:
#   /tmp/tidy-declinit/declinit -apply KEY      /tmp/tidy-declinit/declinit -locals FILE
# A nonzero exit is a broken scan, never an empty queue.
set -euo pipefail
D=/tmp/tidy-declinit; rm -rf $D; mkdir -p $D
export GOTOOLCHAIN=$(go env GOVERSION)                 # the repo's toolchain (go1.25.6 on 2026-10-03): an older go/types rejects a newer go line
xt=$(go list -m -f '{{.Version}}' golang.org/x/tools)  # the root module's own pin (v0.45.0 on 2026-10-03), already in the module cache
cat > $D/main.go <<'EOF'
// declinit: the declaration-init scan. Run from the repo root.
//
//	declinit             one line per instance, in pick order
//	declinit -apply KEY  make the edit of the instance whose key is KEY, nothing else
//	declinit -locals F   every variable file F declares: top-level decl, name, type, enclosing scopes
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// A declaration is listed only if the host build (what the gate's `go vet ./...` compiles)
// type-checks its file and every build that type-checks the file lists it identically.
// The host and linux builds must type-check every package; windows has no verdict on
// the packages it cannot.
var builds = [][2]string{{runtime.GOOS, ""}, {"linux", "live,hangar_live"}, {"windows", ""}}

type edit struct {
	start, end int // byte offsets into the original file; start == end inserts
	text       string
}

type decl struct {
	form, name, label, file, typ, drop string
	line, at                           int
	del                                []int  // whole lines the edit deletes
	ed                                 []edit // byte edits: the target's rewrite, a move's insertion
}

type inst struct {
	rank      int
	test      bool
	file      string
	line      int
	key, text string
	del       []int
	ed        []edit
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "declinit: "+format+"\n", a...)
	os.Exit(1)
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal("%v", err)
	}
	switch {
	case len(os.Args) == 1:
		for _, in := range queue(root) {
			fmt.Println(in.text)
		}
	case len(os.Args) == 3 && os.Args[1] == "-apply":
		apply(root, os.Args[2])
	case len(os.Args) == 3 && os.Args[1] == "-locals":
		locals(root, os.Args[2])
	default:
		fatal("usage: declinit | declinit -apply KEY | declinit -locals FILE")
	}
}

func load(root, goos, tags, pattern string) []*packages.Package {
	pkgs, err := packages.Load(&packages.Config{Dir: root, Tests: true, Env: append(os.Environ(), "GOOS="+goos),
		BuildFlags: []string{"-tags=" + tags}, Mode: packages.NeedName | packages.NeedFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo}, pattern)
	if err != nil {
		fatal("GOOS=%s: %v", goos, err)
	}
	return pkgs
}

func queue(root string) []inst {
	cites := citations(root)
	var seen []map[string]bool
	var got []map[string]decl
	broken := false
	for _, b := range builds {
		s, g := map[string]bool{}, map[string]decl{}
		for _, p := range load(root, b[0], b[1], "./...") {
			if len(p.Errors) > 0 {
				msg := strings.Split(strings.TrimSpace(p.Errors[0].Msg), "\n")
				fmt.Fprintf(os.Stderr, "GOOS=%s -tags=%q: cannot type-check %s: %s\n", b[0], b[1], p.ID, msg[len(msg)-1])
				broken = broken || b[0] != "windows"
				continue
			}
			for _, f := range p.Syntax {
				rel, _ := filepath.Rel(root, p.Fset.Position(f.Pos()).Filename)
				if s[rel] || skip(rel, f) {
					continue
				}
				s[rel] = true
				for _, d := range scan(p, f, rel) {
					g[fmt.Sprint(rel, ":", d.line)] = d
				}
			}
		}
		seen, got = append(seen, s), append(got, g)
	}
	if broken {
		fatal("the host or linux build cannot type-check a package (above); this is a broken scan, not an empty queue")
	}
	groups := map[string][]decl{} // one instance = one target statement
	for k, d := range got[0] {
		agree := true
		for i := 1; i < len(builds); i++ {
			agree = agree && (!seen[i][d.file] || fmt.Sprintf("%+v", got[i][k]) == fmt.Sprintf("%+v", d))
		}
		if agree {
			t := fmt.Sprint(d.file, ":", d.at)
			groups[t] = append(groups[t], d)
		}
	}
	rank := map[string]int{"join": 0, "join-multi": 1, "join-typed": 2, "move": 3}
	var out []inst
	for _, ds := range groups {
		sort.Slice(ds, func(i, j int) bool { return ds[i].line < ds[j].line })
		d, ok := ds[0], true
		var names, lines, typs, drop []string
		var del []int
		var ed []edit
		for _, m := range ds {
			ok = ok && m.form == d.form // one target, two kinds of edit: not one instance
			names, lines, typs = append(names, m.name), append(lines, strconv.Itoa(m.line)), append(typs, m.typ)
			if m.drop != "" {
				drop = append(drop, strings.Split(m.drop, ",")...)
			}
			del = append(del, m.del...)
			for _, e := range m.ed {
				merged := false
				for i := range ed {
					if ed[i].start == e.start && ed[i].end == e.end {
						merged = true
						if e.start == e.end {
							ed[i].text += e.text // a move's var lines keep their order
						} else {
							ok = ok && ed[i].text == e.text
						}
					}
				}
				if !merged {
					ed = append(ed, e)
				}
			}
		}
		slices.Sort(del)
		slices.Sort(drop)
		del, drop = slices.Compact(del), slices.Compact(drop)
		if !ok || cited(cites, d.file, del[0]) {
			continue
		}
		key := fmt.Sprintf("%s:%s@%s", strings.Join(names, ","), d.label, d.file)
		out = append(out, inst{rank[d.form], strings.HasSuffix(d.file, "_test.go"), d.file, d.line, key,
			fmt.Sprintf("%s\t%s\tdecl=%s:%s\tat=%s:%d\ttype=%s\tdrop=%s", d.form, key, d.file, strings.Join(lines, ","),
				d.file, d.at, strings.Join(typs, ","), strings.Join(drop, ",")), del, ed})
	}
	count := map[string]int{}
	for _, in := range out {
		count[in.key]++
	}
	var res []inst
	for _, in := range out {
		if count[in.key] == 1 { // a key two instances would share names neither
			res = append(res, in)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.test != b.test {
			return !a.test
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.line < b.line
	})
	return res
}

// citations: every `<path>.go:<N>[-<M>]` or `<path>.go#L<N>[-L<M>]` in a tracked text file,
// keyed by the path without leading `./` and `../`, with the last line it names.
func citations(root string) map[string][]int {
	ls, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		fatal("git ls-files: %v", err)
	}
	re, res := regexp.MustCompile(`([A-Za-z0-9_./-]*\.go)(?::|#L)([0-9]+)(?:-L?([0-9]+))?`), map[string][]int{}
	for _, name := range strings.Split(strings.TrimRight(string(ls), "\x00"), "\x00") {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			continue
		}
		for _, m := range re.FindAllSubmatch(b, -1) {
			n, _ := strconv.Atoi(string(m[2]))
			if e, err := strconv.Atoi(string(m[3])); err == nil {
				n = max(n, e)
			}
			p := strings.TrimLeft(string(m[1]), "./")
			res[p] = append(res[p], n)
		}
	}
	return res
}

// cited: some file names a line of rel at or below the first line the edit touches.
func cited(cites map[string][]int, rel string, first int) bool {
	for p, ns := range cites {
		if strings.HasSuffix("/"+rel, "/"+p) {
			for _, n := range ns {
				if n >= first {
					return true
				}
			}
		}
	}
	return false
}

func skip(rel string, f *ast.File) bool {
	for _, p := range []string{"..", "atc/db/migration/migrations/", "atc/worker/jetbridge/brine/",
		"topgun/", "testflight/", "integration/", "testhelpers/otel/"} { // the last four: no local tier runs them
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	for _, d := range strings.Split(filepath.Dir(rel), "/") {
		if d == "vendor" || d == "node_modules" || strings.HasSuffix(d, "fakes") {
			return true
		}
	}
	return ast.IsGenerated(f)
}

// top names a top-level declaration: Recv.Method, Func, or the first name of a
// package-level var (`_` for `var _ = Describe(...)`).
func top(d ast.Decl) string {
	if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil {
		for t := fd.Recv.List[0].Type; ; {
			switch x := t.(type) {
			case *ast.StarExpr:
				t = x.X
			case *ast.IndexExpr:
				t = x.X
			case *ast.IndexListExpr:
				t = x.X
			case *ast.Ident:
				return x.Name + "." + fd.Name.Name
			}
		}
	} else if ok {
		return fd.Name.Name
	} else if gd, ok := d.(*ast.GenDecl); ok && len(gd.Specs) > 0 {
		if vs, ok := gd.Specs[0].(*ast.ValueSpec); ok {
			return vs.Names[0].Name
		}
	}
	return "?"
}

// walk calls visit for every statement list under n with its innermost function and a
// label: the enclosing top-level name, or It("...")/t.Run("...") for a function literal
// passed to a call whose first argument is a string literal (whitespace collapsed, `|` and a
// backquote written `/` and `'`).
func walk(n, fn ast.Node, label string, visit func([]ast.Stmt, ast.Node, string)) {
	labels := map[ast.Node]string{}
	ast.Inspect(n, func(m ast.Node) bool {
		switch m := m.(type) {
		case *ast.CallExpr:
			callee := m.Fun
			if sel, ok := callee.(*ast.SelectorExpr); ok {
				callee = sel.Sel
			}
			if id, ok := callee.(*ast.Ident); ok && len(m.Args) > 0 {
				if lit, ok := m.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						s = strings.NewReplacer("|", "/", "`", "'").Replace(strings.Join(strings.Fields(s), " "))
						for _, a := range m.Args {
							labels[a] = id.Name + "(" + strconv.Quote(s) + ")"
						}
					}
				}
			}
		case *ast.FuncLit, *ast.FuncDecl:
			if m != n {
				l, ok := labels[m]
				walk(m, m, map[bool]string{true: l, false: label}[ok], visit)
				return false
			}
		case *ast.BlockStmt:
			visit(m.List, fn, label)
		case *ast.CaseClause:
			visit(m.Body, fn, label)
		case *ast.CommClause:
			visit(m.Body, fn, label)
		}
		return true
	})
}

func has(n ast.Node, pred func(ast.Node) bool) bool {
	hit := false
	ast.Inspect(n, func(n ast.Node) bool {
		hit = hit || n != nil && pred(n)
		return !hit
	})
	return hit
}

func mentions(n ast.Node, info *types.Info, obj types.Object) bool {
	return has(n, func(n ast.Node) bool { id, ok := n.(*ast.Ident); return ok && info.Uses[id] == obj })
}

func scan(p *packages.Package, f *ast.File, rel string) (res []decl) {
	info, tf := p.TypesInfo, p.Fset.File(f.Pos())
	line, off := tf.Line, tf.Offset
	src, err := os.ReadFile(tf.Name())
	if fmted, ferr := format.Source(src); err != nil || ferr != nil || !bytes.Equal(fmted, src) {
		return nil // not gofmt-clean: the edit's gofmt would touch other lines
	}
	for _, d := range f.Decls {
		walk(d, d, top(d), func(list []ast.Stmt, fn ast.Node, label string) {
			if _, isGen := fn.(*ast.GenDecl); isGen || has(fn, func(n ast.Node) bool {
				b, ok := n.(*ast.BranchStmt)
				return ok && b.Tok == token.GOTO
			}) {
				return
			}
			for i, s := range list {
				if !isVar(s) || s.(*ast.DeclStmt).Decl.(*ast.GenDecl).Lparen.IsValid() {
					continue
				}
				vs := s.(*ast.DeclStmt).Decl.(*ast.GenDecl).Specs[0].(*ast.ValueSpec)
				obj := info.Defs[vs.Names[0]]
				if len(vs.Names) != 1 || len(vs.Values) != 0 || obj == nil || line(s.Pos()) != line(s.End()) ||
					commented(f, tf, src, list, i) {
					continue
				}
				j := i + 1
				for j < len(list) && !mentions(list[j], info, obj) {
					j++
				}
				if j == len(list) || !typeStable(p, vs.Type, list[j].Pos()) {
					continue
				}
				as, ok := list[j].(*ast.AssignStmt)
				if !ok || !assigns(as, info, obj) {
					continue // the target is not a plain assignment of x: x's zero value may be read
				}
				d := decl{form: join(p, as, obj), name: obj.Name(), label: label, file: rel,
					typ: tstr(obj.Type(), types.RelativeTo(p.Types)), line: line(s.Pos()), at: line(as.Pos()),
					del: []int{line(s.Pos())}}
				switch d.form {
				case "join", "join-multi":
					paths, lines, ok := drops(p, f, vs.Type)
					if !ok {
						continue
					}
					d.drop, d.del = paths, append(d.del, lines...)
					if as.Tok == token.ASSIGN {
						d.ed = []edit{{off(as.TokPos), off(as.TokPos) + 1, ":="}}
					}
				case "join-typed":
					if comments(f, as.Pos(), as.Rhs[0].Pos()) {
						continue
					}
					d.ed = []edit{{off(as.Pos()), off(as.Rhs[0].Pos()),
						"var " + obj.Name() + " " + string(src[off(vs.Type.Pos()):off(vs.Type.End())]) + " = "}}
				case "":
					if j == i+1 || onlyDecls(list[i+1:j]) || feeds(list[j-1], as, info) {
						continue
					}
					at := line(as.Pos()) // above the target's own-line comment, if it has one
					for _, cg := range f.Comments {
						if line(cg.End()) == at-1 && ownLine(tf, src, cg) {
							at = line(cg.Pos())
						}
					}
					d.form = "move"
					d.ed = []edit{{off(tf.LineStart(at)), off(tf.LineStart(at)),
						string(src[off(tf.LineStart(line(s.Pos()))):off(tf.LineStart(line(s.Pos())+1))])}}
				default: // "nil": the target reassigns the zero value
					continue
				}
				res = append(res, d)
			}
		})
	}
	return res
}

func isVar(s ast.Stmt) bool {
	ds, ok := s.(*ast.DeclStmt)
	return ok && ds.Decl.(*ast.GenDecl).Tok == token.VAR
}

// commented: the run of `var` statements on consecutive lines holding list[i] has a
// comment on one of its lines, or an own-line comment ending on the line above it.
func commented(f *ast.File, tf *token.File, src []byte, list []ast.Stmt, i int) bool {
	a, b := i, i
	for a > 0 && isVar(list[a-1]) && tf.Line(list[a-1].End()) == tf.Line(list[a].Pos())-1 {
		a--
	}
	for b < len(list)-1 && isVar(list[b+1]) && tf.Line(list[b+1].Pos()) == tf.Line(list[b].End())+1 {
		b++
	}
	first, last := tf.Line(list[a].Pos()), tf.Line(list[b].End())
	for _, cg := range f.Comments {
		s, e := tf.Line(cg.Pos()), tf.Line(cg.End())
		if s <= last && e >= first || e == first-1 && ownLine(tf, src, cg) {
			return true
		}
	}
	return false
}

func ownLine(tf *token.File, src []byte, cg *ast.CommentGroup) bool {
	return strings.TrimSpace(string(src[tf.Offset(tf.LineStart(tf.Line(cg.Pos()))):tf.Offset(cg.Pos())])) == ""
}

func comments(f *ast.File, from, to token.Pos) bool {
	for _, cg := range f.Comments {
		if cg.Pos() < to && cg.End() > from {
			return true
		}
	}
	return false
}

// assigns: as is `=` or `:=` with x alone on one side of it, a bare name on the left,
// and x named nowhere else in it.
func assigns(as *ast.AssignStmt, info *types.Info, obj types.Object) bool {
	if as.Tok != token.ASSIGN && as.Tok != token.DEFINE {
		return false
	}
	n := 0
	for _, l := range as.Lhs {
		if id, ok := l.(*ast.Ident); ok && info.Uses[id] == obj {
			n++
		} else if mentions(l, info, obj) {
			return false
		}
	}
	return n == 1 && !mentions(&ast.CompositeLit{Elts: as.Rhs}, info, obj)
}

// join says how `var x T` merges into as, an assignment of x: "join" (x := e),
// "join-multi" (x, y := f()), "join-typed" (var x T = e), "nil" (x = nil), or "" (cannot).
func join(p *packages.Package, as *ast.AssignStmt, obj types.Object) string {
	info, q := p.TypesInfo, types.RelativeTo(p.Types)
	at := -1
	for i, l := range as.Lhs {
		id, ok := l.(*ast.Ident)
		switch {
		case ok && info.Uses[id] == obj:
			at = i
		case ok && (id.Name == "_" || info.Defs[id] != nil):
		case !ok || info.Uses[id] == nil || info.Uses[id].Parent() != obj.Parent():
			return "" // `:=` would declare a new variable here instead of assigning this one
		}
	}
	var inferred types.Type // the type `:=` would give x
	if len(as.Lhs) > len(as.Rhs) {
		_, call := ast.Unparen(as.Rhs[0]).(*ast.CallExpr)
		if tup, ok := info.Types[as.Rhs[0]].Type.(*types.Tuple); ok && (call || at == 0) {
			inferred = tup.At(at).Type()
		} else if ok {
			inferred = types.Typ[types.Bool] // comma-ok
		}
	} else if ti := (&types.Info{Types: map[ast.Expr]types.TypeAndValue{}}); types.CheckExpr(p.Fset, p.Types,
		as.Rhs[at].Pos(), as.Rhs[at], ti) == nil {
		inferred = types.Default(ti.Types[as.Rhs[at]].Type) // untyped constants and comparisons take their default
	}
	id, _ := ast.Unparen(as.Rhs[0]).(*ast.Ident)
	switch same := inferred != nil && types.Identical(inferred, obj.Type()) && tstr(inferred, q) == tstr(obj.Type(), q); {
	case same && len(as.Lhs) == 1:
		return "join"
	case same:
		return "join-multi"
	case len(as.Lhs) == 1 && id != nil && info.Uses[id] == types.Universe.Lookup("nil"):
		return "nil"
	case len(as.Lhs) == 1:
		return "join-typed"
	}
	return ""
}

// drops lists the imports deleting texpr leaves unused in f and the lines that delete
// them; ok is false when one is imported by no other file of the package (its init
// could leave the binary), carries a comment, or texpr names a dot-imported type.
func drops(p *packages.Package, f *ast.File, texpr ast.Expr) (string, []int, bool) {
	inExpr, total, ok := map[types.Object]int{}, map[types.Object]int{}, true
	ast.Inspect(texpr, func(n ast.Node) bool {
		if sel, isSel := n.(*ast.SelectorExpr); isSel {
			if x, isId := sel.X.(*ast.Ident); !isId {
				ok = false
			} else if pn, isPkg := p.TypesInfo.Uses[x].(*types.PkgName); isPkg {
				inExpr[pn]++
			}
			return false
		}
		if id, isId := n.(*ast.Ident); isId {
			if o := p.TypesInfo.Uses[id]; o != nil && o.Pkg() != nil && o.Pkg() != p.Types {
				ok = false
			}
		}
		return true
	})
	for _, o := range p.TypesInfo.Uses {
		total[o]++
	}
	tf := p.Fset.File(f.Pos())
	var paths []string
	var lines []int
	for o, n := range inExpr {
		path := o.(*types.PkgName).Imported().Path()
		if n != total[o] {
			continue
		}
		kept := false
		for _, g := range p.Syntax {
			for _, im := range g.Imports {
				kept = kept || g != f && strings.Trim(im.Path.Value, "`\"") == path
			}
		}
		ok = ok && kept
		paths = append(paths, path)
		for _, d := range f.Decls {
			gd, isGen := d.(*ast.GenDecl)
			if !isGen || gd.Tok != token.IMPORT {
				continue
			}
			for _, sp := range gd.Specs {
				if im := sp.(*ast.ImportSpec); strings.Trim(im.Path.Value, "`\"") == path {
					ok = ok && im.Doc == nil && im.Comment == nil && gd.Doc == nil
					from, to := tf.Line(im.Pos()), tf.Line(im.End())
					if len(gd.Specs) == 1 {
						from, to = tf.Line(gd.Pos()), tf.Line(gd.End())
					}
					for l := from; l <= to; l++ {
						lines = append(lines, l)
					}
				}
			}
		}
	}
	sort.Strings(paths)
	return strings.Join(paths, ","), lines, ok
}

// feeds: prev writes a name the target reads, so a `var` between them would split a pair.
func feeds(prev ast.Stmt, as *ast.AssignStmt, info *types.Info) bool {
	var written []ast.Expr
	switch s := prev.(type) {
	case *ast.AssignStmt:
		written = s.Lhs
	case *ast.IncDecStmt:
		written = []ast.Expr{s.X}
	case *ast.DeclStmt:
		for _, sp := range s.Decl.(*ast.GenDecl).Specs {
			if vs, ok := sp.(*ast.ValueSpec); ok {
				for _, n := range vs.Names {
					written = append(written, n)
				}
			}
		}
	}
	for _, w := range written {
		if id, ok := ast.Unparen(w).(*ast.Ident); ok {
			o := info.Defs[id]
			if o == nil {
				o = info.Uses[id]
			}
			if o != nil && mentions(as, info, o) {
				return true
			}
		}
	}
	return false
}

// tstr spells a type; a function type's parameter names are not part of it.
func tstr(t types.Type, q types.Qualifier) string {
	if sig, ok := t.(*types.Signature); ok {
		strip := func(tu *types.Tuple) *types.Tuple {
			var vs []*types.Var
			for i := range tu.Len() {
				vs = append(vs, types.NewParam(0, nil, "", tu.At(i).Type()))
			}
			return types.NewTuple(vs...)
		}
		t = types.NewSignatureType(nil, nil, nil, strip(sig.Params()), strip(sig.Results()), sig.Variadic())
	}
	return types.TypeString(t, q)
}

// typeStable: every name in the type expression means the same thing at pos.
func typeStable(p *packages.Package, texpr ast.Expr, pos token.Pos) bool {
	scope, ok := p.Types.Scope().Innermost(pos), true
	ast.Inspect(texpr, func(n ast.Node) bool {
		sel, isSel := n.(*ast.SelectorExpr)
		if isSel {
			n = sel.X // pkg.T: only the package name is scoped
		}
		if id, isId := n.(*ast.Ident); isId && p.TypesInfo.Uses[id] != nil {
			_, o := scope.LookupParent(id.Name, pos)
			ok = ok && o == p.TypesInfo.Uses[id]
		}
		return !isSel
	})
	return ok
}

func onlyDecls(between []ast.Stmt) bool {
	for _, s := range between {
		if _, ok := s.(*ast.DeclStmt); !ok {
			return false
		}
	}
	return true
}

// apply makes the instance's edit and nothing else.
func apply(root, key string) {
	for _, in := range queue(root) {
		if in.key == key {
			path := filepath.Join(root, in.file)
			src, err := os.ReadFile(path)
			if err != nil {
				fatal("%v", err)
			}
			res, err := patch(src, in)
			if err != nil {
				fatal("%s: %v", key, err)
			}
			st, _ := os.Stat(path)
			if err := os.WriteFile(path, res, st.Mode()); err != nil {
				fatal("%v", err)
			}
			fmt.Println(in.text)
			return
		}
	}
	fatal("%s is not in today's queue", key)
}

// patch deletes the instance's lines (and the blank line a deletion would leave after
// `{`, `(`, `:` or another blank line), makes its byte edits, and gofmts the result.
func patch(src []byte, in inst) ([]byte, error) {
	starts := []int{0} // starts[l-1] is the offset of line l; one extra entry for EOF
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	if starts[len(starts)-1] != len(src) {
		starts = append(starts, len(src))
	}
	text := func(l int) string { return strings.TrimSpace(string(src[starts[l-1]:starts[l]])) }
	eds := append([]edit(nil), in.ed...)
	for i := 0; i < len(in.del); {
		a, b := in.del[i], in.del[i]
		for i++; i < len(in.del) && in.del[i] == b+1; i++ {
			b++
		}
		if prev := text(max(a-1, 1)); b+1 < len(starts) && text(b+1) == "" && (a == 1 || prev == "" ||
			strings.HasSuffix(prev, "{") || strings.HasSuffix(prev, "(") || strings.HasSuffix(prev, ":")) {
			b++
		}
		eds = append(eds, edit{starts[a-1], starts[b], ""})
	}
	sort.SliceStable(eds, func(i, j int) bool {
		if eds[i].start != eds[j].start {
			return eds[i].start < eds[j].start
		}
		return eds[i].start == eds[i].end // an insertion goes before a deletion at the same offset
	})
	var out []byte
	pos := 0
	for _, e := range eds {
		if e.start < pos {
			return nil, fmt.Errorf("overlapping edits")
		}
		out = append(append(out, src[pos:e.start]...), e.text...)
		pos = e.end
	}
	return format.Source(append(out, src[pos:]...))
}

// locals prints every variable file declares: its top-level declaration, name, type,
// and the chain of enclosing scopes, sorted. The gate diffs it before and after the edit.
func locals(root, file string) {
	for _, p := range load(root, runtime.GOOS, "", "file="+file) {
		for _, f := range p.Syntax {
			if rel, _ := filepath.Rel(root, p.Fset.Position(f.Pos()).Filename); rel != file || len(p.Errors) > 0 {
				continue
			}
			var out []string
			for _, d := range f.Decls {
				var stack []string
				ast.Inspect(d, func(n ast.Node) bool {
					if n == nil {
						stack = stack[:len(stack)-1]
						return true
					}
					if id, ok := n.(*ast.Ident); ok {
						if v, ok := p.TypesInfo.Defs[id].(*types.Var); ok && !v.IsField() && v.Name() != "_" {
							var scopes []string
							for _, k := range stack {
								if strings.HasPrefix(k, "Func") || strings.HasSuffix(k, "Clause") ||
									strings.HasSuffix(k, "Stmt") && k != "DeclStmt" && k != "AssignStmt" {
									scopes = append(scopes, k)
								}
							}
							out = append(out, fmt.Sprintf("%s\t%s\t%s\t%s", top(d), v.Name(),
								tstr(v.Type(), types.RelativeTo(p.Types)), strings.Join(scopes, "/")))
						}
					}
					stack = append(stack, strings.TrimPrefix(reflect.TypeOf(n).String(), "*ast."))
					return true
				})
			}
			sort.Strings(out)
			fmt.Println(strings.Join(out, "\n"))
			return
		}
	}
	fatal("the host build does not type-check %s", file)
}
EOF
(cd $D && go mod init declinit && go get golang.org/x/tools/go/packages@$xt && go build -o declinit .) >$D/build.log 2>&1 \
  || { cat $D/build.log; echo 'declinit: the tool did not build'; exit 1; }
$D/declinit > $D/queue.tsv
wc -l < $D/queue.tsv; head $D/queue.tsv
