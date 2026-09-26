package implement

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/capture"
	"github.com/pmezard/go-difflib/difflib"
)

// MaxPatchBytes bounds a published patch.
const MaxPatchBytes = 32 << 20

// TreeFile is one file of a tree: its bytes and its Git mode.
type TreeFile struct {
	Data []byte
	Mode string
}

// Tree is a set of files keyed by slash-separated repository path.
type Tree map[string]TreeFile

// ChangedFile names one path the patch touches.
type ChangedFile struct {
	Path string `json:"path"`
	// Status is added, modified or deleted.
	Status string `json:"status"`
}

// patchablePath accepts only names the patch format carries unquoted and that
// git apply will write: no control characters or quotes, and nothing under a
// .git directory.
func patchablePath(name string) bool {
	if !capture.SafePath(name) || strings.ContainsRune(name, '"') {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// Diff returns the unified diff that turns base into edited, in the form
// ParsePatch accepts: a/ and b/ prefixes, one section per path in path
// order, three lines of context. The edit-only policy cannot express binary
// content, mode or symlink changes, so Diff refuses them rather than emit a
// patch the developer's git would apply differently.
func Diff(base, edited Tree) ([]byte, []ChangedFile, error) {
	paths := map[string]bool{}
	for p := range base {
		paths[p] = true
	}
	for p := range edited {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	var out bytes.Buffer
	changes := []ChangedFile{}
	for _, p := range sorted {
		old, inBase := base[p]
		now, inEdited := edited[p]
		if inBase && inEdited && bytes.Equal(old.Data, now.Data) && (old.Mode == now.Mode || (old.Mode == "120000" && now.Mode == "100644")) {
			continue
		}
		if !patchablePath(p) {
			return nil, nil, fmt.Errorf("unsupported path in change: %q", p)
		}
		if (inBase && old.Mode == "120000") || (inEdited && now.Mode == "120000") {
			return nil, nil, fmt.Errorf("symlink changes are unsupported: %s", p)
		}
		if (inBase && !capture.Text(old.Data)) || (inEdited && !capture.Text(now.Data)) {
			return nil, nil, fmt.Errorf("binary changes are unsupported: %s", p)
		}
		switch {
		case inBase && inEdited:
			if old.Mode != now.Mode {
				return nil, nil, fmt.Errorf("mode changes are unsupported: %s", p)
			}
			writeSection(&out, p, "modified", old.Mode, old.Data, now.Data)
			changes = append(changes, ChangedFile{p, "modified"})
		case inEdited:
			if now.Mode != "100644" {
				return nil, nil, fmt.Errorf("mode changes are unsupported: %s", p)
			}
			writeSection(&out, p, "added", now.Mode, nil, now.Data)
			changes = append(changes, ChangedFile{p, "added"})
		default:
			writeSection(&out, p, "deleted", old.Mode, old.Data, nil)
			changes = append(changes, ChangedFile{p, "deleted"})
		}
		if out.Len() > MaxPatchBytes {
			return nil, nil, errors.New("change exceeds the 32 MiB patch limit")
		}
	}
	return out.Bytes(), changes, nil
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Git appends a tab after a name containing a space so traditional patch
// tools can find its end; git apply accepts it.
func headerName(prefix, p string) string {
	if strings.Contains(p, " ") {
		return prefix + p + "\t"
	}
	return prefix + p
}

func writeSection(out *bytes.Buffer, p, status, mode string, old, now []byte) {
	fmt.Fprintf(out, "diff --git a/%s b/%s\n", p, p)
	switch status {
	case "added":
		fmt.Fprintf(out, "new file mode %s\n", mode)
	case "deleted":
		fmt.Fprintf(out, "deleted file mode %s\n", mode)
	}
	a, b := splitLines(old), splitLines(now)
	if len(a) == 0 && len(b) == 0 {
		return // an empty file created or deleted has no hunk
	}
	from, to := headerName("a/", p), headerName("b/", p)
	if status == "added" {
		from = "/dev/null"
	}
	if status == "deleted" {
		to = "/dev/null"
	}
	fmt.Fprintf(out, "--- %s\n+++ %s\n", from, to)
	for _, group := range difflib.NewMatcher(a, b).GetGroupedOpCodes(3) {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(out, "@@ -%s +%s @@\n", unifiedRange(first.I1, last.I2), unifiedRange(first.J1, last.J2))
		for _, c := range group {
			if c.Tag == 'e' {
				writeLines(out, ' ', a[c.I1:c.I2])
				continue
			}
			if c.Tag == 'r' || c.Tag == 'd' {
				writeLines(out, '-', a[c.I1:c.I2])
			}
			if c.Tag == 'r' || c.Tag == 'i' {
				writeLines(out, '+', b[c.J1:c.J2])
			}
		}
	}
}

func writeLines(out *bytes.Buffer, op byte, lines []string) {
	for _, line := range lines {
		out.WriteByte(op)
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

// unifiedRange formats a zero-based half-open range as a unified diff range.
func unifiedRange(start, stop int) string {
	beginning, length := start+1, stop-start
	if length == 1 {
		return strconv.Itoa(beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}

type patchLine struct {
	op   byte
	text string // including its newline, unless it ends the file without one
}

type hunk struct {
	oldStart, oldLines, newStart, newLines int
	lines                                  []patchLine
}

type patchSection struct {
	ChangedFile
	mode  string
	hunks []hunk
}

// ParsePatch reads a patch in exactly the form Diff writes and nothing else:
// sections in strictly increasing path order, each touching one patchable
// path, with hunks whose counts match their lines. It does not need the base.
func ParsePatch(patch []byte) ([]patchSection, error) {
	if len(patch) > MaxPatchBytes || !capture.Text(patch) {
		return nil, errors.New("patch is not bounded UTF-8 text")
	}
	if len(patch) > 0 && patch[len(patch)-1] != '\n' {
		return nil, errors.New("patch does not end with a newline")
	}
	lines := splitLines(patch)
	i := 0
	next := func() (string, bool) {
		if i >= len(lines) {
			return "", false
		}
		i++
		return strings.TrimSuffix(lines[i-1], "\n"), true
	}
	peek := func() string {
		if i >= len(lines) {
			return ""
		}
		return strings.TrimSuffix(lines[i], "\n")
	}
	var sections []patchSection
	for i < len(lines) {
		line, _ := next()
		rest, ok := strings.CutPrefix(line, "diff --git a/")
		if !ok || len(rest) < 4 || (len(rest)-3)%2 != 0 {
			return nil, errors.New("malformed patch section header")
		}
		p := rest[:(len(rest)-3)/2]
		if rest != p+" b/"+p || !patchablePath(p) {
			return nil, errors.New("malformed patch section header")
		}
		if len(sections) > 0 && sections[len(sections)-1].Path >= p {
			return nil, errors.New("patch sections are not in path order")
		}
		s := patchSection{ChangedFile: ChangedFile{Path: p, Status: "modified"}}
		if mode, ok := strings.CutPrefix(peek(), "new file mode "); ok {
			next()
			if mode != "100644" {
				return nil, errors.New("patch creates a file with an unsupported mode")
			}
			s.Status, s.mode = "added", mode
		} else if mode, ok := strings.CutPrefix(peek(), "deleted file mode "); ok {
			next()
			if mode != "100644" && mode != "100755" {
				return nil, errors.New("patch deletes a file with an unsupported mode")
			}
			s.Status, s.mode = "deleted", mode
		}
		if strings.HasPrefix(peek(), "--- ") {
			from, _ := next()
			to, _ := next()
			wantFrom, wantTo := "--- "+headerName("a/", p), "+++ "+headerName("b/", p)
			if s.Status == "added" {
				wantFrom = "--- /dev/null"
			}
			if s.Status == "deleted" {
				wantTo = "+++ /dev/null"
			}
			if from != wantFrom || to != wantTo {
				return nil, fmt.Errorf("malformed file header for %s", p)
			}
			for strings.HasPrefix(peek(), "@@ ") {
				h, err := parseHunk(next, peek)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", p, err)
				}
				s.hunks = append(s.hunks, h)
			}
			if len(s.hunks) == 0 {
				return nil, fmt.Errorf("%s: file header without hunks", p)
			}
		} else if s.Status == "modified" {
			return nil, fmt.Errorf("%s: modification without hunks", p)
		}
		switch s.Status {
		case "added", "deleted":
			if len(s.hunks) > 1 || (len(s.hunks) == 1 && ((s.Status == "added" && s.hunks[0].oldLines != 0) || (s.Status == "deleted" && s.hunks[0].newLines != 0))) {
				return nil, fmt.Errorf("%s: malformed %s file", p, s.Status)
			}
		}
		sections = append(sections, s)
	}
	return sections, nil
}

func parseRange(s string) (int, int, error) {
	start, count, found := strings.Cut(s, ",")
	a, err := strconv.Atoi(start)
	if err != nil || a < 0 || strconv.Itoa(a) != start {
		return 0, 0, errors.New("malformed hunk range")
	}
	n := 1
	if found {
		if n, err = strconv.Atoi(count); err != nil || n < 0 || strconv.Itoa(n) != count {
			return 0, 0, errors.New("malformed hunk range")
		}
	}
	return a, n, nil
}

func parseHunk(next func() (string, bool), peek func() string) (hunk, error) {
	header, _ := next()
	fields := strings.Fields(header)
	if len(fields) != 4 || fields[0] != "@@" || fields[3] != "@@" || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return hunk{}, errors.New("malformed hunk header")
	}
	var h hunk
	var err error
	if h.oldStart, h.oldLines, err = parseRange(fields[1][1:]); err != nil {
		return h, err
	}
	if h.newStart, h.newLines, err = parseRange(fields[2][1:]); err != nil {
		return h, err
	}
	if h.oldLines+h.newLines == 0 {
		return h, errors.New("empty hunk")
	}
	oldSeen, newSeen := 0, 0
	for oldSeen < h.oldLines || newSeen < h.newLines {
		line, ok := next()
		if !ok || line == "" {
			return h, errors.New("hunk is shorter than its header")
		}
		op := line[0]
		switch op {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		default:
			return h, errors.New("malformed hunk line")
		}
		text := line[1:] + "\n"
		if peek() == `\ No newline at end of file` {
			next()
			text = line[1:]
		}
		h.lines = append(h.lines, patchLine{op, text})
	}
	if oldSeen != h.oldLines || newSeen != h.newLines {
		return h, errors.New("hunk counts do not match its lines")
	}
	return h, nil
}

// ApplyPatch applies parsed sections to base with no fuzz: every context and
// removed line must match base exactly at the stated position. It returns
// the resulting tree.
func ApplyPatch(base Tree, sections []patchSection) (Tree, error) {
	result := make(Tree, len(base))
	for p, f := range base {
		result[p] = f
	}
	for _, s := range sections {
		old, exists := base[s.Path]
		switch s.Status {
		case "added":
			if exists {
				return nil, fmt.Errorf("patch adds %s, which exists at base", s.Path)
			}
			var data strings.Builder
			for _, h := range s.hunks {
				for _, l := range h.lines {
					data.WriteString(l.text)
				}
			}
			result[s.Path] = TreeFile{Data: []byte(data.String()), Mode: s.mode}
			continue
		case "deleted":
			if !exists || old.Mode != s.mode {
				return nil, fmt.Errorf("patch deletes %s, which does not match base", s.Path)
			}
		default:
			if !exists || old.Mode == "120000" {
				return nil, fmt.Errorf("patch modifies %s, which is not a base file", s.Path)
			}
		}
		a := splitLines(old.Data)
		var out []string
		cursor := 0
		for _, h := range s.hunks {
			start := h.oldStart - 1
			if h.oldLines == 0 {
				start = h.oldStart
			}
			if start < cursor || start > len(a) {
				return nil, fmt.Errorf("%s: hunk outside base", s.Path)
			}
			out = append(out, a[cursor:start]...)
			cursor = start
			for _, l := range h.lines {
				if l.op != '+' {
					if cursor >= len(a) || a[cursor] != l.text {
						return nil, fmt.Errorf("%s: patch does not apply to base", s.Path)
					}
					cursor++
				}
				if l.op != '-' {
					out = append(out, l.text)
				}
			}
		}
		out = append(out, a[cursor:]...)
		if s.Status == "deleted" {
			if len(out) != 0 {
				return nil, fmt.Errorf("%s: deletion does not remove the whole base file", s.Path)
			}
			delete(result, s.Path)
			continue
		}
		result[s.Path] = TreeFile{Data: []byte(strings.Join(out, "")), Mode: old.Mode}
	}
	return result, nil
}

// VerifyPatch re-derives the change set from patch and proves that applying
// it to base yields exactly edited.
func VerifyPatch(base, edited Tree, patch []byte) ([]ChangedFile, error) {
	sections, err := ParsePatch(patch)
	if err != nil {
		return nil, err
	}
	result, err := ApplyPatch(base, sections)
	if err != nil {
		return nil, err
	}
	if len(result) != len(edited) {
		return nil, errors.New("patch does not reproduce the edited workspace")
	}
	for p, f := range edited {
		got, ok := result[p]
		if !ok || !bytes.Equal(got.Data, f.Data) || !sameMode(got.Mode, f.Mode) {
			return nil, errors.New("patch does not reproduce the edited workspace")
		}
	}
	return changedFiles(sections), nil
}

// A workspace cannot hold a symlink; its captured target text is a regular
// file there.
func sameMode(tree, workspace string) bool {
	return tree == workspace || (tree == "120000" && workspace == "100644")
}

func changedFiles(sections []patchSection) []ChangedFile {
	out := make([]ChangedFile, 0, len(sections))
	for _, s := range sections {
		out = append(out, s.ChangedFile)
	}
	return out
}
