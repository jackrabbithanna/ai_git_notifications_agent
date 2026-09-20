// Package ollama is the Ollama-backed llm.Generator (/api/chat with a JSON-schema format).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitinbox/internal/llm"
)

const DefaultURL = "http://localhost:11434"

type Config struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	KeepAlive  string // default 30m
}

type Generator struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) *Generator {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultURL
	}
	if cfg.KeepAlive == "" {
		cfg.KeepAlive = "30m"
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Minute}
	}
	return &Generator{cfg: cfg, hc: hc}
}

func (g *Generator) Name() string { return "ollama" }

func (g *Generator) Generate(ctx context.Context, system, user string, schema map[string]any) (llm.Result, error) {
	if g.cfg.Model == "" {
		return llm.Result{}, fmt.Errorf("%w: no ollama model configured", llm.ErrUnavailable)
	}
	payload := map[string]any{
		"model":      g.cfg.Model,
		"stream":     false,
		"format":     schema,
		"keep_alive": g.cfg.KeepAlive,
		"options":    map[string]any{"temperature": 0.2},
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	start := time.Now()
	raw, status, err := PostChat(ctx, g.hc, g.cfg.BaseURL, payload)
	if err != nil {
		return llm.Result{}, fmt.Errorf("%w: ollama: %v", llm.ErrUnavailable, err)
	}
	if status != http.StatusOK {
		return llm.Result{}, fmt.Errorf("%w: ollama HTTP %d: %.300s", llm.ErrUnavailable, status, raw)
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
		return llm.Result{}, fmt.Errorf("ollama: decode: %w", err)
	}
	if !json.Valid([]byte(chat.Message.Content)) {
		return llm.Result{}, fmt.Errorf("ollama: model output is not JSON: %.300s", chat.Message.Content)
	}
	out := llm.Result{Provider: "ollama", Model: chat.Model, JSON: json.RawMessage(chat.Message.Content), Latency: time.Since(start),
		Usage: llm.Usage{InputTokens: chat.PromptEvalCount, OutputTokens: chat.EvalCount}}
	if out.Model == "" {
		out.Model = g.cfg.Model
	}
	return out, nil
}

var _ llm.Generator = (*Generator)(nil)

// PostChat posts to /api/chat with "think": false (skips hidden reasoning on
// thinking models, which dominates their latency). Models that reject the
// flag get one retry without it.
func PostChat(ctx context.Context, hc *http.Client, baseURL string, payload map[string]any) ([]byte, int, error) {
	do := func(withThink bool) ([]byte, int, error) {
		p := make(map[string]any, len(payload)+1)
		for k, v := range payload {
			p[k] = v
		}
		if withThink {
			p["think"] = false
		}
		body, _ := json.Marshal(p)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(baseURL, "/")+"/api/chat", bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return raw, res.StatusCode, nil
	}
	raw, status, err := do(true)
	if err == nil && status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(raw)), "think") {
		return do(false)
	}
	return raw, status, err
}
