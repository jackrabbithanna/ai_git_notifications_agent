// Package ollama is the uncalibrated fallback judge: one chat completion with a
// JSON-schema `format` per question set. Probabilities are self-reported by the
// model, so Calibrated() is false and scoring must treat them accordingly.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitinbox/internal/judge"
	llmollama "gitinbox/internal/llm/ollama"
)

const DefaultURL = "http://localhost:11434"

// Config for the judge.
type Config struct {
	BaseURL    string // default DefaultURL
	Model      string // required
	HTTPClient *http.Client
}

// Judge implements judge.Judge over /api/chat.
type Judge struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) *Judge {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// Large local models can take minutes to load into VRAM on first use.
		hc = &http.Client{Timeout: 15 * time.Minute}
	}
	return &Judge{cfg: cfg, hc: hc}
}

func (j *Judge) Name() string     { return "ollama" }
func (j *Judge) Calibrated() bool { return false }

const systemPrompt = `You are a careful, calibrated triage judge for a software developer's notification inbox.
You answer a fixed set of questions about a JSON "state". Answer ONLY with JSON matching the schema.
Rules:
- For yes/no questions give "probability" in 0..1 that the answer is YES.
- For choice questions give "choice" (one option id) and "probabilities": a distribution over ALL option ids that sums to 1.
- For score questions give "probabilities": a distribution over the level indexes "0".."n-1" (as strings) that sums to 1; higher index = higher level.
- Use the state only; do not invent facts. Spread probability when the evidence is ambiguous.`

// Ask renders the questions into one prompt and parses the schema-constrained reply.
func (j *Judge) Ask(ctx context.Context, state any, questions []judge.Question) (judge.Response, error) {
	if j.cfg.Model == "" {
		return judge.Response{}, fmt.Errorf("%w: no ollama model configured", judge.ErrInvalidRequest)
	}
	for _, q := range questions {
		if err := q.Validate(); err != nil {
			return judge.Response{}, err
		}
	}
	stateJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return judge.Response{}, err
	}
	schema := Schema(questions)
	user := "STATE:\n" + string(stateJSON) + "\n\nQUESTIONS:\n" + renderQuestions(questions)
	payload := map[string]any{
		"model":      j.cfg.Model,
		"stream":     false,
		"format":     schema,
		"keep_alive": "30m", // keep the model resident between threads of one run
		"options": map[string]any{
			"temperature": 0,
		},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": user},
		},
	}
	start := time.Now()
	raw, status, err := llmollama.PostChat(ctx, j.hc, j.cfg.BaseURL, payload)
	if err != nil {
		return judge.Response{}, fmt.Errorf("%w: ollama: %v", judge.ErrUnavailable, err)
	}
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			return judge.Response{}, fmt.Errorf("%w: ollama: model %q not found (pull it first)", judge.ErrInvalidRequest, j.cfg.Model)
		}
		return judge.Response{}, fmt.Errorf("%w: ollama HTTP %d: %s", judge.ErrUnavailable, status, truncate(raw, 300))
	}
	var chat struct {
		Model   string `json:"model"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount int `json:"prompt_eval_count"`
		EvalCount       int `json:"eval_count"`
	}
	if err := json.Unmarshal(raw, &chat); err != nil {
		return judge.Response{}, fmt.Errorf("ollama: decode chat response: %w", err)
	}
	var answers map[string]json.RawMessage
	if err := json.Unmarshal([]byte(chat.Message.Content), &answers); err != nil {
		return judge.Response{}, fmt.Errorf("ollama: model output is not the requested JSON: %w: %s", err, truncate([]byte(chat.Message.Content), 300))
	}
	out := judge.Response{Provider: "ollama", Model: chat.Model, Calibrated: false, Answers: map[string]judge.Answer{}, Latency: time.Since(start),
		Usage: judge.Usage{InputTokens: chat.PromptEvalCount, OutputTokens: chat.EvalCount}}
	if out.Model == "" {
		out.Model = j.cfg.Model
	}
	for _, q := range questions {
		rawA, ok := answers[q.ID]
		if !ok {
			return out, fmt.Errorf("ollama: no answer for question %q", q.ID)
		}
		a, err := parseAnswer(q, rawA)
		if err != nil {
			return out, err
		}
		out.Answers[q.ID] = a
	}
	return out, nil
}

func parseAnswer(q judge.Question, raw json.RawMessage) (judge.Answer, error) {
	a := judge.Answer{Kind: q.Kind}
	switch q.Kind {
	case judge.Noul:
		var v struct {
			Probability float64 `json:"probability"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return a, fmt.Errorf("ollama: %s: %w", q.ID, err)
		}
		a.Noul = clamp01(v.Probability)
	case judge.Choice:
		var v struct {
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return a, fmt.Errorf("ollama: %s: %w", q.ID, err)
		}
		keys := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			keys = append(keys, o.ID)
		}
		var best string
		a.Probabilities, best = judge.NormalizeProbabilities(v.Probabilities, keys)
		a.Choice = v.Choice
		if _, ok := a.Probabilities[a.Choice]; !ok || a.Choice == "" {
			a.Choice = best
		}
		a.Confidence = judge.TopProbability(a.Probabilities)
	case judge.Score:
		var v struct {
			Probabilities map[string]float64 `json:"probabilities"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return a, fmt.Errorf("ollama: %s: %w", q.ID, err)
		}
		keys := make([]string, len(q.Levels))
		for i := range q.Levels {
			keys[i] = strconv.Itoa(i)
		}
		a.Probabilities, _ = judge.NormalizeProbabilities(v.Probabilities, keys)
		for i, k := range keys {
			a.Score += float64(i) * a.Probabilities[k]
		}
		a.Legend = append([]string(nil), q.Levels...)
		a.Confidence = judge.TopProbability(a.Probabilities)
	}
	return a, nil
}

// Schema builds the JSON schema the model must follow.
func Schema(questions []judge.Question) map[string]any {
	props := map[string]any{}
	required := make([]string, 0, len(questions))
	for _, q := range questions {
		required = append(required, q.ID)
		switch q.Kind {
		case judge.Noul:
			props[q.ID] = map[string]any{"type": "object", "properties": map[string]any{"probability": map[string]any{"type": "number"}}, "required": []string{"probability"}}
		case judge.Choice:
			ids := make([]string, 0, len(q.Options))
			pp := map[string]any{}
			for _, o := range q.Options {
				ids = append(ids, o.ID)
				pp[o.ID] = map[string]any{"type": "number"}
			}
			props[q.ID] = map[string]any{"type": "object", "properties": map[string]any{
				"choice":        map[string]any{"type": "string", "enum": ids},
				"probabilities": map[string]any{"type": "object", "properties": pp, "required": ids},
			}, "required": []string{"choice", "probabilities"}}
		case judge.Score:
			keys := make([]string, len(q.Levels))
			pp := map[string]any{}
			for i := range q.Levels {
				keys[i] = strconv.Itoa(i)
				pp[keys[i]] = map[string]any{"type": "number"}
			}
			props[q.ID] = map[string]any{"type": "object", "properties": map[string]any{
				"probabilities": map[string]any{"type": "object", "properties": pp, "required": keys},
			}, "required": []string{"probabilities"}}
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func renderQuestions(questions []judge.Question) string {
	var b strings.Builder
	for i, q := range questions {
		fmt.Fprintf(&b, "%d. id=%q (%s): %s\n", i+1, q.ID, q.Kind, q.Instructions)
		switch q.Kind {
		case judge.Noul:
			if q.NoulTrue != "" {
				fmt.Fprintf(&b, "   YES means: %s\n   NO means: %s\n", q.NoulTrue, q.NoulFalse)
			}
		case judge.Choice:
			for _, o := range q.Options {
				fmt.Fprintf(&b, "   - %s: %s\n", o.ID, o.Description)
			}
		case judge.Score:
			for j, l := range q.Levels {
				fmt.Fprintf(&b, "   - level %d: %s\n", j, l)
			}
		}
	}
	return b.String()
}

// Models lists the models the server has (reachability check + Settings picker).
func (j *Judge) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(j.cfg.BaseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	res, err := j.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ollama: %v", judge.ErrUnavailable, err)
	}
	defer res.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("ollama: decode /api/tags: %w", err)
	}
	out := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, m.Name)
	}
	return out, nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var _ judge.Judge = (*Judge)(nil)
