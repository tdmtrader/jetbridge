package executioncontrol

import "github.com/concourse/concourse/hangar"

// ControlWarrant is the bound fields of a control warrant: one operation on
// one exact execution under one fence, for one of the two control purposes.
//
// The purpose is the route's. A base route admits hangar.PurposeControlBase
// and a capture route hangar.PurposeControlCapture; a warrant valid for base
// execution control cannot hold, seal or publish, however valid it is, and a
// capture warrant cannot stop or observe. The nonce and the window are the
// signer's, and the node refuses a replayed nonce inside its window.
func ControlWarrant(purpose hangar.Purpose, operation string, id Identity) hangar.Warrant {
	return hangar.Warrant{
		Purpose:     purpose,
		Operation:   operation,
		ExecutionID: string(id.ExecutionID),
		Fence:       uint64(id.Fence),
	}
}
