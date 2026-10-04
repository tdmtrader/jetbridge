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

// unionMerge merges key by key each list of union that both own->HEAD and own->pick changed,
// conflicted or not, as the old queue's merge driver did. It returns the unmerged paths left,
// and conflict when a key changed differently on both sides or a list the change touched ends
// with one key twice in one table.
func unionMerge(git func(...string) (string, error), dir, own, pick string, unmerged, union []string) (rest []string, conflict bool, err error) {
	rest = slices.DeleteFunc(slices.Clone(unmerged), func(p string) bool { return slices.Contains(union, p) })
	for _, p := range union {
		ids := make([]string, 3)
		for i, rev := range []string{own, "HEAD", pick} {
			ids[i], _ = git("rev-parse", "-q", "--verify", rev+":"+p)
		}
		o, a, b := ids[0], ids[1], ids[2]
		fi, lerr := os.Lstat(filepath.Join(dir, p))
		regular, verdict, merged := lerr == nil && fi.Mode().IsRegular(), 2, ""
		if o != "" && a != "" && b != "" && o != a && o != b && a != b && regular {
			sides := make([]string, 3)
			for i, id := range ids {
				name, err := git("unpack-file", id)
				if err != nil {
					return nil, false, err
				}
				body, rerr := os.ReadFile(filepath.Join(dir, name))
				err, sides[i] = errors.Join(rerr, os.Remove(filepath.Join(dir, name))), string(body)
				if err != nil {
					return nil, false, err
				}
			}
			merged, verdict = keyedUnion(sides[0], sides[1], sides[2])
		}
		switch {
		case verdict == 1:
			return nil, true, nil
		case verdict == 0:
			if err = os.WriteFile(filepath.Join(dir, p), []byte(merged), 0o644); err == nil {
				_, err = git("add", "--", p)
			}
		case slices.Contains(unmerged, p):
			rest = append(rest, p)
		}
		if err != nil {
			return nil, false, err
		}
		if body, rerr := os.ReadFile(filepath.Join(dir, p)); b != o && regular && rerr == nil && dupKey(string(body)) {
			return nil, true, nil
		}
	}
	return rest, false, nil
}

// dupKey says whether one key is in one table twice.
func dupKey(s string) bool {
	seen, sect := map[[2]string]bool{}, ""
	for i, ln := range strings.Split(s, "\n") {
		if m := entryRe.FindStringSubmatch(ln); strings.HasPrefix(ln, "[[") {
			sect = fmt.Sprint(i)
		} else if strings.HasPrefix(ln, "[") {
			sect = tableName(ln)
		} else if m != nil {
			if seen[[2]string{sect, normKey(m[1])}] {
				return true
			}
			seen[[2]string{sect, normKey(m[1])}] = true
		}
	}
	return false
}

func tableName(ln string) string {
	t, _, _ := strings.Cut(ln, "#")
	return tableRe.ReplaceAllString(strings.TrimRight(t, " \t"), "")
}

// normKey spells a quoted key that could be bare as bare.
func normKey(k string) string {
	if q := strings.Trim(k, `"`); k != q && bareRe.MatchString(q) {
		return q
	}
	return k
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
				sect = tableName(ln)
			}
			skel, prevEntry = append(skel, ln), false
			continue
		case m == nil:
			return nil, nil, false
		}
		k, v := normKey(m[1]), strings.TrimRight(strings.TrimLeft(strings.TrimLeft(ln[len(m[1]):], " \t")[1:], " \t"), " \t")
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

// keyedUnion merges list o, ours a and theirs b key by key, as the old queue did: verdict 0.
// All share one skeleton, else 2; a key changed differently on both sides, or twice in one table, is 1.
// Ours keeps its order; a key new in theirs follows the key it follows there.
func keyedUnion(o, a, b string) (merged string, verdict int) {
	var skel [3][]string
	var ls [3][]listBlock
	for i, s := range []string{o, a, b} {
		var ok bool
		if skel[i], ls[i], ok = parseList(s); !ok {
			return "", 2
		}
	}
	if !slices.Equal(skel[0], skel[1]) || !slices.Equal(skel[0], skel[2]) {
		return "", 2
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
			return "", 1
		}
		for _, k := range keys {
			if seen[[2]string{ba.sect, k}] {
				return "", 1
			}
			seen[[2]string{ba.sect, k}], out = true, append(out, lines[k])
		}
	}
	return strings.Join(out, "\n") + "\n", 0
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
