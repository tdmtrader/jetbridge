package integration_test

import (
	"context"
	"encoding/json"

	"github.com/concourse/ci-agent/llm"
)

// fakeLLMClient returns canned responses for each call in order.
type fakeLLMClient struct {
	responses []json.RawMessage
	callIdx   int
	prompts   []string
}

func (f *fakeLLMClient) Call(_ context.Context, prompt string, _ llm.CallOpts) (llm.CallResult, error) {
	f.prompts = append(f.prompts, prompt)
	if f.callIdx < len(f.responses) {
		resp := f.responses[f.callIdx]
		f.callIdx++
		return llm.CallResult{Result: resp}, nil
	}
	return llm.CallResult{Result: json.RawMessage(`{}`)}, nil
}
