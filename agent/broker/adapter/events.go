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

type Event struct {
	Type      string
	SessionID string
	Raw       json.RawMessage
}

type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	Duration     *time.Duration
}

type StreamResult struct {
	Output []byte
	Events []Event
	Usage  Usage
}

// DecodeStream converts the evolving native JSONL envelopes into the small set
// of facts the broker owns. Unknown event fields and event types remain in the
// protected raw event stream and do not change terminal semantics.
func DecodeStream(name broker.AdapterName, input io.Reader, maxBytes int) (StreamResult, error) {
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
			return StreamResult{}, fmt.Errorf("broker adapter: invalid native JSON event: %w", err)
		}
		if strings.TrimSpace(envelope.Type) == "" {
			return StreamResult{}, fmt.Errorf("broker adapter: native JSON event type is required")
		}
		result.Events = append(result.Events, Event{
			Type: envelope.Type, SessionID: sessionID(envelope),
			Raw: append(json.RawMessage(nil), line...),
		})
		switch name {
		case broker.AdapterCodex:
			if envelope.Type == "item.completed" && envelope.Item.Type == "agent_message" {
				candidate = []byte(envelope.Item.Text)
			}
			if envelope.Type == "turn.completed" {
				if len(candidate) == 0 {
					return StreamResult{}, fmt.Errorf("broker adapter: terminal Codex turn has no agent message")
				}
				terminal = true
				setTokenUsage(&result.Usage, envelope.Usage)
			}
			if envelope.Type == "error" {
				return StreamResult{}, fmt.Errorf("broker adapter: native execution failed")
			}
		case broker.AdapterClaude, broker.AdapterCursor:
			if envelope.Type == "result" {
				if terminal {
					return StreamResult{}, fmt.Errorf("broker adapter: conflicting terminal native results")
				}
				if envelope.Subtype != "success" {
					return StreamResult{}, fmt.Errorf("broker adapter: native execution failed")
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
			return StreamResult{}, fmt.Errorf("broker adapter: unsupported adapter %q", name)
		}
	}
	if err := scanner.Err(); err != nil {
		return StreamResult{}, fmt.Errorf("broker adapter: native stream limit or read failure: %w", err)
	}
	if !terminal {
		return StreamResult{}, fmt.Errorf("broker adapter: native stream ended without a terminal result")
	}
	if len(candidate) == 0 {
		return StreamResult{}, fmt.Errorf("broker adapter: terminal result output is empty")
	}
	result.Output = append([]byte(nil), candidate...)
	return result, nil
}

type nativeEnvelope struct {
	Type         string        `json:"type"`
	Subtype      string        `json:"subtype"`
	ThreadID     string        `json:"thread_id"`
	SessionID    string        `json:"session_id"`
	Result       string        `json:"result"`
	DurationMS   *int64        `json:"duration_ms"`
	TotalCostUSD *float64      `json:"total_cost_usd"`
	Usage        nativeUsage   `json:"usage"`
	Item         nativeItem    `json:"item"`
	Message      nativeMessage `json:"message"`
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

func sessionID(envelope nativeEnvelope) string {
	if envelope.SessionID != "" {
		return envelope.SessionID
	}
	return envelope.ThreadID
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
