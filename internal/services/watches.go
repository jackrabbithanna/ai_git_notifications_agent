package services

import (
	"context"
	"errors"

	"ghinbox/internal/app"
	"ghinbox/internal/store"
)

// WatchesService manages the GitLab projects whose activity an account polls.
type WatchesService struct {
	App *app.App
}

func (s *WatchesService) List(accountID int64) ([]store.WatchedProject, error) {
	return s.App.DB.ListWatched(context.Background(), accountID)
}

// Add registers a project path (e.g. dev/core); it is resolved on the next sync.
func (s *WatchesService) Add(accountID int64, path string) error {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if acct.Forge != store.ForgeGitLab {
		return errors.New("watched projects only apply to GitLab accounts")
	}
	return s.App.DB.AddWatched(ctx, accountID, path)
}

func (s *WatchesService) Remove(accountID int64, path string) error {
	return s.App.DB.RemoveWatched(context.Background(), accountID, path)
}
