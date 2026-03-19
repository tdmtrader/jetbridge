package llm

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	citracing "github.com/concourse/ci-agent/tracing"
)

// GenAI semantic convention attribute keys.
// See: https://opentelemetry.io/docs/specs/semconv/gen-ai/
const (
	AttrGenAISystem              = "gen_ai.system"
	AttrGenAIRequestModel        = "gen_ai.request.model"
	AttrGenAIUsageInputTokens    = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens   = "gen_ai.usage.output_tokens"
	AttrGenAICacheReadTokens     = "gen_ai.usage.cache_read_input_tokens"
	AttrGenAICacheCreationTokens = "gen_ai.usage.cache_creation_input_tokens"
	AttrGenAICostUSD             = "gen_ai.cost_usd"
	AttrGenAIDurationAPIMS       = "gen_ai.duration_api_ms"
	AttrGenAINumTurns            = "gen_ai.num_turns"
)

// TracingClient wraps a Client and emits OTel spans for each LLM call
// with GenAI semantic convention attributes.
type TracingClient struct {
	Inner Client
}

// NewTracingClient wraps an existing Client with OTel tracing.
func NewTracingClient(inner Client) *TracingClient {
	return &TracingClient{Inner: inner}
}

// Call delegates to the inner client and records a span with GenAI attributes.
func (t *TracingClient) Call(ctx context.Context, prompt string, opts CallOpts) (CallResult, error) {
	ctx, span := citracing.Tracer().Start(ctx, "gen_ai.invoke",
		trace.WithSpanKind(trace.SpanKindClient),
	)
	defer span.End()

	// Set request attributes before the call.
	span.SetAttributes(attribute.String(AttrGenAISystem, "anthropic"))
	if opts.Model != "" {
		span.SetAttributes(attribute.String(AttrGenAIRequestModel, opts.Model))
	}

	cr, err := t.Inner.Call(ctx, prompt, opts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return cr, err
	}

	// Set response attributes from the call result.
	if cr.Model != "" {
		span.SetAttributes(attribute.String(AttrGenAIRequestModel, cr.Model))
	}
	span.SetAttributes(
		attribute.Int(AttrGenAIUsageInputTokens, cr.Usage.InputTokens),
		attribute.Int(AttrGenAIUsageOutputTokens, cr.Usage.OutputTokens),
	)
	if cr.Usage.CacheReadInputTokens > 0 {
		span.SetAttributes(attribute.Int(AttrGenAICacheReadTokens, cr.Usage.CacheReadInputTokens))
	}
	if cr.Usage.CacheCreationInputTokens > 0 {
		span.SetAttributes(attribute.Int(AttrGenAICacheCreationTokens, cr.Usage.CacheCreationInputTokens))
	}
	if cr.CostUSD > 0 {
		span.SetAttributes(attribute.Float64(AttrGenAICostUSD, cr.CostUSD))
	}
	if cr.DurationAPIMS > 0 {
		span.SetAttributes(attribute.Int(AttrGenAIDurationAPIMS, cr.DurationAPIMS))
	}
	if cr.NumTurns > 0 {
		span.SetAttributes(attribute.Int(AttrGenAINumTurns, cr.NumTurns))
	}

	return cr, nil
}
