package publisher

func cloneApproval(approval *ApprovalEvidence) *ApprovalEvidence {
	if approval == nil {
		return nil
	}
	copy := *approval
	return &copy
}
