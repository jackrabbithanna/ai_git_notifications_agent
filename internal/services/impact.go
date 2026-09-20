package services

import (
	"context"
	"errors"

	"gitinbox/internal/app"
	"gitinbox/internal/llm"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/profiles"
	"gitinbox/internal/store"
)

// ImpactService serves the Impact view and on-demand analyses (M3).
type ImpactService struct {
	App *app.App
}

// ImpactQuery filters the Impact view.
type ImpactQuery struct {
	AccountID int64 `json:"accountId"`
	MinLevel  int   `json:"minLevel"` // 0 none … 3 certain
	Landed    bool  `json:"landed"`   // merged only
	Limit     int   `json:"limit"`
}

func (s *ImpactService) List(q ImpactQuery) ([]pipeline.ImpactView, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	return s.App.Pipe.ListImpact(context.Background(), store.AnalysisQuery{AccountID: q.AccountID, MinLevel: q.MinLevel, Landed: q.Landed, Limit: limit})
}

// Get returns one PR/MR's analysis (nil when not analysed yet).
func (s *ImpactService) Get(accountID int64, repo string, number int) (*pipeline.ImpactView, error) {
	return s.App.Pipe.GetImpact(context.Background(), accountID, repo, number)
}

// Analyze runs (or re-runs with force) the analysis for one PR/MR; profileID "" = auto.
func (s *ImpactService) Analyze(accountID int64, repo string, number int, profileID string, force bool) (store.Analysis, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return store.Analysis{}, err
	}
	a, _, err := s.App.Pipe.AnalyzePR(ctx, acct, repo, number, profileID, force)
	return a, err
}

// Note generates the impact note for an analysed PR (Ollama).
func (s *ImpactService) Note(accountID int64, repo string, number int) (llm.ImpactNote, error) {
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return llm.ImpactNote{}, err
	}
	return s.App.Pipe.ImpactNote(ctx, acct, repo, number)
}

// RunPending analyses pending PR threads (and scans landed changes) for one account (0 = all).
func (s *ImpactService) RunPending(accountID int64) ([]pipeline.ImpactReport, error) {
	ctx := context.Background()
	accts, err := s.App.DB.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []pipeline.ImpactReport
	for _, a := range accts {
		if accountID != 0 && a.ID != accountID {
			continue
		}
		rep, err := s.App.Pipe.AnalyzePending(ctx, a, 0)
		if err != nil && rep.Error == "" {
			rep.Error = err.Error()
		}
		out = append(out, rep)
		if errors.Is(err, pipeline.ErrJudgeOff) {
			break
		}
	}
	return out, nil
}

func (s *ImpactService) Settings() (pipeline.ImpactSettings, error) {
	return s.App.Pipe.ImpactSettings(context.Background())
}

func (s *ImpactService) SaveSettings(st pipeline.ImpactSettings) error {
	return s.App.Pipe.SetImpactSettings(context.Background(), st)
}

// ProfilesService manages impact profiles (built-in + user YAML).
type ProfilesService struct {
	App *app.App
}

// ProfilesView lists profiles plus parse problems of user profiles.
type ProfilesView struct {
	Profiles []profiles.Profile `json:"profiles"`
	Problems []string           `json:"problems"`
}

func (s *ProfilesService) List() (ProfilesView, error) {
	ps, problems, err := s.App.Pipe.Profiles(context.Background())
	return ProfilesView{Profiles: ps, Problems: problems}, err
}

// Save validates YAML and stores it as a user profile (overrides a built-in with the same id).
func (s *ProfilesService) Save(yaml string, enabled bool) (profiles.Profile, error) {
	return s.App.Pipe.SaveProfile(context.Background(), yaml, enabled)
}

// Validate parses YAML without storing it.
func (s *ProfilesService) Validate(yaml string) (profiles.Profile, error) {
	return profiles.Parse([]byte(yaml))
}

// Delete removes a user profile (a built-in with the same id becomes visible again).
func (s *ProfilesService) Delete(id string) error {
	return s.App.DB.DeleteUserProfile(context.Background(), id)
}

func (s *ProfilesService) SetEnabled(id string, enabled bool) error {
	return s.App.DB.SetProfileEnabled(context.Background(), id, enabled)
}
