package services

import (
	"context"
	"errors"
	"time"

	"ghinbox/internal/app"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/store"
)

// ProseService serves summaries, digests and their settings (M4).
type ProseService struct {
	App *app.App
}

// Summary returns the stored summary for a thread, or nil when none exists.
func (s *ProseService) Summary(accountID int64, threadID string) (*pipeline.SummaryView, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	v, err := s.App.Pipe.Summary(ctx, acct, threadID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Summarize generates (or reuses unless force) the summary for a thread.
func (s *ProseService) Summarize(accountID int64, threadID string, force bool) (pipeline.SummaryView, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return pipeline.SummaryView{}, err
	}
	return s.App.Pipe.Summarize(ctx, acct, threadID, force)
}

// SummarizeTop summarises the n highest-priority threads without a current summary.
func (s *ProseService) SummarizeTop(n int) (int, error) {
	return s.App.Pipe.SummarizeTop(context.Background(), n)
}

// GenerateDigest writes a digest covering the last `hours`.
func (s *ProseService) GenerateDigest(hours int) (pipeline.DigestView, error) {
	if hours <= 0 {
		hours = 24
	}
	return s.App.Pipe.GenerateDigest(context.Background(), time.Now().Add(-time.Duration(hours)*time.Hour))
}

func (s *ProseService) Digests(limit int) ([]pipeline.DigestView, error) {
	return s.App.Pipe.Digests(context.Background(), limit)
}

func (s *ProseService) Settings() (pipeline.ProseSettings, error) {
	return s.App.Pipe.ProseSettings(context.Background())
}

func (s *ProseService) SaveSettings(st pipeline.ProseSettings) error {
	return s.App.Pipe.SetProseSettings(context.Background(), st)
}

// TestNotification sends a sample desktop notification.
func (s *ProseService) TestNotification() error {
	if s.App.Notify == nil {
		return errors.New("notifications are not available in this build")
	}
	s.App.Notify(pipeline.Notification{Title: "GH Inbox", Body: "Desktop notifications are working."})
	return nil
}
