package interaction

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// A piped answer (`echo some-team | fly destroy-team -n some-team`) ends in a
// newline, which bubbletea reads as ctrl+j. Each prompt must take it as the
// end of the answer, as it takes enter at a terminal; before, it was typed
// into nothing and the prompt waited for ever.
func focused(value string) textinput.Model {
	i := textinput.New()
	i.Focus()
	i.SetValue(value)
	return i
}

var newline = tea.KeyMsg{Type: tea.KeyCtrlJ}

func TestInputEndsAtANewline(t *testing.T) {
	m, cmd := InputModel{input: focused("some-team")}.Update(newline)
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatalf("the input prompt did not end at a newline")
	}
	if got := m.(InputModel).Output(); got != "some-team" {
		t.Fatalf("answer = %q, want %q", got, "some-team")
	}
}

func TestConfirmEndsAtANewline(t *testing.T) {
	m, cmd := confirmModel{input: focused("y")}.Update(newline)
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatalf("the confirmation did not end at a newline")
	}
	if !m.(confirmModel).confirmation {
		t.Fatalf("a piped \"y\" was not taken as yes")
	}
}

func TestTokenEndsAtANewline(t *testing.T) {
	m, cmd := TokenModel{input: focused("Bearer some-token")}.Update(newline)
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatalf("the token prompt did not end at a newline")
	}
	if got := m.(TokenModel).Token(); got != "Bearer some-token" {
		t.Fatalf("token = %q, want %q", got, "Bearer some-token")
	}
}
