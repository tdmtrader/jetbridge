package mergepolicy

// JudgeVerdict is the judge's input to the ladder. The judge may only
// ESCALATE — there is deliberately no field by which it can authorize a
// merge that the fence rejected.
type JudgeVerdict struct {
	Escalate bool
	Reason   string
	Err      error // any judge fault; non-nil always escalates
}

// Decision is the ladder's answer.
type Decision struct {
	Merge    bool
	MergedBy string // "auto" | "judge"; empty when escalating
	Escalate bool
	Reason   string
}

func escalate(reason string) Decision {
	return Decision{Escalate: true, Reason: reason}
}

// Decide reports whether the platform may merge without a human click.
//
// Fail-safe direction: every uncertain path returns escalate. The judge tier
// is strictly MORE conservative than auto — it is the fence plus a mandatory
// judge non-veto.
func Decide(p Policy, changes []Change, judge *JudgeVerdict) Decision {
	switch p.Tier {
	case TierAuto:
		if r := EvaluateFence(p, changes); !r.Passed {
			return escalate(r.Reason)
		}
		return Decision{Merge: true, MergedBy: "auto"}

	case TierJudge:
		// Fence first: the judge never gets to authorize past it.
		if r := EvaluateFence(p, changes); !r.Passed {
			return escalate(r.Reason)
		}
		if judge == nil {
			return escalate("judge tier requires a judge verdict")
		}
		if judge.Err != nil {
			return escalate("judge fault: " + judge.Err.Error())
		}
		if judge.Escalate {
			return escalate(judge.Reason)
		}
		return Decision{Merge: true, MergedBy: "judge"}

	default:
		// TierManual and any unknown/corrupt tier value.
		return escalate("manual review required")
	}
}
