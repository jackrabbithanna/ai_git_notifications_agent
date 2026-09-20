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
	"strings"
	"sync"
	"time"

	"gitinbox/internal/classify"
	"gitinbox/internal/filter"
	"gitinbox/internal/ghmcp"
	"gitinbox/internal/judge"
	"gitinbox/internal/llm"
	"gitinbox/internal/secrets"
	"gitinbox/internal/source"
	githubsrc "gitinbox/internal/source/github"
	"gitinbox/internal/store"
)

// Deps are the collaborators a Pipeline needs.
type Deps struct {
	DB        *store.DB
	Secrets   secrets.Store
	MCPPath   string // github-mcp-server binary
	Logger    *slog.Logger
	Emit      func(name string, data any) // UI event sink; may be nil
	ServerLog io.Writer                   // github-mcp-server stderr sink; may be nil
	Notify    func(n Notification)        // desktop notification sink; nil disables notifications
}

// Pipeline owns one MCP client per account and runs syncs.
type Pipeline struct {
	deps Deps

	mu      sync.Mutex
	sources map[int64]source.Source
	syncing map[int64]bool

	// newGitLab builds a GitLab source; set by the gitlab package wiring so this
	// package does not import client-go directly (see SetGitLabFactory).
	newGitLab func(acct store.Account, token string) (source.Source, error)

	// judgeOverride replaces the configured provider (tests, dry runs).
	judgeOverride judge.Judge
	// generatorOverride replaces the note generator (tests).
	generatorOverride llm.Generator
}

// GitLabFactory builds a GitLab Source for an account (registered by main/CLI).
type GitLabFactory func(acct store.Account, token string) (source.Source, error)

// SetGitLabFactory installs the GitLab source constructor.
func (p *Pipeline) SetGitLabFactory(f GitLabFactory) { p.newGitLab = f }

// New wires a Pipeline. Nothing is started until an account is used.
func New(deps Deps) *Pipeline {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Emit == nil {
		deps.Emit = func(string, any) {}
	}
	return &Pipeline{deps: deps, sources: map[int64]source.Source{}, syncing: map[int64]bool{}}
}

// Close stops every server process.
func (p *Pipeline) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range p.sources {
		_ = s.Close()
		delete(p.sources, id)
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

func (p *Pipeline) newSource(acct store.Account, token string) (source.Source, error) {
	mode, err := ghmcp.ParseWriteMode(acct.WriteMode)
	if err != nil {
		p.deps.Logger.Warn("invalid write mode; using readonly", "account", acct.Login, "mode", acct.WriteMode)
		mode = ghmcp.WriteModeReadOnly
	}
	switch acct.Forge {
	case "", store.ForgeGitHub:
		host := acct.Host
		if host == "github.com" {
			host = ""
		}
		return githubsrc.New(githubsrc.Config{
			BinaryPath: p.deps.MCPPath,
			Token:      token,
			Host:       host,
			WriteMode:  mode,
			Stderr:     p.deps.ServerLog,
		}), nil
	case store.ForgeGitLab:
		if p.newGitLab == nil {
			return nil, errors.New("gitlab source not available in this build")
		}
		return p.newGitLab(acct, token)
	}
	return nil, fmt.Errorf("unsupported forge %q", acct.Forge)
}

// Source returns the (lazily started) forge connection for an account. A source
// whose write mode no longer matches the account is replaced.
func (p *Pipeline) Source(ctx context.Context, acct store.Account) (source.Source, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sources[acct.ID]; ok {
		if m, ok := s.(interface{ WriteMode() string }); !ok || m.WriteMode() == acct.WriteMode {
			return s, nil
		}
		_ = s.Close()
		delete(p.sources, acct.ID)
	}
	token, err := p.deps.Secrets.Get(secrets.AccountKey(acct.ID))
	if err != nil {
		return nil, fmt.Errorf("token for %s: %w", acct.Login, err)
	}
	s, err := p.newSource(acct, token)
	if err != nil {
		return nil, err
	}
	p.sources[acct.ID] = s
	return s, nil
}

// MCPClient returns the GitHub MCP client behind an account (Diagnostics), or an
// error for non-GitHub accounts.
func (p *Pipeline) MCPClient(ctx context.Context, acct store.Account) (*ghmcp.Client, error) {
	s, err := p.Source(ctx, acct)
	if err != nil {
		return nil, err
	}
	if m, ok := s.(interface{ MCP() *ghmcp.Client }); ok {
		return m.MCP(), nil
	}
	return nil, fmt.Errorf("account %s (%s) has no MCP server", acct.Login, acct.Forge)
}

// AddAccount validates a token against the forge, stores it, and registers the account.
func (p *Pipeline) AddAccount(ctx context.Context, forge, token, host string) (store.Account, error) {
	if forge == "" {
		forge = store.ForgeGitHub
	}
	switch host {
	case "", "api.github.com":
		if forge == store.ForgeGitLab {
			host = "gitlab.com"
		} else {
			host = "github.com"
		}
	}
	probe, err := p.newSource(store.Account{Forge: forge, Login: "?", Host: host, WriteMode: string(ghmcp.WriteModeReadOnly)}, token)
	if err != nil {
		return store.Account{}, err
	}
	login, err := probe.Login(ctx)
	scopes := ""
	if err == nil {
		if sr, ok := probe.(source.ScopeReporter); ok {
			if sc, serr := sr.TokenScopes(ctx); serr == nil {
				scopes = sc
			} else {
				p.deps.Logger.Info("token scopes unavailable", "host", host, "err", serr)
			}
		}
	}
	_ = probe.Close()
	if err != nil {
		return store.Account{}, fmt.Errorf("token check failed: %w", err)
	}
	if existing, err := p.deps.DB.FindAccount(ctx, login, host); err == nil {
		// Re-adding an existing account rotates its token.
		if err := p.deps.Secrets.Set(secrets.AccountKey(existing.ID), token); err != nil {
			return store.Account{}, err
		}
		_ = p.deps.DB.SetAccountTokenScopes(ctx, existing.ID, scopes)
		p.dropSource(existing.ID)
		existing.TokenScopes = scopes
		return existing, nil
	}
	acct, err := p.deps.DB.InsertAccount(ctx, forge, login, host)
	if err != nil {
		return store.Account{}, err
	}
	if err := p.deps.Secrets.Set(secrets.AccountKey(acct.ID), token); err != nil {
		_ = p.deps.DB.DeleteAccount(ctx, acct.ID)
		return store.Account{}, fmt.Errorf("store token: %w", err)
	}
	if scopes != "" {
		_ = p.deps.DB.SetAccountTokenScopes(ctx, acct.ID, scopes)
		acct.TokenScopes = scopes
	}
	return acct, nil
}

// RemoveAccount deletes the account, its data, and its token.
func (p *Pipeline) RemoveAccount(ctx context.Context, id int64) error {
	p.dropSource(id)
	if err := p.deps.DB.DeleteAccount(ctx, id); err != nil {
		return err
	}
	return p.deps.Secrets.Delete(secrets.AccountKey(id))
}

// SetWriteMode changes the account's write policy and restarts its source so
// the server flag / in-code guard matches (PLAN.md §4.9).
func (p *Pipeline) SetWriteMode(ctx context.Context, id int64, mode string) error {
	if _, err := ghmcp.ParseWriteMode(mode); err != nil {
		return err
	}
	if err := p.deps.DB.SetAccountWriteMode(ctx, id, mode); err != nil {
		return err
	}
	p.dropSource(id)
	return nil
}

func (p *Pipeline) dropSource(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.sources[id]; ok {
		_ = s.Close()
		delete(p.sources, id)
	}
}

// ClientStats returns per-account MCP counters (GitHub accounts) for Diagnostics.
func (p *Pipeline) ClientStats() map[int64]ghmcp.Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[int64]ghmcp.Stats, len(p.sources))
	for id, s := range p.sources {
		if m, ok := s.(interface{ MCP() *ghmcp.Client }); ok {
			out[id] = m.MCP().Stats()
		}
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
	Warnings   []string      `json:"warnings"`
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

	src, err := p.Source(ctx, acct)
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
	opts := source.Opts{AccountID: acct.ID, Host: acct.Host, Full: full}
	if full {
		opts.Since = start.Add(-FullSyncWindow)
	} else if state.LastSince != nil {
		opts.Since = state.LastSince.Add(-10 * time.Minute)
	}

	res, err := src.Notifications(ctx, opts)
	if err != nil {
		return fail(err)
	}
	rep.Warnings = append(rep.Warnings, res.Warnings...)
	for _, t := range res.Threads {
		rep.Fetched++
		isNew, isNoise, err := p.storeThread(ctx, rules, t)
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
	if res.ReadExcept != nil {
		if _, err := p.deps.DB.MarkThreadsReadExcept(ctx, acct.ID, res.ReadExcept.Prefix, res.ReadExcept.IDs); err != nil {
			return fail(err)
		}
	}

	if w, ok := src.(source.Watcher); ok {
		watched, err := p.deps.DB.ListWatched(ctx, acct.ID)
		if err != nil {
			return fail(err)
		}
		if len(watched) > 0 {
			threads, updates, warns, err := w.SyncWatched(ctx, watched, acct.ID)
			if err != nil {
				return fail(err)
			}
			rep.Warnings = append(rep.Warnings, warns...)
			for _, t := range threads {
				rep.Fetched++
				isNew, isNoise, err := p.storeThread(ctx, rules, t)
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
			for _, u := range updates {
				if err := p.deps.DB.UpdateWatched(ctx, acct.ID, u.Path, u.ProjectID, u.LastEventAt, u.Err); err != nil {
					return fail(err)
				}
			}
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
		n, run, err := p.syncMine(ctx, src, acct, state.MineRun+1)
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

// storeThread filters and upserts one classified thread. Returns whether it was
// new to the store and whether it was judged noise.
func (p *Pipeline) storeThread(ctx context.Context, rules filter.Rules, t store.Thread) (isNew, isNoise bool, err error) {
	v := filter.Apply(rules, filter.Input{Repo: t.Repo, Title: t.Title, Reason: t.Reason, Kind: classifyKind(t.ActivityKind)})
	t.FilterVerdict = "keep"
	if !v.Keep {
		t.FilterVerdict = "noise"
	}
	t.FilterReason = v.Reason
	if _, err := p.deps.DB.GetThread(ctx, t.AccountID, t.ThreadID); errors.Is(err, store.ErrNotFound) {
		isNew = true
	} else if err != nil {
		return false, false, err
	}
	return isNew, !v.Keep, p.deps.DB.UpsertThread(ctx, t)
}

// SyncMine refreshes the account's assigned/mentioned/review-requested/authored items now.
func (p *Pipeline) SyncMine(ctx context.Context, acct store.Account) (int, error) {
	src, err := p.Source(ctx, acct)
	if err != nil {
		return 0, err
	}
	state, err := p.deps.DB.GetSyncState(ctx, acct.ID)
	if err != nil {
		return 0, err
	}
	n, run, err := p.syncMine(ctx, src, acct, state.MineRun+1)
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

func (p *Pipeline) syncMine(ctx context.Context, src source.Source, acct store.Account, run int64) (int, int64, error) {
	items, err := src.Mine(ctx)
	if err != nil {
		return 0, run, err
	}
	for _, it := range items {
		it.AccountID = acct.ID
		if err := p.deps.DB.UpsertItem(ctx, it, run); err != nil {
			return 0, run, err
		}
	}
	if _, err := p.deps.DB.PruneItems(ctx, acct.ID, run); err != nil {
		return 0, run, err
	}
	return len(items), run, nil
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
	return p.mirror(ctx, acct, func(s source.Source) error { return s.MarkRead(ctx, threadID) }), nil
}

func (p *Pipeline) MarkDone(ctx context.Context, acct store.Account, threadID string) (MirrorResult, error) {
	if err := p.deps.DB.MarkThreadDone(ctx, acct.ID, threadID); err != nil {
		return MirrorResult{}, err
	}
	defer p.deps.Emit(EventInboxUpdated, nil)
	return p.mirror(ctx, acct, func(s source.Source) error { return s.MarkDone(ctx, threadID) }), nil
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
	res := p.mirror(ctx, acct, func(s source.Source) error { return s.Unsubscribe(ctx, threadID) })
	if !res.Mirrored && res.Warning == "" {
		res.Warning = "unsubscribe needs write mode 'notifications'; thread hidden locally only"
	}
	return res, nil
}

// mirror runs a forge write when the account allows it; failures become warnings.
// Actions the forge cannot represent (source.ErrNotMirrorable) stay silent.
func (p *Pipeline) mirror(ctx context.Context, acct store.Account, do func(source.Source) error) MirrorResult {
	if acct.WriteMode != string(ghmcp.WriteModeNotifications) {
		return MirrorResult{}
	}
	src, err := p.Source(ctx, acct)
	if err != nil {
		return MirrorResult{Warning: "not mirrored to " + acct.Forge + ": " + err.Error()}
	}
	if err := do(src); err != nil {
		if errors.Is(err, source.ErrNotMirrorable) {
			return MirrorResult{}
		}
		p.deps.Logger.Warn("mirror failed", "account", acct.Login, "forge", acct.Forge, "err", err)
		return MirrorResult{Warning: "not mirrored to " + acct.Forge + ": " + err.Error()}
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
			p.judgeAfterSync(ctx)
			p.analyzeAfterJudge(ctx)
			p.proseAfterAnalyze(ctx)
		}
	}
}

func classifyKind(k string) classify.Kind { return classify.Kind(k) }

// judgeAfterSync runs the judge step when a provider is configured; a missing
// provider is not an error worth logging on every tick.
func (p *Pipeline) judgeAfterSync(ctx context.Context) {
	reports, err := p.JudgeAll(ctx)
	if err != nil && ctx.Err() == nil {
		p.deps.Logger.Warn("scheduled judge", "err", err)
	}
	for _, r := range reports {
		if r.Error != "" && !errors.Is(errors.New(r.Error), ErrJudgeOff) && !strings.Contains(r.Error, ErrJudgeOff.Error()) {
			p.deps.Logger.Warn("judge run", "account", r.Login, "err", r.Error)
		}
	}
}

// SyncAndJudge is the scheduler's unit of work, also used at startup.
func (p *Pipeline) SyncAndJudge(ctx context.Context, full bool) ([]SyncReport, []JudgeReport, error) {
	syncs, err := p.SyncAll(ctx, full)
	if err != nil {
		return syncs, nil, err
	}
	judges, _ := p.JudgeAll(ctx)
	p.analyzeAfterJudge(ctx)
	p.proseAfterAnalyze(ctx)
	return syncs, judges, nil
}

// analyzeAfterJudge runs the impact step for every account when enabled.
func (p *Pipeline) analyzeAfterJudge(ctx context.Context) {
	settings, err := p.ImpactSettings(ctx)
	if err != nil || !settings.AutoAnalyze {
		return
	}
	accts, err := p.deps.DB.ListAccounts(ctx)
	if err != nil {
		return
	}
	for _, a := range accts {
		rep, err := p.AnalyzePending(ctx, a, 0)
		if err != nil && ctx.Err() == nil && !errors.Is(err, ErrJudgeOff) {
			p.deps.Logger.Warn("impact analysis", "account", a.Login, "err", err)
		}
		for _, e := range rep.Errors {
			p.deps.Logger.Warn("impact analysis", "account", a.Login, "detail", e)
		}
		if errors.Is(err, ErrJudgeOff) || errors.Is(err, judge.ErrUnauthorized) {
			break
		}
	}
}
