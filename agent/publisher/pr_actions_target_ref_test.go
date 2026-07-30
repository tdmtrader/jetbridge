package publisher_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestStatusAndResponsePublicationRequireTargetBranchAuthority(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		request := validStatusPublicationRequest()
		request.TargetRef = ""
		if err := request.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
			t.Fatalf("missing target ref error = %v, want invalid request", err)
		}
	})

	t.Run("response", func(t *testing.T) {
		request := validResponsePublicationRequest()
		request.TargetRef = ""
		if err := request.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
			t.Fatalf("missing target ref error = %v, want invalid request", err)
		}
	})
}

func TestStatusAndResponseTargetBranchParticipatesInOperationIdentityAndClone(t *testing.T) {
	status := validStatusPublicationRequest()
	status.TargetRef = "refs/heads/main"
	statusKey, err := status.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	otherStatus := status
	otherStatus.TargetRef = "refs/heads/release"
	otherStatusKey, err := otherStatus.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if otherStatusKey == statusKey {
		t.Fatalf("status targets shared operation key %q", statusKey)
	}
	statusAction := publisher.PRAction{
		Kind:   publisher.OperationPublishPRStatus,
		Status: &status,
	}
	clonedStatus := statusAction.Clone()
	if clonedStatus.Status == statusAction.Status ||
		clonedStatus.Status.TargetRef != status.TargetRef {
		t.Fatalf("cloned status action = %+v", clonedStatus)
	}

	response := validResponsePublicationRequest()
	response.TargetRef = "refs/heads/main"
	responseKey, err := response.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	otherResponse := response
	otherResponse.TargetRef = "refs/heads/release"
	otherResponseKey, err := otherResponse.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if otherResponseKey == responseKey {
		t.Fatalf("response targets shared operation key %q", responseKey)
	}
	responseAction := publisher.PRAction{
		Kind:     publisher.OperationRespondToReview,
		Response: &response,
	}
	clonedResponse := responseAction.Clone()
	if clonedResponse.Response == responseAction.Response ||
		clonedResponse.Response.TargetRef != response.TargetRef {
		t.Fatalf("cloned response action = %+v", clonedResponse)
	}
}

func TestStatusAndResponseRejectNonHeadTargetRefs(t *testing.T) {
	for _, targetRef := range []string{
		"main",
		"refs/tags/main",
		"refs/heads/../main",
		"refs/heads/main.lock",
	} {
		t.Run(targetRef, func(t *testing.T) {
			status := validStatusPublicationRequest()
			status.TargetRef = targetRef
			if err := status.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
				t.Fatalf("status target ref error = %v, want invalid request", err)
			}

			response := validResponsePublicationRequest()
			response.TargetRef = targetRef
			if err := response.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
				t.Fatalf("response target ref error = %v, want invalid request", err)
			}
		})
	}
}

func TestResponsePublicationRejectsSemanticNoResponse(t *testing.T) {
	request := validResponsePublicationRequest()
	request.Response = contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseNoResponse,
	}
	if err := request.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("no-response publication error = %v, want invalid request", err)
	}
}
