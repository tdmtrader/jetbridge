package output

// Req 36's margin, which was enforced nowhere on the read path.
//
// "Work starts only with the operation's timeout plus two minutes remaining" is
// carried by exactly one number -- RequiredRemainingSeconds -- and the database
// applies it as `expires_at < now() + required_remaining`. Every production
// caller passed ZERO, so the check degenerated to "not yet expired": a read
// admitted with thirty seconds left would start and be cut off mid-transfer,
// which is what the margin exists to prevent. The repository looked correct
// because the spec that appeared to pin it supplied the margin itself.

import (
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

func TestAReadProfileCarriesTheTimeoutItsMarginIsMadeOf(t *testing.T) {
	client := &LeaseControlClient{}

	profile, err := NewLeaseReadProfile(client, "a-grant", 10*time.Minute)
	if err != nil {
		t.Fatalf("building the profile: %v", err)
	}
	if want := 10*time.Minute + LeaseStartMargin; profile.requiredRemaining() != want {
		t.Errorf("the profile asks for %s of remaining term, expected %s (the operation's "+
			"timeout plus the start margin)", profile.requiredRemaining(), want)
	}

	// A profile with no timeout cannot be built at all. One that could would be
	// one that asks the control plane whether the lease has expired.
	if _, err := NewLeaseReadProfile(client, "a-grant", 0); err == nil {
		t.Error("a managed read profile was built with no materialization timeout; its Admit " +
			"would ask for zero remaining term, which is not Req 36's rule")
	}
}

// And the control plane's own end: a validate or a renew naming zero is refused
// before it is ever asked, so a daemon cannot talk the database into applying
// the weaker rule.
func TestAValidateOrRenewMayNotNameZeroWork(t *testing.T) {
	question := func(operation LeaseOperation, work time.Duration) LeaseQuestion {
		return LeaseQuestion{
			ProtocolVersion:          ProtocolVersion,
			Operation:                operation,
			KeyID:                    "control-key-1",
			NodeUID:                  executioncontrol.NodeUID("node-1"),
			Grant:                    "a-grant",
			RequiredRemainingSeconds: int64(work.Seconds()),
			IssuedAt:                 NewTimestamp(time.Now().UTC()),
			Signature:                "signed",
		}
	}

	for _, operation := range []LeaseOperation{LeaseValidate, LeaseRenew} {
		if err := question(operation, 0).Validate(); err == nil {
			t.Errorf("a %s naming zero work was accepted. The database's check is "+
				"`expires_at < now() + required_remaining`, so zero asks only whether the lease "+
				"has expired -- and a read admitted with seconds left starts and is cut off "+
				"mid-transfer.", operation)
		} else if !errors.Is(err, ErrIncomplete) {
			t.Errorf("a %s naming zero work was refused as %v", operation, err)
		}
		// A non-zero term is the CALLER's to compute -- only the caller knows
		// what it is about to start -- so this validation refuses the one value
		// that can never be right rather than second-guessing the rest.
		if err := question(operation, time.Minute).Validate(); err != nil {
			t.Errorf("a %s naming a minute of work was refused: %v", operation, err)
		}
		if err := question(operation, time.Hour).Validate(); err != nil {
			t.Errorf("a %s naming an hour of work was refused: %v", operation, err)
		}
	}

	// A release starts no work and still names none.
	if err := question(LeaseRelease, 0).Validate(); err != nil {
		t.Errorf("a release naming zero work was refused: %v", err)
	}
	if err := question(LeaseRelease, time.Hour).Validate(); err == nil {
		t.Error("a release naming an hour of work was accepted")
	}
}
