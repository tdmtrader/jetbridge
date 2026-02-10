package feedback

import (
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/schema"
)

type pattern struct {
	keywords   []string
	verdict    schema.Verdict
	confidence float64
}

// negationPrefixes are phrases that invert a match.
var negationPrefixes = []string{"not a ", "not ", "isn't a ", "isn't ", "no "}

var patterns = []pattern{
	// Accurate signals.
	{keywords: []string{"good catch", "real bug", "real issue", "correct", "accurate", "valid finding", "agree"}, verdict: schema.VerdictAccurate, confidence: 0.85},
	// False positive signals.
	{keywords: []string{"false positive", "not a bug", "not an issue", "doesn't apply", "expected behavior", "by design", "intended"}, verdict: schema.VerdictFalsePositive, confidence: 0.85},
	// Noisy signals.
	{keywords: []string{"noisy", "not important", "too minor", "trivial", "low priority", "don't care", "not worth"}, verdict: schema.VerdictNoisy, confidence: 0.80},
	// Overly strict signals.
	{keywords: []string{"style issue", "preference", "opinionated", "subjective", "nitpick", "too strict", "overly strict"}, verdict: schema.VerdictOverlyStrict, confidence: 0.80},
	// Partially correct signals.
	{keywords: []string{"partially right", "partially correct", "right area", "wrong diagnosis", "close but", "half right"}, verdict: schema.VerdictPartiallyCorrect, confidence: 0.75},
	// Missed context signals.
	{keywords: []string{"missing context", "lacks context", "needs more context", "doesn't know", "not aware", "can't tell"}, verdict: schema.VerdictMissedContext, confidence: 0.75},
}

// ClassifyVerdict takes a human's natural language response and suggests a verdict.
// Returns the verdict, a confidence score (0.0-1.0), and any error.
func ClassifyVerdict(text string) (schema.Verdict, float64, error) {
	if strings.TrimSpace(text) == "" {
		return "", 0, fmt.Errorf("empty input")
	}

	lower := strings.ToLower(text)

	// Check for negation patterns that flip the verdict.
	// "not a false positive" → accurate
	if containsNegatedFP(lower) {
		return schema.VerdictAccurate, 0.80, nil
	}

	// Match against patterns.
	var bestVerdict schema.Verdict
	var bestConfidence float64

	for _, p := range patterns {
		for _, kw := range p.keywords {
			if strings.Contains(lower, kw) {
				if p.confidence > bestConfidence {
					bestVerdict = p.verdict
					bestConfidence = p.confidence
				}
			}
		}
	}

	if bestVerdict != "" {
		return bestVerdict, bestConfidence, nil
	}

	// Fallback: return low-confidence guess.
	return schema.VerdictAccurate, 0.3, nil
}

// containsNegatedFP checks if the text negates "false positive".
func containsNegatedFP(text string) bool {
	for _, prefix := range negationPrefixes {
		if strings.Contains(text, prefix+"false positive") {
			return true
		}
	}
	return false
}
