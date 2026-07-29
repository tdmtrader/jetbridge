package checkpoint

// RecoveryDecision is intentionally just a durable decision record. Provider
// resume and workspace restoration are separate orchestration work; this
// package only makes the unsafe condition impossible to ignore.
type RecoveryDecision struct {
	Mode               FallbackMode
	ManualReviewReason string
}

// DecideRecovery fails closed for an effect that was begun without a durable
// completion record. Replaying that effect could duplicate a remote write,
// and claiming it completed could skip a required write.
func DecideRecovery(effects []Effect) RecoveryDecision {
	for _, effect := range effects {
		if effect.State == EffectBegun {
			return RecoveryDecision{
				Mode:               FallbackManualReview,
				ManualReviewReason: "external effect began but is not known complete",
			}
		}
	}
	return RecoveryDecision{Mode: FallbackWorkspaceOnly}
}
