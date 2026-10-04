package git_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/adapters/git"
	"github.com/concourse/concourse/queue/core"
)

// unionHook owns nothing and names file-length.toml as a keyed-line list.
const unionHook = "#!/bin/sh\ncase \"$1\" in\nunion) echo file-length.toml ;;\nesac\n"

const lengths = "# longest file, by path\n[ceilings]\n\"a.go\" = 100\n\"b.go\" = 200\n\"c.go\" = 300\n\"d.go\" = 400\n"

var _ = Describe("Composer merging keyed-line lists the hook script names", func() {
	var r scriptRepo
	ctx := context.Background()
	BeforeEach(func() { r = newScriptRepo() })
	// compose composes changes a and b, writing ca and cb to file.
	compose := func(hook bool, file, ca, cb string) (string, error) {
		if hook {
			r.commit("main", "main", "ci/hook.sh", unionHook)
		}
		r.commit("main", "main", file, lengths)
		a, b := r.commit("a", "main", file, ca), r.commit("b", "main", file, cb)
		sha, err := git.NewComposer(r.config("")).Compose(ctx, "main", []core.Entry{a, b})
		if err != nil {
			return "", err
		}
		return composeRun(r.remote, "show", sha+":"+file), nil
	}
	e, f := lengths+"\"e.go\" = 500\n", lengths+"\"f.go\" = 600\n"

	It("Two rows in one batch that both touch file-length.toml both land", func() {
		Expect(compose(true, "file-length.toml", e, f)).To(Equal(e + "\"f.go\" = 600"))
	})

	It("A key both rows change to different values is still a conflict", func() {
		c301, c302 := strings.Replace(lengths, "300", "301", 1), strings.Replace(lengths, "300", "302", 1)
		Expect(compose(true, "file-length.toml", c301, c302)).Error().To(MatchError(core.ConflictError{EntryID: "b"}))
	})

	It("One key added with two values at separate places is a conflict, not two lines", func() {
		top := strings.Replace(lengths, "100\n", "100\n\"shared.go\" = 100\n", 1)
		Expect(compose(true, "file-length.toml", top, lengths+"\"shared.go\" = 200\n")).Error().To(MatchError(core.ConflictError{EntryID: "b"}))
	})

	It("A conflict in a file the hook does not list is still a conflict", func() {
		Expect(compose(true, "other.toml", e, f)).Error().To(MatchError(core.ConflictError{EntryID: "b"}))
	})

	It("With no hook script on main two rows adding to the list conflict as before", func() {
		Expect(compose(false, "file-length.toml", e, f)).Error().To(MatchError(core.ConflictError{EntryID: "b"}))
	})
})
