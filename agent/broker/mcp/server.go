// Package mcp exposes the broker's two synchronous agent-facing tools.
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/atc/api/mcpserver"
)

type Service interface {
	RequestReview(context.Context, broker.ReviewRequest) (broker.Result, error)
	ConsultAgent(context.Context, broker.ConsultRequest) (broker.Result, error)
}

func NewServer(service Service, catalog *broker.Catalog) (*mcpserver.Server, error) {
	if service == nil || catalog == nil {
		return nil, fmt.Errorf("broker MCP: service and catalog are required")
	}
	visible := catalog.Visible()
	if len(visible) == 0 {
		return nil, fmt.Errorf("broker MCP: visible catalog is empty")
	}
	catalogJSON, err := json.Marshal(visible)
	if err != nil {
		return nil, err
	}
	tiers, efforts := visibleEnums(visible)
	server := mcpserver.NewServer()
	server.AddTool(
		string(broker.ToolRequestReview),
		"Statically review an exact dirty Git workspace. Tests cannot be run. Supported node profiles: "+string(catalogJSON),
		requestReviewSchema(tiers, efforts),
		func(ctx context.Context, raw json.RawMessage, progress func(string)) (any, error) {
			var input reviewInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if err := broker.ValidateAttachments(broker.ToolRequestReview, input.Attachments); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			idempotencyKey, err := newCallID()
			if err != nil {
				return nil, err
			}
			progress("resolving review profile")
			return service.RequestReview(ctx, broker.ReviewRequest{
				IdempotencyKey: idempotencyKey,
				Selector:       broker.Selector{Tier: input.Tier, Effort: input.Effort},
				Instructions:   input.Instructions,
				Attachments:    append([]string(nil), input.Attachments...),
			})
		},
	)
	server.AddTool(
		string(broker.ToolConsultAgent),
		"Ask a fresh-context agent for a structured consultation. Supported node profiles: "+string(catalogJSON),
		consultSchema(tiers, efforts),
		func(ctx context.Context, raw json.RawMessage, progress func(string)) (any, error) {
			var input consultInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if err := broker.ValidateAttachments(broker.ToolConsultAgent, input.Attachments); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			idempotencyKey, err := newCallID()
			if err != nil {
				return nil, err
			}
			progress("resolving consultation profile")
			return service.ConsultAgent(ctx, broker.ConsultRequest{
				IdempotencyKey: idempotencyKey,
				Selector:       broker.Selector{Tier: input.Tier, Effort: input.Effort},
				Question:       input.Question, Context: input.Context,
				Attachments: append([]string(nil), input.Attachments...),
			})
		},
	)
	return server, nil
}

type reviewInput struct {
	Tier         broker.Tier   `json:"tier"`
	Effort       broker.Effort `json:"effort"`
	Instructions string        `json:"instructions,omitempty"`
	Attachments  []string      `json:"attachments"`
}

type consultInput struct {
	Tier        broker.Tier   `json:"tier"`
	Effort      broker.Effort `json:"effort"`
	Question    string        `json:"question"`
	Context     string        `json:"context,omitempty"`
	Attachments []string      `json:"attachments"`
}

func requestReviewSchema(tiers, efforts []string) json.RawMessage {
	return mcpserver.MustJSON(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"tier", "effort", "attachments"},
		"properties": map[string]any{
			"tier":         map[string]any{"type": "string", "enum": tiers},
			"effort":       map[string]any{"type": "string", "enum": efforts},
			"instructions": map[string]any{"type": "string"},
			"attachments": map[string]any{
				"type": "array", "minItems": 1, "uniqueItems": true,
				"contains": map[string]any{"const": "workspace"},
				"items":    map[string]any{"type": "string", "enum": []string{"workspace", "validation"}},
			},
		},
	})
}

func consultSchema(tiers, efforts []string) json.RawMessage {
	return mcpserver.MustJSON(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"tier", "effort", "question", "attachments"},
		"properties": map[string]any{
			"tier":     map[string]any{"type": "string", "enum": tiers},
			"effort":   map[string]any{"type": "string", "enum": efforts},
			"question": map[string]any{"type": "string", "minLength": 1},
			"context":  map[string]any{"type": "string"},
			"attachments": map[string]any{
				"type": "array", "minItems": 1, "uniqueItems": true,
				"items": map[string]any{"type": "string", "enum": []string{"design", "api-contract"}},
			},
		},
	})
}

func visibleEnums(visible []broker.VisibleProfile) ([]string, []string) {
	tierSet := make(map[string]struct{})
	effortSet := make(map[string]struct{})
	for _, profile := range visible {
		tierSet[string(profile.Tier)] = struct{}{}
		effortSet[string(profile.Effort)] = struct{}{}
	}
	tiers := make([]string, 0, len(tierSet))
	for _, candidate := range []broker.Tier{broker.TierEconomy, broker.TierBalanced, broker.TierFrontier} {
		if _, found := tierSet[string(candidate)]; found {
			tiers = append(tiers, string(candidate))
		}
	}
	efforts := make([]string, 0, len(effortSet))
	for _, candidate := range []broker.Effort{broker.EffortMedium, broker.EffortHigh} {
		if _, found := effortSet[string(candidate)]; found {
			efforts = append(efforts, string(candidate))
		}
	}
	return tiers, efforts
}

func decodeStrict(raw []byte, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("expected one JSON object")
		}
		return err
	}
	return nil
}

func newCallID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("broker MCP: generate call identity: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return strings.Join([]string{
		encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32],
	}, "-"), nil
}
