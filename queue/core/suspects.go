package core

// Suspects ranks the entries of a red batch, likeliest cause first, from the
// failed test names and the files each entry changed (keyed by entry ID). A
// ranking is only a hint: the core proves the top suspect alone before it
// ejects it. An adapter that cannot rank returns nil.
type Suspects interface {
	Rank(failed []string, batch []Entry, changed map[string][]string) []string
}

// NoSuspects ranks nothing, so a bisect with it is a plain bisect.
type NoSuspects struct{}

func (NoSuspects) Rank([]string, []Entry, map[string][]string) []string { return nil }
