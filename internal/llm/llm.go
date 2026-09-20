// Package llm is the generative side (PLAN.md §4.4 impact notes, later
// summaries/digests): a Generator returns a JSON document matching a schema.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Usage is token accounting for one generation.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Result is one generation.
type Result struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	JSON     json.RawMessage `json:"json"`
	Usage    Usage           `json:"usage"`
	Latency  time.Duration   `json:"latency"`
}

// Generator produces schema-constrained JSON from a system + user prompt.
type Generator interface {
	Name() string
	Generate(ctx context.Context, system, user string, schema map[string]any) (Result, error)
}

// ErrUnavailable means the provider could not be reached or has no model.
var ErrUnavailable = errors.New("llm: provider unavailable")

// ImpactNote is the structured note written for an analysed pull request.
type ImpactNote struct {
	WhatChanged               string   `json:"what_changed"`
	WhyItMattersForDownstream string   `json:"why_it_matters_for_downstream"`
	SurfacesChanged           []string `json:"surfaces_changed"`
	RecommendedChecks         []string `json:"recommended_checks"`
	MigrationHints            string   `json:"migration_hints"`
	ConfidenceNote            string   `json:"confidence_note"`
}

// ImpactNoteSchema constrains ImpactNote generation.
func ImpactNoteSchema() map[string]any {
	str := map[string]any{"type": "string"}
	arr := map[string]any{"type": "array", "items": str}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"what_changed":                  str,
			"why_it_matters_for_downstream": str,
			"surfaces_changed":              arr,
			"recommended_checks":            arr,
			"migration_hints":               str,
			"confidence_note":               str,
		},
		"required": []string{"what_changed", "why_it_matters_for_downstream", "surfaces_changed", "recommended_checks", "migration_hints", "confidence_note"},
	}
}

// ThreadSummary is the structured summary of one notification thread (M4).
type ThreadSummary struct {
	Summary              string   `json:"summary"`                 // 1–2 sentences: what this thread is about and where it stands
	KeyPoints            []string `json:"key_points"`              // ≤5 bullets
	AsksOfMe             []string `json:"asks_of_me"`              // explicit requests aimed at the user; empty when none
	ChangedSinceLastRead string   `json:"changed_since_last_read"` // what is new relative to the previous summary; "" when first summary
}

// ThreadSummarySchema constrains ThreadSummary generation.
func ThreadSummarySchema() map[string]any {
	str := map[string]any{"type": "string"}
	arr := map[string]any{"type": "array", "items": str}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":                 str,
			"key_points":              arr,
			"asks_of_me":              arr,
			"changed_since_last_read": str,
		},
		"required": []string{"summary", "key_points", "asks_of_me", "changed_since_last_read"},
	}
}

// DigestItem is one thread or PR mentioned in a digest.
type DigestItem struct {
	Ref   string `json:"ref"`   // "owner/repo#123" or "group/project!12" as given in the input
	Title string `json:"title"` // short
	Why   string `json:"why"`   // why it is in this section, one sentence
}

// DigestSection groups items under a heading.
type DigestSection struct {
	Title string       `json:"title"`
	Items []DigestItem `json:"items"`
}

// Digest is the structured overview of a period.
type Digest struct {
	Headline         string          `json:"headline"` // one sentence
	Sections         []DigestSection `json:"sections"` // e.g. Needs you · Impact on your projects · Worth knowing
	SuggestedActions []string        `json:"suggested_actions"`
}

// DigestSchema constrains Digest generation.
func DigestSchema() map[string]any {
	str := map[string]any{"type": "string"}
	item := map[string]any{"type": "object", "properties": map[string]any{"ref": str, "title": str, "why": str}, "required": []string{"ref", "title", "why"}}
	section := map[string]any{"type": "object", "properties": map[string]any{"title": str, "items": map[string]any{"type": "array", "items": item}}, "required": []string{"title", "items"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"headline":          str,
			"sections":          map[string]any{"type": "array", "items": section},
			"suggested_actions": map[string]any{"type": "array", "items": str},
		},
		"required": []string{"headline", "sections", "suggested_actions"},
	}
}
