package eos_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tdmtrader/lightingdesign/internal/eos"
	"github.com/tdmtrader/lightingdesign/internal/osc"
)

// truncatedActiveChan is the exact /eos/out/active/chan selection string captured
// from the console on a busy look — 100 characters, cut mid-token, with the tail
// of the final entry replaced by an ellipsis fragment ("10..").
const truncatedActiveChan = "2,7-8,10-11,14-15,17-18,21-22,24-25,28-29,31-32,35-36,38-39,42-43,45-46,56-57,59-60,76-78,80-85,10.."

// startSelectionResponder answers the client's console traffic with a scripted
// /eos/out/active/chan reply: `selection` for the [Select Active] press, and a
// plausible level echo for every single-channel select ("Chan N#"), so no
// per-channel read times out and `partial` can only come from the selection
// string itself.
func startSelectionResponder(t *testing.T, mt *mockTransport, selection string) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case m := <-mt.sentCh:
				switch {
				case m.Address == "/eos/key/select active" && len(m.Args) > 0:
					if v, _ := m.Args[0].(float32); v == 1.0 {
						mt.inject(&osc.Message{Address: "/eos/out/active/chan", Args: []any{selection}})
					}
				case m.Address == "/eos/newcmd" && len(m.Args) > 0:
					s, _ := m.Args[0].(string)
					if !strings.HasPrefix(s, "Chan ") || !strings.HasSuffix(s, "#") {
						continue
					}
					n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(s, "Chan "), "#"))
					if err != nil || n < 1 {
						continue
					}
					mt.inject(&osc.Message{
						Address: "/eos/out/active/chan",
						Args:    []any{fmt.Sprintf("%d [50] Generic Dimmer @ 50", n)},
					})
				}
			}
		}
	}()
}

func connectedFakeClient(t *testing.T) (*eos.Client, *mockTransport) {
	t.Helper()
	mt := newMockTransport()
	c := eos.New(mt, testTarget())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, mt
}

// A busy look is exactly the look an operator most wants to inspect, and it is
// the one the console cannot echo in full. read_stage must still report it: the
// complete channels ahead of the cut are salvaged and the result is flagged
// partial, rather than the whole read failing.
func TestActiveChannelsSalvagesTruncatedSelection(t *testing.T) {
	c, mt := connectedFakeClient(t)
	startSelectionResponder(t, mt, truncatedActiveChan)

	states, partial, err := c.ActiveChannels(context.Background(), nil)
	if err != nil {
		t.Fatalf("a console-truncated selection must not fail the whole read, got error: %v", err)
	}
	if !partial {
		t.Fatal("expected partial=true so read_stage can tell the operator the list may be incomplete")
	}
	if len(states) != 38 {
		t.Fatalf("got %d channels, want the 38 complete channels ahead of the cut: %+v", len(states), states)
	}
	for _, s := range states {
		if s.Channel < 2 || s.Channel > 85 {
			t.Fatalf("channel %d is outside the salvageable range 2..85 (phantom from the fragment?): %+v", s.Channel, states)
		}
	}
	if states[0].Channel != 2 || states[len(states)-1].Channel != 85 {
		t.Fatalf("got range %d..%d, want 2..85", states[0].Channel, states[len(states)-1].Channel)
	}
}

// Guard against an over-broad fix: tolerating the trailing fragment must not turn
// into tolerating garbage anywhere. A token the console could never emit, sitting
// in the middle of an otherwise complete selection, is a real protocol error and
// must still surface as one.
func TestActiveChannelsMidSelectionGarbageStillErrors(t *testing.T) {
	c, mt := connectedFakeClient(t)
	startSelectionResponder(t, mt, "1 bogus 3 [0]")

	if _, _, err := c.ActiveChannels(context.Background(), nil); err == nil {
		t.Fatal("expected an error for an unparseable token in the middle of the selection")
	}
}
