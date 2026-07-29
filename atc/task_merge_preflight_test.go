package atc

import "testing"

func TestMergePreflightStaticArgsUseRebase(t *testing.T) {
	args := MergePreflightStaticArgs(MergePreflightAuthority{
		CandidateInput: "candidate", BaseInput: "base", TargetInput: "target",
	})
	for _, arg := range args {
		if arg == "--method=merge" {
			t.Fatal("merge preflight still requested a merge commit")
		}
		if arg == "--method=rebase" {
			return
		}
	}
	t.Fatal("merge preflight did not request rebase")
}
