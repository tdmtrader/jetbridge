package git_test

import (
	"context"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/core"
)

// oldRows is the old queue's reader of a land commit's rows block, copied as
// a fixture: the head line, then "RID SHA" lines up to the first blank line.
func oldRows(msg string) [][2]string {
	rid, sha := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`), regexp.MustCompile(`^[0-9a-f]{40}$`)
	var rows [][2]string
	in := false
	for _, ln := range strings.Split(msg, "\n") {
		if ln == "Rows (row id, original sha):" {
			in = true
			continue
		}
		if in && ln == "" {
			break
		}
		if f := strings.Fields(ln); in && len(f) == 2 && rid.MatchString(f[0]) && sha.MatchString(f[1]) {
			rows = append(rows, [2]string{f[0], f[1]})
		}
	}
	return rows
}

var _ = Describe("Land commit messages", func() {
	It("Each land commit carries the rows block the old queue wrote, and its original line", func() {
		r := newScriptRepo()
		a, b := r.commit("a", "main", "a.txt", "a\n"), r.commit("b", "main", "b.txt", "b\n")
		sha, err := git.NewComposer(r.config("")).Compose(context.Background(), "main", []core.Entry{a, b})
		Expect(err).NotTo(HaveOccurred())
		for i, e := range []core.Entry{a, b} {
			msg := composeRun(r.remote, "log", "-1", "--format=%B", sha+"~"+[]string{"1", "0"}[i])
			Expect(msg).To(Equal("land(" + e.ID + "): change " + e.ID + "\n\noriginal: " + e.Commit + "\n\nRows (row id, original sha):\n" + e.ID + " " + e.Commit))
			Expect(oldRows(msg)).To(Equal([][2]string{{e.ID, e.Commit}}))
		}
		fixture := "land(JBQ-g7-0a1b2c3d4e): 1 row(s) composed as ONE commit on 0123abcd (jb-land-compose)\n\nRows (row id, original sha):\nR12 " + a.Commit + "\n\nBase: " + a.Commit + "\n"
		Expect(oldRows(fixture)).To(Equal([][2]string{{"R12", a.Commit}}))
	})
})
