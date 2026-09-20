// Package github is the GitHub Source: notifications, "mine" searches and
// notification writes through the bundled github-mcp-server (see ghmcp).
package github

import (
	"context"
	"errors"
	"io"
	"time"

	"gitinbox/internal/classify"
	"gitinbox/internal/ghmcp"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Config mirrors ghmcp.Config plus the account's host.
type Config struct {
	BinaryPath string
	Token      string
	Host       string // "" for github.com
	WriteMode  ghmcp.WriteMode
	Stderr     io.Writer
}

// Source is one account's GitHub connection.
type Source struct {
	client *ghmcp.Client
	host   string
}

// New prepares the source; the server process starts on first use.
func New(cfg Config) *Source {
	return &Source{
		host: cfg.Host,
		client: ghmcp.New(ghmcp.Config{
			BinaryPath: cfg.BinaryPath,
			Token:      cfg.Token,
			Host:       cfg.Host,
			WriteMode:  cfg.WriteMode,
			Stderr:     cfg.Stderr,
		}),
	}
}

// NewFromClient wraps an existing client (tests).
func NewFromClient(c *ghmcp.Client, host string) *Source { return &Source{client: c, host: host} }

// MCP exposes the underlying client for Diagnostics (tools list, counters).
func (s *Source) MCP() *ghmcp.Client { return s.client }

func (s *Source) Forge() string { return store.ForgeGitHub }

// WriteMode lets the pipeline detect a stale source after a mode change.
func (s *Source) WriteMode() string { return string(s.client.Config().WriteMode) }

func (s *Source) Login(ctx context.Context) (string, error) {
	me, err := s.client.GetMe(ctx)
	if err != nil {
		return "", err
	}
	return me.Login, nil
}

// Notifications pages through list_notifications and classifies each thread.
func (s *Source) Notifications(ctx context.Context, opts source.Opts) (source.Result, error) {
	lo := ghmcp.ListNotificationsOpts{PerPage: 50, Since: opts.Since, Filter: "default"}
	if opts.Full {
		lo.Filter = "include_read_notifications"
	}
	var res source.Result
	for page := 1; page <= 40; page++ {
		lo.Page = page
		batch, err := s.client.ListNotifications(ctx, lo)
		if err != nil {
			return res, err
		}
		for _, n := range batch {
			res.Threads = append(res.Threads, s.thread(opts.AccountID, n))
		}
		if len(batch) < lo.PerPage {
			break
		}
	}
	return res, nil
}

// thread maps one notification onto a store row (classified, unfiltered).
func (s *Source) thread(accountID int64, n ghmcp.Notification) store.Thread {
	r := classify.Classify(n)
	return store.Thread{
		AccountID:        accountID,
		ThreadID:         n.ID,
		Repo:             n.Repo,
		SubjectType:      n.SubjectType,
		SubjectURL:       n.SubjectURL,
		SubjectNumber:    n.SubjectNumber(),
		HTMLURL:          classify.HTMLURL(s.host, n),
		Title:            n.Title,
		Reason:           n.Reason,
		Unread:           n.Unread,
		UpdatedAt:        n.UpdatedAt,
		LastReadAt:       n.LastReadAt,
		LatestCommentURL: n.LatestCommentURL,
		ActivityKind:     string(r.Kind),
		RelationTags:     r.RelationTags,
	}
}

// mineQueries are the "mine" searches; review requests only exist for PRs.
var mineQueries = []struct {
	Relation string
	Query    string
	Issues   bool
	PRs      bool
}{
	{"assigned", "assignee:@me is:open", true, true},
	{"mentioned", "mentions:@me is:open", true, true},
	{"review_requested", "review-requested:@me is:open", false, true},
	{"author", "author:@me is:open", true, true},
}

// Mine runs the searches and merges relations per item. AccountID is left for the caller.
func (s *Source) Mine(ctx context.Context) ([]store.Item, error) {
	type key struct {
		repo string
		num  int
	}
	merged := map[key]*store.Item{}
	var order []key
	add := func(rel string, items []ghmcp.Item) {
		for _, it := range items {
			k := key{it.Repo, it.Number}
			cur, ok := merged[k]
			if !ok {
				kind := "issue"
				if it.IsPR {
					kind = "pr"
				}
				cur = &store.Item{Repo: it.Repo, Number: it.Number, Kind: kind, Title: it.Title, State: it.State,
					HTMLURL: it.HTMLURL, Author: it.Author, Assignees: it.Assignees, Labels: it.Labels, Draft: it.Draft, Comments: it.Comments,
					CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}
				merged[k] = cur
				order = append(order, k)
			}
			cur.Relations = source.AppendUnique(cur.Relations, rel)
		}
	}
	for _, q := range mineQueries {
		if q.Issues {
			res, err := s.client.SearchIssues(ctx, q.Query, 1, 100)
			if err != nil {
				return nil, err
			}
			add(q.Relation, res.Items)
		}
		if q.PRs {
			res, err := s.client.SearchPullRequests(ctx, q.Query, 1, 100)
			if err != nil {
				return nil, err
			}
			add(q.Relation, res.Items)
		}
	}
	out := make([]store.Item, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out, nil
}

func (s *Source) MarkRead(ctx context.Context, threadID string) error {
	return wrapWrite(s.client.DismissNotification(ctx, threadID, "read"))
}

func (s *Source) MarkDone(ctx context.Context, threadID string) error {
	return wrapWrite(s.client.DismissNotification(ctx, threadID, "done"))
}

func (s *Source) Unsubscribe(ctx context.Context, threadID string) error {
	return wrapWrite(s.client.ManageNotificationSubscription(ctx, threadID, "ignore"))
}

func (s *Source) Close() error { return s.client.Close() }

// wrapWrite turns the allowlist rejection into the forge-neutral error.
func wrapWrite(err error) error {
	var na *ghmcp.NotAllowedError
	if errors.As(err, &na) {
		return source.ErrWritesDisabled
	}
	return err
}

// ensure the compile-time contract.
var _ source.Source = (*Source)(nil)

// unused import guard for time (kept for future enrichment fields).
var _ = time.Second
