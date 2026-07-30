package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
)

// LocalTranscript is a protected sidecar-only sink for native detail. It is
// deliberately separate from StreamResult so callers cannot persist provider
// payloads through the authority event DTO.
type LocalTranscript interface {
	AppendNative([]byte) error
}

type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	Duration     *time.Duration
}

type StreamResult struct {
	Output []byte
	Events []broker.Event
	Usage  Usage
}

// DecodeStream converts the evolving native JSONL envelopes into the small set
// of facts the broker owns. Unknown event fields and event types remain in the
// protected raw event stream and do not change terminal semantics.
func DecodeStream(
	name broker.AdapterName,
	input io.Reader,
	maxBytes int,
	transcripts ...LocalTranscript,
) (StreamResult, error) {
	if maxBytes <= 0 {
		return StreamResult{}, fmt.Errorf("broker adapter: stream byte limit must be positive")
	}
	stream, err := io.ReadAll(io.LimitReader(input, int64(maxBytes)+1))
	if err != nil {
		return StreamResult{}, fmt.Errorf("broker adapter: read native stream: %w", err)
	}
	if len(stream) > maxBytes {
		return StreamResult{}, fmt.Errorf("broker adapter: native stream exceeds byte limit %d", maxBytes)
	}
	scanner := bufio.NewScanner(bytes.NewReader(stream))
	scanner.Buffer(make([]byte, 4096), maxBytes)
	var result StreamResult
	var candidate []byte
	terminal := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope nativeEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return result, fmt.Errorf("broker adapter: invalid native JSON event: %w", err)
		}
		if strings.TrimSpace(envelope.Type) == "" {
			return result, fmt.Errorf("broker adapter: native JSON event type is required")
		}
		for _, transcript := range transcripts {
			if transcript != nil {
				if err := transcript.AppendNative(append([]byte(nil), line...)); err != nil {
					return result, fmt.Errorf("broker adapter: append protected transcript: %w", err)
				}
			}
		}
		result.Events = appendNormalizedEvent(result.Events, envelope)
		switch name {
		case broker.AdapterCodex:
			if envelope.Type == "item.completed" && envelope.Item.Type == "agent_message" {
				candidate = []byte(envelope.Item.Text)
			}
			if envelope.Type == "turn.completed" {
				if len(candidate) == 0 {
					return result, fmt.Errorf("broker adapter: terminal Codex turn has no agent message")
				}
				terminal = true
				setTokenUsage(&result.Usage, envelope.Usage)
			}
			if envelope.Type == "error" {
				return result, fmt.Errorf("broker adapter: native execution failed")
			}
		case broker.AdapterClaude:
			if envelope.Type == "result" {
				if terminal {
					return result, fmt.Errorf("broker adapter: conflicting terminal native results")
				}
				if envelope.Subtype != "success" {
					return result, fmt.Errorf("broker adapter: native execution failed")
				}
				structured := bytes.TrimSpace(envelope.StructuredOutput)
				if len(structured) == 0 || bytes.Equal(structured, []byte("null")) {
					return result, fmt.Errorf("broker adapter: terminal Claude result has no structured output")
				}
				candidate = append([]byte(nil), structured...)
				terminal = true
				setTokenUsage(&result.Usage, envelope.Usage)
				if envelope.TotalCostUSD != nil {
					value := *envelope.TotalCostUSD
					result.Usage.CostUSD = &value
				}
				if envelope.DurationMS != nil {
					value := time.Duration(*envelope.DurationMS) * time.Millisecond
					result.Usage.Duration = &value
				}
			}
		case broker.AdapterCursor:
			if envelope.Type == "result" {
				if terminal {
					return result, fmt.Errorf("broker adapter: conflicting terminal native results")
				}
				if envelope.Subtype != "success" {
					return result, fmt.Errorf("broker adapter: native execution failed")
				}
				candidate = []byte(envelope.Result)
				terminal = true
				setTokenUsage(&result.Usage, envelope.Usage)
				if envelope.TotalCostUSD != nil {
					value := *envelope.TotalCostUSD
					result.Usage.CostUSD = &value
				}
				if envelope.DurationMS != nil {
					value := time.Duration(*envelope.DurationMS) * time.Millisecond
					result.Usage.Duration = &value
				}
			}
		default:
			return result, fmt.Errorf("broker adapter: unsupported adapter %q", name)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("broker adapter: native stream limit or read failure: %w", err)
	}
	if !terminal {
		return result, fmt.Errorf("broker adapter: native stream ended without a terminal result")
	}
	if len(candidate) == 0 {
		return result, fmt.Errorf("broker adapter: terminal result output is empty")
	}
	result.Output = append([]byte(nil), candidate...)
	return result, nil
}

type nativeEnvelope struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	ThreadID         string          `json:"thread_id"`
	SessionID        string          `json:"session_id"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	DurationMS       *int64          `json:"duration_ms"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	Usage            nativeUsage     `json:"usage"`
	Item             nativeItem      `json:"item"`
	Message          nativeMessage   `json:"message"`
}

type nativeUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

type nativeItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type nativeMessage struct {
	ID string `json:"id"`
}

func appendNormalizedEvent(events []broker.Event, envelope nativeEnvelope) []broker.Event {
	if len(events) == 128 {
		return events
	}
	kind := broker.EventProgress
	if envelope.Type == "error" || (envelope.Type == "result" && envelope.Subtype != "success") {
		kind = broker.EventFailed
	} else if envelope.Type == "turn.completed" || (envelope.Type == "result" && envelope.Subtype == "success") {
		kind = broker.EventCompleted
	}
	return append(events, broker.Event{Kind: kind})
}

func setTokenUsage(target *Usage, source nativeUsage) {
	if source.InputTokens != nil {
		value := *source.InputTokens
		target.InputTokens = &value
	}
	if source.OutputTokens != nil {
		value := *source.OutputTokens
		target.OutputTokens = &value
	}
}
