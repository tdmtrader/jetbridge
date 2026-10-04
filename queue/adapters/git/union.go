package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// hookLists asks main's hook script "owned" and "union" (its keyed-line lists, none if it cannot
// answer); a list outside the repository, or one it also owns, is an error.
func hookLists(ctx context.Context, git func(...string) (string, error), ref, script string, limit time.Duration) (owned, union []string, err error) {
	owned, found, err := ownedOn(ctx, git, ref, script, limit)
	if err != nil || !found {
		return nil, nil, err
	}
	if union, _, err = askOn(ctx, git, ref, script, limit, "union"); errors.Is(err, errOutside) {
		return nil, nil, err
	}
	for _, p := range union {
		if slices.Contains(owned, p) {
			return nil, nil, fmt.Errorf("compose hook names %s both owned and a union list", p)
		}
	}
	return owned, union, nil
}

// unionMerge merges key by key each unmerged list of union whose base, ours and
// theirs allow it (keyedUnion), and returns the paths still unmerged.
func unionMerge(git func(...string) (string, error), dir string, unmerged, union []string) (rest []string, err error) {
	for _, p := range unmerged {
		if !slices.Contains(union, p) {
			rest = append(rest, p)
			continue
		}
		out, err := git("checkout-index", "--stage=all", "--temp", "--", p)
		names, _, _ := strings.Cut(out, "\t")
		var sides []string
		for _, n := range strings.Fields(names) {
			if n != "." {
				b, rerr := os.ReadFile(filepath.Join(dir, n))
				err, sides = errors.Join(err, rerr, os.Remove(filepath.Join(dir, n))), append(sides, string(b))
			}
		}
		merged, ok := "", false
		if fi, lerr := os.Lstat(filepath.Join(dir, p)); len(sides) == 3 && lerr == nil && fi.Mode().IsRegular() {
			merged, ok = keyedUnion(sides[0], sides[1], sides[2])
		}
		if err == nil && ok {
			if err = os.WriteFile(filepath.Join(dir, p), []byte(merged), 0o644); err == nil {
				_, err = git("add", "--", p)
			}
		} else if err == nil {
			rest = append(rest, p)
		}
		if err != nil {
			return nil, err
		}
	}
	return rest, nil
}

var (
	entryRe = regexp.MustCompile(`^("[^"\\]*"|[A-Za-z0-9_-]+)[ \t]*=`)
	bareRe  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	tableRe = regexp.MustCompile(`^\[[ \t]*|[ \t]*\]$`)
)

// listBlock is a run of entries under table sect, values "=" prefixed (a missing key reads "").
type listBlock struct {
	sect      string
	keys      []string
	val, line map[string]string
}

// parseList cuts a list of one-line `key = value` entries into its skeleton (comments, blank
// lines, headers and "\x01" for each block) and blocks; ok is false for any other line.
func parseList(s string) (skel []string, blocks []listBlock, ok bool) {
	prevEntry, sect := false, ""
	for _, ln := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		m := entryRe.FindStringSubmatch(ln)
		switch {
		case strings.Contains(ln, "\r") || strings.HasPrefix(ln, "[["):
			return nil, nil, false
		case strings.Trim(ln, " \t") == "" || strings.HasPrefix(strings.TrimLeft(ln, " \t"), "#") || strings.HasPrefix(ln, "["):
			if strings.HasPrefix(ln, "[") {
				sect, _, _ = strings.Cut(ln, "#")
				sect = tableRe.ReplaceAllString(strings.TrimRight(sect, " \t"), "")
			}
			skel, prevEntry = append(skel, ln), false
			continue
		case m == nil:
			return nil, nil, false
		}
		k, v := m[1], strings.TrimRight(strings.TrimLeft(strings.TrimLeft(ln[len(m[1]):], " \t")[1:], " \t"), " \t")
		if q := strings.Trim(k, `"`); k != q && bareRe.MatchString(q) {
			k = q
		}
		if v == "" || strings.HasPrefix(v, `"""`) || strings.HasPrefix(v, "'''") ||
			(v[0] == '[' && !strings.HasSuffix(v, "]")) || (v[0] == '{' && !strings.HasSuffix(v, "}")) {
			return nil, nil, false
		}
		if !prevEntry {
			skel, blocks = append(skel, "\x01"), append(blocks, listBlock{sect: sect, val: map[string]string{}, line: map[string]string{}})
		}
		b := &blocks[len(blocks)-1]
		if _, dup := b.val[k]; dup {
			return nil, nil, false
		}
		b.keys, b.val[k], b.line[k], prevEntry = append(b.keys, k), "="+v, ln, true
	}
	return skel, blocks, s != ""
}

// keyedUnion merges list o, ours a and theirs b key by key, as the old queue did. All share
// one skeleton; a key changed differently on both sides, or twice in one table, is not ok.
// Ours keeps its order; a key new in theirs follows the key it follows there.
func keyedUnion(o, a, b string) (string, bool) {
	var skel [3][]string
	var ls [3][]listBlock
	for i, s := range []string{o, a, b} {
		var ok bool
		if skel[i], ls[i], ok = parseList(s); !ok {
			return "", false
		}
	}
	if !slices.Equal(skel[0], skel[1]) || !slices.Equal(skel[0], skel[2]) {
		return "", false
	}
	var out []string
	seen, bi := map[[2]string]bool{}, 0
	for _, ln := range skel[1] {
		if ln != "\x01" {
			out = append(out, ln)
			continue
		}
		ba := ls[1][bi]
		keys, lines, ok := mergeBlock(ls[0][bi], ba, ls[2][bi])
		bi++
		if !ok {
			return "", false
		}
		for _, k := range keys {
			if seen[[2]string{ba.sect, k}] {
				return "", false
			}
			seen[[2]string{ba.sect, k}], out = true, append(out, lines[k])
		}
	}
	return strings.Join(out, "\n") + "\n", true
}

// mergeBlock returns the keys the merged block keeps, in order, and their lines.
func mergeBlock(o, a, b listBlock) (keys []string, lines map[string]string, ok bool) {
	lines, done := map[string]string{}, map[string]bool{}
	// take puts k's line at keys[at] unless k goes; it is false when k changed differently on both sides.
	take := func(k string, at int) bool {
		l, ok := "", true
		switch {
		case a.val[k] == b.val[k] || b.val[k] == o.val[k]:
			l = a.line[k]
		case a.val[k] == o.val[k]:
			l = b.line[k]
		default:
			ok = false
		}
		if done[k] = true; l != "" {
			keys, lines[k] = slices.Insert(keys, at, k), l
		}
		return ok
	}
	for _, k := range a.keys {
		if !take(k, len(keys)) {
			return nil, nil, false
		}
	}
	anchor := -1
	for _, k := range b.keys {
		if i := slices.Index(keys, k); i >= 0 {
			anchor = i
		}
		if done[k] {
			continue
		}
		for anchor+1 < len(keys) && b.line[keys[anchor+1]] == "" {
			anchor++
		}
		if n := len(keys); !take(k, anchor+1) {
			return nil, nil, false
		} else if len(keys) > n {
			anchor++
		}
	}
	return keys, lines, true
}
