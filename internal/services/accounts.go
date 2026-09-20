package services

import (
	"context"
	"errors"
	"strings"

	"ghinbox/internal/app"
	"ghinbox/internal/store"
)

// AccountsService manages GitHub accounts and their tokens.
type AccountsService struct {
	App *app.App
}

// AccountView is an account plus its sync state.
type AccountView struct {
	Account store.Account   `json:"account"`
	Sync    store.SyncState `json:"sync"`
}

func (s *AccountsService) List() ([]AccountView, error) {
	ctx := context.Background()
	accts, err := s.App.DB.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountView, 0, len(accts))
	for _, a := range accts {
		st, err := s.App.DB.GetSyncState(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, AccountView{Account: a, Sync: st})
	}
	return out, nil
}

// Add validates the token against the forge (get_me / GET /user), stores it in
// the secrets store, and registers the account in read-only write mode.
// forge is "github" (default) or "gitlab"; host defaults per forge.
func (s *AccountsService) Add(forge, token, host string) (store.Account, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return store.Account{}, errors.New("token is required")
	}
	forge = strings.TrimSpace(strings.ToLower(forge))
	if forge == "" {
		forge = store.ForgeGitHub
	}
	if forge == store.ForgeGitHub && s.App.MCPErr != nil {
		return store.Account{}, s.App.MCPErr
	}
	return s.App.Pipe.AddAccount(context.Background(), forge, token, strings.TrimSpace(host))
}

func (s *AccountsService) Remove(id int64) error {
	return s.App.Pipe.RemoveAccount(context.Background(), id)
}

// SetWriteMode switches between "readonly" and "notifications" (PLAN.md §4.9).
func (s *AccountsService) SetWriteMode(id int64, mode string) error {
	return s.App.Pipe.SetWriteMode(context.Background(), id, mode)
}
