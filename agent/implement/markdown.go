package implement

import (
	"fmt"
	"html"
	"strings"
)

// Markdown renders a verified change for a person: what the agent says it
// did, what it disclosed, which files the patch touches, and the patch itself
// in a fence no line of the patch can close.
func (s *Summary) Markdown(patch []byte) string { return s.markdown(patch, nil) }

// ValidatedMarkdown renders the change with the validation the template ran
// against it, placed before the patch. The caller has already bound the
// validation to this change (Validation.Attests).
func (s *Summary) ValidatedMarkdown(patch []byte, v *Validation) string { return s.markdown(patch, v) }

func (s *Summary) markdown(patch []byte, v *Validation) string {
	var b strings.Builder
	state := "complete"
	if !s.Complete {
		state = "incomplete"
	}
	fmt.Fprintf(&b, "# Implementation: %s\n\n%s\n\n", state, markdownText(s.Summary))
	if s.RunID != nil {
		fmt.Fprintf(&b, "Run: %d\n\n", *s.RunID)
	}
	fmt.Fprintf(&b, "Base: %s\n\nInput: %s\n\nPatch: %s\n\n", s.Provenance.BaseCommit, s.Provenance.InputDigest, s.PatchDigest)
	fmt.Fprintf(&b, "Author: %s %s, model %s, %s\n\n", markdownText(s.Provenance.Provider), markdownText(s.Provenance.CodexVersion), markdownText(s.Provenance.ModelRequested), s.Provenance.ExecutionPolicy)
	b.WriteString("## Limitations\n\n")
	for _, l := range s.Limitations {
		fmt.Fprintf(&b, "- %s\n", markdownText(l))
	}
	b.WriteString("\n## Changed files\n\n")
	for _, f := range s.ChangedFiles {
		fmt.Fprintf(&b, "- %s %s\n", f.Status, markdownText(f.Path))
	}
	if v != nil {
		b.WriteString("\n")
		v.markdown(&b, "##")
	}
	fence := "```"
	for strings.Contains(string(patch), fence) {
		fence += "`"
	}
	fmt.Fprintf(&b, "\n## Patch\n\n%sdiff\n%s", fence, patch)
	if len(patch) > 0 && patch[len(patch)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n")
	return b.String()
}

func markdownText(s string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "#", "\\#").Replace(html.EscapeString(s))
}
