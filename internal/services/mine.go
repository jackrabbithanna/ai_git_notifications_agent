package services

import (
	"context"

	"ghinbox/internal/app"
	"ghinbox/internal/store"
)

// MineService serves issues/PRs assigned to, mentioning, review-requested from, or authored by the account.
type MineService struct {
	App *app.App
}

// List returns items across all accounts (accountID 0) or one account.
func (s *MineService) List(accountID int64, includeClosed bool) ([]store.Item, error) {
	return s.App.DB.ListItems(context.Background(), accountID, includeClosed)
}

// Refresh re-runs the searches for one account now.
func (s *MineService) Refresh(accountID int64) (int, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return s.App.Pipe.SyncMine(ctx, acct)
}
