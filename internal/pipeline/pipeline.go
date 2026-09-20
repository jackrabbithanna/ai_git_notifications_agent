// Package pipeline orchestrates accounts, the per-account MCP clients, and the
// sync → classify → filter → store flow. Judgments, scoring and summaries plug
// in here in later milestones.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"ghinbox/internal/classify"
	"ghinbox/internal/filter"
	"ghinbox/internal/ghmcp"
	"ghinbox/internal/secrets"
	"ghinbox/internal/store"
)

// Deps are the collaborators a Pipeline needs.
type Deps struct {
	DB        *store.DB
	Secrets   secrets.Store
	MCPPath   string // github-mcp-server binary
	Logger    *slog.Logger
	Emit      func(name string, data any) // UI event sink; may be nil
	ServerLog io.Writer                   // github-mcp-server stderr sink; may be nil
}

// Pipeline owns one MCP client per account and runs syncs.
type Pipeline struct {
	deps Deps

	mu      sync.Mutex
	clients map[int64]*ghmcp.Client
	syncing map[int64]bool
}

// New wires a Pipeline. Nothing is started until an account is used.
func New(deps Deps) *Pipeline {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Emit == nil {
		deps.Emit = func(string, any) {}
	}
	return &Pipeline{deps: deps, clients: map[int64]*ghmcp.Client{}, syncing: map[int64]bool{}}
}

// Close stops every server process.
func (p *Pipeline) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		_ = c.Close()
		delete(p.clients, id)
	}
}

// Event names emitted to the UI.
const (
	EventInboxUpdated = "inbox:updated"
	EventSyncReport   = "sync:report"
	EventSyncError    = "sync:error"
)

// SyncMineEvery is how often the "mine" searches rerun (search API has its own rate limit).
const SyncMineEvery = 10 * time.Minute

// FullSyncWindow is how far back a full (include-read) sync looks.
const FullSyncWindow = 7 * 24 * time.Hour

// --- clients & accounts -----------------------------------------------------

func (p *Pipeline) newClient(acct store.Account, token string) *ghmcp.Client {
	mode, err := ghmcp.ParseWriteMode(acct.WriteMode)
	if err != nil {
		p.deps.Logger.Warn("invalid write mode; using readonly", "account", acct.Login, "mode", acct.WriteMode)
		mode = ghmcp.WriteModeReadOnly
	}
	host := acct.Host
	if host == "github.com" {
		host = ""
	}
	return ghmcp.New(ghmcp.Config{
		BinaryPath: p.deps.MCPPath,
		Token:      token,
		Host:       host,
		WriteMode:  mode,
		Stderr:     p.deps.ServerLog,
	})
}

// Client returns the (lazily started) MCP client for an account. A client whose
// write mode no longer matches the account is replaced.
func (p *Pipeline) Client(ctx context.Context, acct store.Account) (*ghmcp.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[acct.ID]; ok {
		if string(c.Config().WriteMode) == acct.WriteMode {
			return c, nil
		}
		_ = c.Close()
		delete(p.clients, acct.ID)
	}
	token, err := p.deps.Secrets.Get(secrets.AccountKey(acct.ID))
	if err != nil {
		return nil, fmt.Errorf("token for %s: %w", acct.Login, err)
	}
	c := p.newClient(acct, token)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	p.clients[acct.ID] = c
	return c, nil
}

// AddAccount validates a token via get_me, stores it, and registers the account.
func (p *Pipeline) AddAccount(ctx context.Context, token, host string) (store.Account, error) {
	if host == "" || host == "api.github.com" {
		host = "github.com"
	}
	probe := p.newClient(store.Account{Login: "?", Host: host, WriteMode: string(ghmcp.WriteModeReadOnly)}, token)
	me, err := probe.GetMe(ctx)
	_ = probe.Close()
	if err != nil {
		return store.Account{}, fmt.Errorf("token check failed: %w", err)
	}
	if existing, err := p.deps.DB.FindAccount(ctx, me.Login, host); err == nil {
		// Re-adding an existing account rotates its token.
		if err := p.deps.Secrets.Set(secrets.AccountKey(existing.ID), token); err != nil {
			return store.Account{}, err
		}
		p.dropClient(existing.ID)
		return existing, nil
	}
	acct, err := p.deps.DB.InsertAccount(ctx, me.Login, host)
	if err != nil {
		return store.Account{}, err
	}
	if err := p.deps.Secrets.Set(secrets.AccountKey(acct.ID), token); err != nil {
		_ = p.deps.DB.DeleteAccount(ctx, acct.ID)
		return store.Account{}, fmt.Errorf("store token: %w", err)
	}
	return acct, nil
}

// RemoveAccount deletes the account, its data, and its token.
func (p *Pipeline) RemoveAccount(ctx context.Context, id int64) error {
	p.dropClient(id)
	if err := p.deps.DB.DeleteAccount(ctx, id); err != nil {
		return err
	}
	return p.deps.Secrets.Delete(secrets.AccountKey(id))
}

// SetWriteMode changes the account's GitHub write policy and restarts its server
// so the --read-only flag matches (PLAN.md §4.9).
func (p *Pipeline) SetWriteMode(ctx context.Context, id int64, mode string) error {
	if _, err := ghmcp.ParseWriteMode(mode); err != nil {
		return err
	}
	if err := p.deps.DB.SetAccountWriteMode(ctx, id, mode); err != nil {
		return err
	}
	p.dropClient(id)
	return nil
}

func (p *Pipeline) dropClient(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[id]; ok {
		_ = c.Close()
		delete(p.clients, id)
	}
}

// ClientStats returns per-account MCP counters for Diagnostics.
func (p *Pipeline) ClientStats() map[int64]ghmcp.Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[int64]ghmcp.Stats, len(p.clients))
	for id, c := range p.clients {
		out[id] = c.Stats()
	}
	return out
}

// --- rules ------------------------------------------------------------------

// Rules loads the filter rules (defaults when unset).
func (p *Pipeline) Rules(ctx context.Context) (filter.Rules, error) {
	r := filter.Defaults()
	if _, err := p.deps.DB.GetSetting(ctx, filter.Key, &r); err != nil {
		return r, err
	}
	return r, nil
}

// SetRules stores filter rules. Existing threads are re-evaluated on their next sync.
func (p *Pipeline) SetRules(ctx context.Context, r filter.Rules) error {
	return p.deps.DB.SetSetting(ctx, filter.Key, r)
}

// --- sync -------------------------------------------------------------------

// SyncReport summarises one account sync.
type SyncReport struct {
	AccountID  int64         `json:"accountId"`
	Login      string        `json:"login"`
	Full       bool          `json:"full"`
	Fetched    int           `json:"fetched"`
	New        int           `json:"new"`
	Updated    int           `json:"updated"`
	Noise      int           `json:"noise"`
	MineSynced bool          `json:"mineSynced"`
	MineItems  int           `json:"mineItems"`
	Duration   time.Duration `json:"duration"`
	Error      string        `json:"error"`
}

// SyncAll syncs every account sequentially and returns one report each.
func (p *Pipeline) SyncAll(ctx context.Context, full bool) ([]SyncReport, error) {
	accts, err := p.deps.DB.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	reports := make([]SyncReport, 0, len(accts))
	for _, a := range accts {
		rep, err := p.SyncAccount(ctx, a, full)
		if err != nil && rep.Error == "" {
			rep.Error = err.Error()
		}
		reports = append(reports, rep)
		if ctx.Err() != nil {
			break
		}
	}
	return reports, ctx.Err()
}

// SyncAccount pulls notifications (and, when due, the "mine" searches) for one account.
// full=true re-reads the last FullSyncWindow including read threads, reconciling
// unread flags changed elsewhere; the first sync is always full.
func (p *Pipeline) SyncAccount(ctx context.Context, acct store.Account, full bool) (SyncReport, error) {
	start := time.Now()
	rep := SyncReport{AccountID: acct.ID, Login: acct.Login}
	if !p.beginSync(acct.ID) {
		rep.Error = "sync already running"
		return rep, errors.New(rep.Error)
	}
	defer p.endSync(acct.ID)

	db := p.deps.DB
	state, err := db.GetSyncState(ctx, acct.ID)
	if err != nil {
		return rep, err
	}
	fail := func(err error) (SyncReport, error) {
		rep.Error = err.Error()
		rep.Duration = time.Since(start)
		state.LastError = err.Error()
		_ = db.PutSyncState(ctx, state)
		p.deps.Emit(EventSyncError, rep)
		return rep, err
	}

	client, err := p.Client(ctx, acct)
	if err != nil {
		return fail(err)
	}
	rules, err := p.Rules(ctx)
	if err != nil {
		return fail(err)
	}

	if state.LastFullAt == nil {
		full = true
	}
	rep.Full = full
	opts := ghmcp.ListNotificationsOpts{PerPage: 50}
	if full {
		opts.Filter = "include_read_notifications"
		opts.Since = start.Add(-FullSyncWindow)
	} else {
		opts.Filter = "default"
		if state.LastSince != nil {
			opts.Since = state.LastSince.Add(-10 * time.Minute)
		}
	}

	host := ""
	if acct.Host != "github.com" {
		host = acct.Host
	}
	for page := 1; page <= 40; page++ {
		opts.Page = page
		batch, err := client.ListNotifications(ctx, opts)
		if err != nil {
			return fail(err)
		}
		for _, n := range batch {
			rep.Fetched++
			isNew, isNoise, err := p.storeNotification(ctx, acct, host, rules, n)
			if err != nil {
				return fail(err)
			}
			if isNew {
				rep.New++
			} else {
				rep.Updated++
			}
			if isNoise {
				rep.Noise++
			}
		}
		if len(batch) < opts.PerPage {
			break
		}
	}

	now := time.Now()
	state.LastSince = &now
	state.LastSyncAt = &now
	if full {
		state.LastFullAt = &now
	}
	state.LastError = ""

	if state.LastMineAt == nil || now.Sub(*state.LastMineAt) >= SyncMineEvery {
		n, run, err := p.syncMine(ctx, client, acct, state.MineRun+1)
		if err != nil {
			// Mine is secondary; report but keep the notification sync result.
			p.deps.Logger.Warn("mine sync failed", "account", acct.Login, "err", err)
			rep.Error = "mine: " + err.Error()
		} else {
			rep.MineSynced = true
			rep.MineItems = n
			state.MineRun = run
			state.LastMineAt = &now
		}
	}
	if err := db.PutSyncState(ctx, state); err != nil {
		return fail(err)
	}
	rep.Duration = time.Since(start)
	p.deps.Emit(EventSyncReport, rep)
	p.deps.Emit(EventInboxUpdated, nil)
	return rep, nil
}

func (p *Pipeline) beginSync(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.syncing[id] {
		return false
	}
	p.syncing[id] = true
	return true
}

func (p *Pipeline) endSync(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.syncing, id)
}

// storeNotification classifies, filters and upserts one thread. Returns whether it
// was new to the store and whether it was judged noise.
func (p *Pipeline) storeNotification(ctx context.Context, acct store.Account, host string, rules filter.Rules, n ghmcp.Notification) (isNew, isNoise bool, err error) {
	res := classify.Classify(n)
	v := filter.Apply(rules, filter.Input{Repo: n.Repo, Title: n.Title, Reason: n.Reason, Kind: res.Kind})
	verdict := "keep"
	if !v.Keep {
		verdict = "noise"
	}
	if _, err := p.deps.DB.GetThread(ctx, acct.ID, n.ID); errors.Is(err, store.ErrNotFound) {
		isNew = true
	} else if err != nil {
		return false, false, err
	}
	t := store.Thread{
		AccountID:        acct.ID,
		ThreadID:         n.ID,
		Repo:             n.Repo,
		SubjectType:      n.SubjectType,
		SubjectURL:       n.SubjectURL,
		SubjectNumber:    n.SubjectNumber(),
		HTMLURL:          classify.HTMLURL(host, n),
		Title:            n.Title,
		Reason:           n.Reason,
		Unread:           n.Unread,
		UpdatedAt:        n.UpdatedAt,
		LastReadAt:       n.LastReadAt,
		LatestCommentURL: n.LatestCommentURL,
		ActivityKind:     string(res.Kind),
		RelationTags:     res.RelationTags,
		FilterVerdict:    verdict,
		FilterReason:     v.Reason,
	}
	return isNew, !v.Keep, p.deps.DB.UpsertThread(ctx, t)
}

// mineQueries are the "mine" searches per account; review requests only exist for PRs.
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

// SyncMine refreshes the account's assigned/mentioned/review-requested/authored items now.
func (p *Pipeline) SyncMine(ctx context.Context, acct store.Account) (int, error) {
	client, err := p.Client(ctx, acct)
	if err != nil {
		return 0, err
	}
	state, err := p.deps.DB.GetSyncState(ctx, acct.ID)
	if err != nil {
		return 0, err
	}
	n, run, err := p.syncMine(ctx, client, acct, state.MineRun+1)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	state.MineRun = run
	state.LastMineAt = &now
	if err := p.deps.DB.PutSyncState(ctx, state); err != nil {
		return n, err
	}
	p.deps.Emit(EventInboxUpdated, nil)
	return n, nil
}

func (p *Pipeline) syncMine(ctx context.Context, client *ghmcp.Client, acct store.Account, run int64) (int, int64, error) {
	type key struct {
		repo string
		num  int
	}
	merged := map[key]*store.Item{}
	add := func(rel string, items []ghmcp.Item) {
		for _, it := range items {
			k := key{it.Repo, it.Number}
			cur, ok := merged[k]
			if !ok {
				kind := "issue"
				if it.IsPR {
					kind = "pr"
				}
				cur = &store.Item{AccountID: acct.ID, Repo: it.Repo, Number: it.Number, Kind: kind, Title: it.Title, State: it.State,
					HTMLURL: it.HTMLURL, Author: it.Author, Assignees: it.Assignees, Labels: it.Labels, Draft: it.Draft, Comments: it.Comments,
					CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}
				merged[k] = cur
			}
			cur.Relations = appendUnique(cur.Relations, rel)
		}
	}
	for _, q := range mineQueries {
		if q.Issues {
			res, err := client.SearchIssues(ctx, q.Query, 1, 100)
			if err != nil {
				return 0, run, fmt.Errorf("%s (issues): %w", q.Relation, err)
			}
			add(q.Relation, res.Items)
		}
		if q.PRs {
			res, err := client.SearchPullRequests(ctx, q.Query, 1, 100)
			if err != nil {
				return 0, run, fmt.Errorf("%s (prs): %w", q.Relation, err)
			}
			add(q.Relation, res.Items)
		}
	}
	for _, it := range merged {
		if err := p.deps.DB.UpsertItem(ctx, *it, run); err != nil {
			return 0, run, err
		}
	}
	if _, err := p.deps.DB.PruneItems(ctx, acct.ID, run); err != nil {
		return 0, run, err
	}
	return len(merged), run, nil
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// --- local triage actions (mirrored to GitHub only when the write mode allows) --

// MirrorResult tells the caller whether GitHub was updated too.
type MirrorResult struct {
	Mirrored bool   `json:"mirrored"`
	Warning  string `json:"warning"`
}

func (p *Pipeline) MarkRead(ctx context.Context, acct store.Account, threadID string) (MirrorResult, error) {
	if err := p.deps.DB.MarkThreadRead(ctx, acct.ID, threadID); err != nil {
		return MirrorResult{}, err
	}
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.mirror(ctx, acct, func(c *ghmcp.Client) error { return c.DismissNotification(ctx, threadID, "read") }), nil
}

func (p *Pipeline) MarkDone(ctx context.Context, acct store.Account, threadID string) (MirrorResult, error) {
	if err := p.deps.DB.MarkThreadDone(ctx, acct.ID, threadID); err != nil {
		return MirrorResult{}, err
	}
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.mirror(ctx, acct, func(c *ghmcp.Client) error { return c.DismissNotification(ctx, threadID, "done") }), nil
}

func (p *Pipeline) UndoDone(ctx context.Context, acct store.Account, threadID string) error {
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.deps.DB.UndoThreadDone(ctx, acct.ID, threadID)
}

func (p *Pipeline) Snooze(ctx context.Context, acct store.Account, threadID string, until time.Time) error {
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.deps.DB.SnoozeThread(ctx, acct.ID, threadID, until)
}

func (p *Pipeline) Unsnooze(ctx context.Context, acct store.Account, threadID string) error {
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.deps.DB.UnsnoozeThread(ctx, acct.ID, threadID)
}

// Unsubscribe ignores the thread on GitHub (write) and marks it done locally.
func (p *Pipeline) Unsubscribe(ctx context.Context, acct store.Account, threadID string) (MirrorResult, error) {
	if err := p.deps.DB.MarkThreadDone(ctx, acct.ID, threadID); err != nil {
		return MirrorResult{}, err
	}
	defer p.deps.Emit(EventInboxUpdated, nil)
	res := p.mirror(ctx, acct, func(c *ghmcp.Client) error { return c.ManageNotificationSubscription(ctx, threadID, "ignore") })
	if !res.Mirrored && res.Warning == "" {
		res.Warning = "unsubscribe needs write mode 'notifications'; thread hidden locally only"
	}
	return res, nil
}

// mirror runs a GitHub write when the account allows it; failures become warnings.
func (p *Pipeline) mirror(ctx context.Context, acct store.Account, do func(*ghmcp.Client) error) MirrorResult {
	if acct.WriteMode != string(ghmcp.WriteModeNotifications) {
		return MirrorResult{}
	}
	client, err := p.Client(ctx, acct)
	if err != nil {
		return MirrorResult{Warning: "GitHub not updated: " + err.Error()}
	}
	if err := do(client); err != nil {
		p.deps.Logger.Warn("mirror to GitHub failed", "account", acct.Login, "err", err)
		return MirrorResult{Warning: "GitHub not updated: " + err.Error()}
	}
	return MirrorResult{Mirrored: true}
}

// --- scheduler --------------------------------------------------------------

// RunScheduler syncs all accounts every interval until ctx is cancelled.
func (p *Pipeline) RunScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 3 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := p.SyncAll(ctx, false); err != nil && ctx.Err() == nil {
				p.deps.Logger.Warn("scheduled sync", "err", err)
			}
		}
	}
}
