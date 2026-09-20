package services

import (
	"context"
	"sort"
	"time"

	"ghinbox/internal/app"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/scoring"
	"ghinbox/internal/store"
)

// InboxService serves the Inbox view and local triage actions.
type InboxService struct {
	App *app.App
}

// InboxQuery mirrors store.ThreadQuery for the frontend.
type InboxQuery struct {
	AccountID    int64  `json:"accountId"`
	IncludeRead  bool   `json:"includeRead"`
	IncludeNoise bool   `json:"includeNoise"`
	IncludeDone  bool   `json:"includeDone"`
	Repo         string `json:"repo"`
	Limit        int    `json:"limit"`
}

// Group is one Inbox section: a "needs me" bucket first (judged threads that
// require action or are pinned), then activity kinds. Threads are ordered by
// priority within a group (PLAN.md §4.6).
type Group struct {
	Kind    string            `json:"kind"`
	Label   string            `json:"label"`
	Threads []pipeline.Scored `json:"threads"`
}

// InboxView is the grouped inbox plus counts.
type InboxView struct {
	Groups   []Group      `json:"groups"`
	Counts   store.Counts `json:"counts"`
	Total    int          `json:"total"`
	Judged   int          `json:"judged"`
	Unjudged int          `json:"unjudged"`
}

var kindOrder = []struct{ Kind, Label string }{
	{"review_requested", "Review requested"},
	{"mention", "Mentions"},
	{"assignment", "Assigned to me"},
	{"new_pr", "Pull requests: new or pushed (no new comments)"},
	{"new_issue", "Issues: new (no comments yet)"},
	{"review", "Reviews"},
	{"comment", "Replies & comments"},
	{"state_change", "Merged / closed"},
	{"ci", "CI"},
	{"security", "Security"},
	{"release", "Releases"},
	{"discussion", "Discussions"},
	{"commit", "Commits"},
	{"other", "Other"},
}

func (s *InboxService) List(q InboxQuery) (InboxView, error) {
	ctx := context.Background()
	limit := q.Limit
	if limit <= 0 {
		limit = 500
	}
	threads, err := s.App.DB.ListThreads(ctx, store.ThreadQuery{
		AccountID: q.AccountID, IncludeRead: q.IncludeRead, IncludeNoise: q.IncludeNoise, IncludeDone: q.IncludeDone, Repo: q.Repo, Limit: limit,
	})
	if err != nil {
		return InboxView{}, err
	}
	scored, err := s.App.Pipe.ScoreThreads(ctx, threads)
	if err != nil {
		return InboxView{}, err
	}
	view := InboxView{Total: len(threads)}
	var needsMe, resolved []pipeline.Scored
	byKind := map[string][]pipeline.Scored{}
	for _, sc := range scored {
		if sc.Score.Judged {
			view.Judged++
		} else {
			view.Unjudged++
		}
		switch {
		case sc.Score.Pinned || sc.Score.Bucket == scoring.BucketNeedsMe:
			needsMe = append(needsMe, sc)
		case sc.Score.Bucket == scoring.BucketResolved:
			resolved = append(resolved, sc)
		default:
			byKind[sc.Thread.ActivityKind] = append(byKind[sc.Thread.ActivityKind], sc)
		}
	}
	byPriority := func(ts []pipeline.Scored) {
		sort.SliceStable(ts, func(i, j int) bool {
			if ts[i].Score.Pinned != ts[j].Score.Pinned {
				return ts[i].Score.Pinned
			}
			if ts[i].Score.Priority != ts[j].Score.Priority {
				return ts[i].Score.Priority > ts[j].Score.Priority
			}
			return ts[i].Thread.UpdatedAt.After(ts[j].Thread.UpdatedAt)
		})
	}
	if len(needsMe) > 0 {
		byPriority(needsMe)
		view.Groups = append(view.Groups, Group{Kind: "needs_me", Label: "Needs me", Threads: needsMe})
	}
	seen := map[string]bool{}
	for _, k := range kindOrder {
		if ts := byKind[k.Kind]; len(ts) > 0 {
			byPriority(ts)
			view.Groups = append(view.Groups, Group{Kind: k.Kind, Label: k.Label, Threads: ts})
		}
		seen[k.Kind] = true
	}
	var extra []string
	for k := range byKind {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		byPriority(byKind[k])
		view.Groups = append(view.Groups, Group{Kind: k, Label: k, Threads: byKind[k]})
	}
	if len(resolved) > 0 {
		byPriority(resolved)
		view.Groups = append(view.Groups, Group{Kind: "resolved", Label: "Resolved (judged)", Threads: resolved})
	}
	view.Counts, err = s.App.DB.Counts(ctx, q.AccountID)
	return view, err
}

// Sync runs a sync for every account and returns the reports.
func (s *InboxService) Sync(full bool) ([]pipeline.SyncReport, error) {
	if s.App.MCPErr != nil {
		return nil, s.App.MCPErr
	}
	return s.App.Pipe.SyncAll(context.Background(), full)
}

func (s *InboxService) account(ctx context.Context, id int64) (store.Account, error) {
	return s.App.DB.GetAccount(ctx, id)
}

func (s *InboxService) MarkRead(accountID int64, threadID string) (pipeline.MirrorResult, error) {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return pipeline.MirrorResult{}, err
	}
	return s.App.Pipe.MarkRead(ctx, acct, threadID)
}

func (s *InboxService) MarkDone(accountID int64, threadID string) (pipeline.MirrorResult, error) {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return pipeline.MirrorResult{}, err
	}
	return s.App.Pipe.MarkDone(ctx, acct, threadID)
}

func (s *InboxService) UndoDone(accountID int64, threadID string) error {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	return s.App.Pipe.UndoDone(ctx, acct, threadID)
}

// Snooze hides the thread for the given number of hours (new activity un-snoozes it).
func (s *InboxService) Snooze(accountID int64, threadID string, hours int) error {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	if hours <= 0 {
		hours = 24
	}
	return s.App.Pipe.Snooze(ctx, acct, threadID, time.Now().Add(time.Duration(hours)*time.Hour))
}

func (s *InboxService) Unsnooze(accountID int64, threadID string) error {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	return s.App.Pipe.Unsnooze(ctx, acct, threadID)
}

// Unsubscribe ignores the thread on GitHub when write mode allows; always hides it locally.
func (s *InboxService) Unsubscribe(accountID int64, threadID string) (pipeline.MirrorResult, error) {
	ctx := context.Background()
	acct, err := s.account(ctx, accountID)
	if err != nil {
		return pipeline.MirrorResult{}, err
	}
	return s.App.Pipe.Unsubscribe(ctx, acct, threadID)
}
