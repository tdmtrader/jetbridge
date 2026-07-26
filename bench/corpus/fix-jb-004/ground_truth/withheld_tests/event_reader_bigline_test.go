// Withheld grading test for bench case fix-jb-004. NEVER expose this file to
// the agent under test — it names the mechanism in its own spec text.
//
// Drop into agent/schema/ at grading time (it is additive: a separate Describe
// block in its own file, so it does not clobber the agent's own edits to
// event_io_test.go). Historical form of this assertion lives inside
// agent/schema/event_io_test.go at 6e113b067b10bc6b426108fffac8297dd75e6151.
package schema_test

import (
	"io"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/schema"
)

var _ = Describe("EventReader oversized lines (bench fix-jb-004)", func() {
	It("reads a line larger than the default 64KiB scanner limit", func() {
		// A single NDJSON event whose payload exceeds bufio.Scanner's default
		// 64KiB token limit must not abort the stream. Agent-step ingestion
		// breaks its read loop on any reader error, so an oversized line
		// mid-stream would otherwise discard every later cost.record and
		// step.end event — leaving the step status=error even when a valid
		// step.end followed (review finding, 2026-07-12).
		big := strings.Repeat("x", 200*1024) // 200 KiB, well past the 64 KiB default
		input := `{"ts":"2026-02-09T21:30:00Z","event":"tool.call","data":{"tool":"grep","blob":"` + big + `"}}` + "\n" +
			`{"ts":"2026-02-09T21:30:01Z","event":"agent.end","data":{"status":"pass"}}` + "\n"

		r := schema.NewEventReader(strings.NewReader(input))

		events := []schema.Event{}
		for {
			event, err := r.Read()
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			events = append(events, *event)
		}

		Expect(events).To(HaveLen(2))
		Expect(events[0].Type).To(Equal(schema.EventToolCall))
		Expect(events[1].Type).To(Equal(schema.EventAgentEnd))
	})
})
