package interaction

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// errUnanswerable is returned by a prompt when stdin is a character device
// that is not a terminal, such as /dev/null: nothing can ever answer it. On
// Linux bubbletea cannot even start there (epoll refuses to register
// /dev/null, "error creating cancelreader"), and on macOS it reads EOF and
// waits forever. A terminal, a pipe and a file are left to bubbletea.
func errUnanswerable(prompt string) error {
	info, err := os.Stdin.Stat()
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeCharDevice == 0 || term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return fmt.Errorf("cannot prompt %q: stdin is not a terminal; run fly from a terminal or pass --non-interactive where the command offers it", strings.TrimSpace(prompt))
}
