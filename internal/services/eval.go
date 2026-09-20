package services

import (
	"context"
	"os"
	"path/filepath"

	"gitinbox/internal/app"
	"gitinbox/internal/eval"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/store"
)

// EvalService serves labeling and evaluation (M5).
type EvalService struct {
	App *app.App
}

func (s *EvalService) Queue(accountID int64, limit int, includeLabeled bool) ([]pipeline.LabelQueueItem, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.App.Pipe.LabelQueue(context.Background(), accountID, limit, includeLabeled)
}

func (s *EvalService) SetLabel(l store.Label) error {
	return s.App.Pipe.SetLabel(context.Background(), l)
}

func (s *EvalService) SetPRLabel(l store.PRLabel) error {
	return s.App.Pipe.SetPRLabel(context.Background(), l)
}

func (s *EvalService) PRLabels() (map[string]store.PRLabel, error) {
	return s.App.DB.ListPRLabels(context.Background())
}

func (s *EvalService) Overview() (pipeline.EvalOverview, error) {
	return s.App.Pipe.EvalOverview(context.Background())
}

// Evaluate reports one provider ("primary", "jev", "ollama").
func (s *EvalService) Evaluate(provider string) (eval.Report, error) {
	return s.App.Pipe.Evaluate(context.Background(), provider)
}

func (s *EvalService) EvaluateImpact() (pipeline.ImpactEval, error) {
	return s.App.Pipe.EvaluateImpact(context.Background())
}

// EvalJudge re-judges the labeled set with a named provider for comparison.
func (s *EvalService) EvalJudge(provider, model string, force bool) (pipeline.EvalJudgeReport, error) {
	rep, err := s.App.Pipe.EvalJudge(context.Background(), provider, model, force)
	if err != nil && len(rep.Errors) == 0 {
		rep.Errors = []string{err.Error()}
	}
	return rep, nil
}

func (s *EvalService) Tune(iters int, apply bool) (eval.TuneResult, error) {
	return s.App.Pipe.Tune(context.Background(), iters, apply)
}

// Report renders the Markdown report and also writes it next to the database.
func (s *EvalService) Report() (string, error) {
	md, err := s.App.Pipe.ReportMarkdown(context.Background())
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(s.App.DBPath), "eval")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "report-latest.md"), []byte(md), 0o644)
	}
	return md, nil
}
