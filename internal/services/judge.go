package services

import (
	"context"
	"errors"

	"ghinbox/internal/app"
	"ghinbox/internal/judge/ollama"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/scoring"
	"ghinbox/internal/store"
)

// JudgeService configures and runs the triage judge (M2).
type JudgeService struct {
	App *app.App
}

// Status tells the UI which provider would run and why not, plus stored-judgment stats.
type JudgeStatus struct {
	Settings   pipeline.JudgeSettings `json:"settings"`
	HasJevKey  bool                   `json:"hasJevKey"`
	Provider   string                 `json:"provider"` // resolved provider name or ""
	Calibrated bool                   `json:"calibrated"`
	Error      string                 `json:"error"` // why no provider resolves
	Stats      store.JudgmentStats    `json:"stats"`
}

func (s *JudgeService) Status() (JudgeStatus, error) {
	ctx := context.Background()
	settings, err := s.App.Pipe.JudgeSettings(ctx)
	if err != nil {
		return JudgeStatus{}, err
	}
	st := JudgeStatus{Settings: settings, HasJevKey: s.App.Pipe.HasJevKey()}
	if j, _, err := s.App.Pipe.Judge(ctx); err != nil {
		st.Error = err.Error()
	} else {
		st.Provider = j.Name()
		st.Calibrated = j.Calibrated()
	}
	st.Stats, _ = s.App.DB.JudgmentStatsFor(ctx, "triage.v1")
	return st, nil
}

func (s *JudgeService) SaveSettings(settings pipeline.JudgeSettings) error {
	return s.App.Pipe.SetJudgeSettings(context.Background(), settings)
}

// SetJevKey stores (or, when empty, removes) the Jev API key in the secrets store.
func (s *JudgeService) SetJevKey(key string) error {
	return s.App.Pipe.SetJevKey(key)
}

// OllamaModels lists models on the configured Ollama server.
func (s *JudgeService) OllamaModels(baseURL string) ([]string, error) {
	if baseURL == "" {
		settings, _ := s.App.Pipe.JudgeSettings(context.Background())
		baseURL = settings.OllamaURL
	}
	return ollama.New(ollama.Config{BaseURL: baseURL, Model: "-"}).Models(context.Background())
}

func (s *JudgeService) Weights() (scoring.Weights, error) {
	return s.App.Pipe.Weights(context.Background())
}

func (s *JudgeService) SaveWeights(w scoring.Weights) error {
	return s.App.Pipe.SetWeights(context.Background(), w)
}

func (s *JudgeService) DefaultWeights() scoring.Weights { return scoring.Defaults() }

// Run judges pending threads for one account (0 = all).
func (s *JudgeService) Run(accountID int64, limit int) ([]pipeline.JudgeReport, error) {
	ctx := context.Background()
	if accountID == 0 {
		return s.App.Pipe.JudgeAll(ctx)
	}
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rep, err := s.App.Pipe.JudgeAccount(ctx, acct, limit)
	if err != nil && rep.Error == "" {
		rep.Error = err.Error()
	}
	if errors.Is(err, pipeline.ErrJudgeOff) {
		return []pipeline.JudgeReport{rep}, err
	}
	return []pipeline.JudgeReport{rep}, nil
}

// Explain returns state, answers, probabilities and score for one thread; judgeNow forces a fresh judgment.
func (s *JudgeService) Explain(accountID int64, threadID string, judgeNow bool) (pipeline.Explanation, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return pipeline.Explanation{}, err
	}
	return s.App.Pipe.Explain(ctx, acct, threadID, judgeNow)
}
