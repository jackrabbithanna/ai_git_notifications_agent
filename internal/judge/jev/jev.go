// Package jev is the TypeSafe System One (Jev) client — the calibrated judge.
// Contract: docs/typesafeapi.md (POST /v1/systemone; noul/choice/score;
// 401/422/429/529 semantics).
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"time"

	"ghinbox/internal/judge"
)

const (
	DefaultBaseURL = "https://api.typesafe.ai"
	DefaultModel   = "jev-latest"
	endpoint       = "/v1/systemone"
)

// Config for the client.
type Config struct {
	APIKey     string
	BaseURL    string // default DefaultBaseURL
	Model      string // default DefaultModel
	HTTPClient *http.Client
	MaxRetries int           // on 429/529; default 4
	Backoff    time.Duration // initial backoff; default 1s
}

// Client implements judge.Judge.
type Client struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 4
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 45 * time.Second}
	}
	return &Client{cfg: cfg, hc: hc}
}

func (c *Client) Name() string     { return "jev" }
func (c *Client) Calibrated() bool { return true }

// wire types -----------------------------------------------------------------

type request struct {
	State     any                     `json:"state"`
	Model     string                  `json:"model"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

type response struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Encode turns questions into the wire map (exported for tests/diagnostics).
func Encode(questions []judge.Question) (map[string]wireQuestion, error) {
	out := make(map[string]wireQuestion, len(questions))
	for _, q := range questions {
		if err := q.Validate(); err != nil {
			return nil, err
		}
		w := wireQuestion{Type: string(q.Kind), Instructions: q.Instructions}
		switch q.Kind {
		case judge.Noul:
			if q.NoulTrue != "" || q.NoulFalse != "" {
				w.Criteria = map[string]string{"true": q.NoulTrue, "false": q.NoulFalse}
			}
		case judge.Choice:
			m := make(map[string]string, len(q.Options))
			for _, o := range q.Options {
				m[o.ID] = o.Description
			}
			w.Criteria = m
		case judge.Score:
			w.Criteria = q.Levels
		}
		out[q.ID] = w
	}
	return out, nil
}

// Ask sends one request with every question over the same state.
func (c *Client) Ask(ctx context.Context, state any, questions []judge.Question) (judge.Response, error) {
	if c.cfg.APIKey == "" {
		return judge.Response{}, judge.ErrUnauthorized
	}
	qs, err := Encode(questions)
	if err != nil {
		return judge.Response{}, err
	}
	body, err := json.Marshal(request{State: state, Model: c.cfg.Model, Questions: qs})
	if err != nil {
		return judge.Response{}, err
	}
	start := time.Now()
	var resp response
	for attempt := 0; ; attempt++ {
		status, raw, err := c.post(ctx, body)
		if err != nil {
			return judge.Response{}, fmt.Errorf("jev: %w", err)
		}
		switch {
		case status == http.StatusOK:
			if err := json.Unmarshal(raw, &resp); err != nil {
				return judge.Response{}, fmt.Errorf("jev: decode response: %w", err)
			}
			return c.convert(resp, questions, time.Since(start))
		case status == http.StatusUnauthorized:
			return judge.Response{}, judge.ErrUnauthorized
		case status == http.StatusUnprocessableEntity:
			return judge.Response{}, fmt.Errorf("%w: %s", judge.ErrInvalidRequest, truncate(raw, 400))
		case status == http.StatusTooManyRequests || status == 529 || status == http.StatusServiceUnavailable:
			if attempt >= c.cfg.MaxRetries {
				return judge.Response{}, fmt.Errorf("%w: HTTP %d after %d attempts", judge.ErrUnavailable, status, attempt+1)
			}
			if err := sleep(ctx, backoff(c.cfg.Backoff, attempt)); err != nil {
				return judge.Response{}, err
			}
		default:
			return judge.Response{}, fmt.Errorf("jev: HTTP %d: %s", status, truncate(raw, 300))
		}
	}
}

func (c *Client) post(ctx context.Context, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, raw, err
}

func (c *Client) convert(r response, questions []judge.Question, latency time.Duration) (judge.Response, error) {
	out := judge.Response{Provider: "jev", Model: r.Model, Calibrated: true, Answers: map[string]judge.Answer{}, Latency: latency}
	out.Usage = judge.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	for _, q := range questions {
		w, ok := r.Answers[q.ID]
		if !ok {
			return out, fmt.Errorf("jev: no answer for question %q", q.ID)
		}
		a := judge.Answer{Kind: q.Kind}
		switch q.Kind {
		case judge.Noul:
			if w.Noul == nil {
				return out, fmt.Errorf("jev: noul answer %q missing value", q.ID)
			}
			a.Noul = *w.Noul
		case judge.Choice:
			keys := make([]string, 0, len(q.Options))
			for _, o := range q.Options {
				keys = append(keys, o.ID)
			}
			a.Probabilities, _ = judge.NormalizeProbabilities(w.Probabilities, keys)
			a.Choice = w.Choice
			if a.Choice == "" {
				_, a.Choice = judge.NormalizeProbabilities(w.Probabilities, keys)
			}
			if w.Confidence != nil {
				a.Confidence = *w.Confidence
			} else {
				a.Confidence = judge.TopProbability(a.Probabilities)
			}
		case judge.Score:
			keys := make([]string, len(q.Levels))
			for i := range q.Levels {
				keys[i] = strconv.Itoa(i)
			}
			a.Probabilities, _ = judge.NormalizeProbabilities(w.Probabilities, keys)
			if w.Score != nil {
				a.Score = *w.Score
			} else {
				for i := range keys {
					a.Score += float64(i) * a.Probabilities[keys[i]]
				}
			}
			a.Legend = legendSlice(w.Legend, q.Levels)
			if w.Confidence != nil {
				a.Confidence = *w.Confidence
			} else {
				a.Confidence = judge.TopProbability(a.Probabilities)
			}
		}
		out.Answers[q.ID] = a
	}
	return out, nil
}

// legendSlice orders the "0".."n" legend map; falls back to the question's levels.
func legendSlice(m map[string]string, levels []string) []string {
	if len(m) == 0 {
		return append([]string(nil), levels...)
	}
	idx := make([]int, 0, len(m))
	for k := range m {
		if i, err := strconv.Atoi(k); err == nil {
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, m[strconv.Itoa(i)])
	}
	if len(out) != len(levels) {
		return append([]string(nil), levels...)
	}
	return out
}

func backoff(base time.Duration, attempt int) time.Duration {
	d := base << uint(attempt)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(d) / 2))
	return d/2 + jitter
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var _ judge.Judge = (*Client)(nil)

// ErrNoKey helps callers distinguish "not configured" from "rejected".
var ErrNoKey = errors.New("jev: no API key configured")
