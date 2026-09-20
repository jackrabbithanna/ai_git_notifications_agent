package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitinbox/internal/ghmcp"
	"gitinbox/internal/scoring"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Settings key for the agent sidecar (M6).
const SettingAgent = "agent.settings"

// AgentSettings configure the pi sidecar (Settings view / CLI).
type AgentSettings struct {
	Enabled   bool   `json:"enabled"`   // show the Agent view and allow starting the sidecar
	AutoStart bool   `json:"autoStart"` // start the sidecar when the app starts
	PiPath    string `json:"piPath"`    // explicit pi binary; "" = auto-detect (PATH, then the repo's agent/node_modules)
	Model     string `json:"model"`     // Ollama model id; "" = summary → note → judge model
	Thinking  string `json:"thinking"`  // off | low | medium | high
}

func DefaultAgentSettings() AgentSettings {
	return AgentSettings{Enabled: true, Thinking: "off"}
}

func (p *Pipeline) AgentSettings(ctx context.Context) (AgentSettings, error) {
	s := DefaultAgentSettings()
	if _, err := p.deps.DB.GetSetting(ctx, SettingAgent, &s); err != nil {
		return s, err
	}
	if s.Thinking == "" {
		s.Thinking = "off"
	}
	return s, nil
}

func (p *Pipeline) SetAgentSettings(ctx context.Context, s AgentSettings) error {
	return p.deps.DB.SetSetting(ctx, SettingAgent, s)
}

// AgentModel resolves the Ollama base URL and the model the agent should use:
// the agent's own setting, else the summary model, else the note model, else
// the judge model. An empty model means nothing is configured.
func (p *Pipeline) AgentModel(ctx context.Context) (baseURL, model string) {
	js, _ := p.JudgeSettings(ctx)
	as, _ := p.AgentSettings(ctx)
	ps, _ := p.ProseSettings(ctx)
	is, _ := p.ImpactSettings(ctx)
	for _, m := range []string{as.Model, ps.SummaryModel, is.NoteModel, js.OllamaJudgeModel} {
		if m != "" {
			return js.OllamaURL, m
		}
	}
	return js.OllamaURL, ""
}

// ResolveAccount accepts an id, "login@host", a host, or an unambiguous login.
func (p *Pipeline) ResolveAccount(ctx context.Context, ref string) (store.Account, error) {
	accts, err := p.deps.DB.ListAccounts(ctx)
	if err != nil {
		return store.Account{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(accts) == 1 {
			return accts[0], nil
		}
		return store.Account{}, errors.New("account required (id or login@host)")
	}
	var matches []store.Account
	for _, a := range accts {
		switch {
		case strconv.FormatInt(a.ID, 10) == ref,
			strings.EqualFold(a.Login+"@"+a.Host, ref),
			strings.EqualFold(a.Host, ref),
			strings.EqualFold(a.Login, ref):
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return store.Account{}, fmt.Errorf("unknown account %q", ref)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, a := range matches {
		names = append(names, fmt.Sprintf("%s@%s (id %d)", a.Login, a.Host, a.ID))
	}
	return store.Account{}, fmt.Errorf("account %q is ambiguous: %s", ref, strings.Join(names, ", "))
}

// --- thread lookups for the local API -------------------------------------

// TopThreads returns scored threads ordered by priority (pinned first). bucket
// "needs_me" keeps only that bucket; "" keeps everything unread and kept.
func (p *Pipeline) TopThreads(ctx context.Context, accountID int64, bucket string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 15
	}
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: accountID, Limit: 2000})
	if err != nil {
		return nil, err
	}
	scored, err := p.ScoreThreads(ctx, threads)
	if err != nil {
		return nil, err
	}
	if bucket != "" {
		kept := scored[:0]
		for _, sc := range scored {
			if sc.Score.Bucket == bucket || (bucket == scoring.BucketNeedsMe && sc.Score.Pinned) {
				kept = append(kept, sc)
			}
		}
		scored = kept
	}
	sortByPriority(scored)
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// SearchThreads matches every whitespace-separated token of query against the
// repository, title, number and thread id (case-insensitive), newest activity
// first. Read and done threads are included when includeRead is set.
func (p *Pipeline) SearchThreads(ctx context.Context, accountID int64, query string, includeRead bool, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 20
	}
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: accountID, IncludeRead: includeRead, IncludeDone: includeRead, Snoozed: includeRead, Limit: 5000})
	if err != nil {
		return nil, err
	}
	tokens := strings.Fields(strings.ToLower(query))
	var matched []store.Thread
	for _, t := range threads {
		hay := strings.ToLower(t.Repo + " " + t.Title + " #" + strconv.Itoa(t.SubjectNumber) + " !" + strconv.Itoa(t.SubjectNumber) + " " + t.ThreadID + " " + t.ActivityKind + " " + strings.Join(t.RelationTags, " ") + " " + t.Actor)
		ok := true
		for _, tok := range tokens {
			if !strings.Contains(hay, tok) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, t)
		}
	}
	if len(matched) > limit*4 {
		matched = matched[:limit*4]
	}
	scored, err := p.ScoreThreads(ctx, matched)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Thread.UpdatedAt.After(scored[j].Thread.UpdatedAt) })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored, nil
}

// FindThread returns the thread for an item reference (repo + number) on an account.
func (p *Pipeline) FindThread(ctx context.Context, accountID int64, repo string, number int) (store.Thread, error) {
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: accountID, IncludeRead: true, IncludeDone: true, IncludeNoise: true, Snoozed: true, Limit: 10000})
	if err != nil {
		return store.Thread{}, err
	}
	for _, t := range threads {
		if t.SubjectNumber == number && strings.EqualFold(t.Repo, repo) {
			return t, nil
		}
	}
	return store.Thread{}, store.ErrNotFound
}

func sortByPriority(scored []Scored) {
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score.Pinned != scored[j].Score.Pinned {
			return scored[i].Score.Pinned
		}
		if scored[i].Score.Priority != scored[j].Score.Priority {
			return scored[i].Score.Priority > scored[j].Score.Priority
		}
		return scored[i].Thread.UpdatedAt.After(scored[j].Thread.UpdatedAt)
	})
}

// --- forge reads for deep-dives --------------------------------------------

// Comments lists an item's discussion through the account's source.
func (p *Pipeline) Comments(ctx context.Context, acct store.Account, repo string, number int, subjectType string, limit int) ([]source.Comment, error) {
	src, err := p.Source(ctx, acct)
	if err != nil {
		return nil, err
	}
	d, ok := src.(source.Discusser)
	if !ok {
		return nil, fmt.Errorf("account %s cannot list comments", acct.Login)
	}
	return d.Comments(ctx, repo, number, subjectType, limit)
}

// Changes fetches a pull/merge request's metadata and changed files.
func (p *Pipeline) Changes(ctx context.Context, acct store.Account, repo string, number int) (source.ChangeSet, error) {
	src, err := p.Source(ctx, acct)
	if err != nil {
		return source.ChangeSet{}, err
	}
	c, ok := src.(source.Changer)
	if !ok {
		return source.ChangeSet{}, fmt.Errorf("account %s cannot fetch changes", acct.Login)
	}
	return c.Changes(ctx, repo, number)
}

// ErrNotReadTool is returned by GitHubRead for anything outside the read allowlist.
var ErrNotReadTool = errors.New("tool is not on the read-only allowlist")

// GitHubRead calls one github-mcp-server read tool for a GitHub account. The
// allowlist is the read set regardless of the account's write mode, so an
// agent can never reach a write tool through this path.
func (p *Pipeline) GitHubRead(ctx context.Context, acct store.Account, tool string, args map[string]any) (string, error) {
	if !ghmcp.AllowedTools(ghmcp.WriteModeReadOnly)[tool] {
		return "", fmt.Errorf("%w: %s", ErrNotReadTool, tool)
	}
	c, err := p.MCPClient(ctx, acct)
	if err != nil {
		return "", err
	}
	return c.CallRaw(ctx, tool, args)
}

// ReadTools lists the tools GitHubRead accepts.
func ReadTools() []string {
	m := ghmcp.AllowedTools(ghmcp.WriteModeReadOnly)
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// EventDraftSaved fires when the agent stores a draft reply.
const EventDraftSaved = "draft:saved"

// EmitDraftSaved notifies the UI that a draft exists for a thread.
func (p *Pipeline) EmitDraftSaved(accountID int64, threadID string) {
	p.deps.Emit(EventDraftSaved, map[string]any{"accountId": accountID, "threadId": threadID})
}
