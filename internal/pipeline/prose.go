package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitinbox/internal/llm"
	llmollama "gitinbox/internal/llm/ollama"
	"gitinbox/internal/scoring"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Settings key and events for the prose step (M4).
const (
	SettingProse        = "prose.settings"
	SettingProseState   = "prose.state"
	EventSummaryUpdated = "summary:updated"
	EventDigestUpdated  = "digest:updated"
)

// ProseSettings are user-editable (Settings view).
type ProseSettings struct {
	SummaryModel         string `json:"summaryModel"`         // Ollama model for summaries; "" = judge/note model
	TopN                 int    `json:"topN"`                 // threads summarised automatically per prose run (by priority)
	SummarizeEveryMin    int    `json:"summarizeEveryMin"`    // throttle between automatic summary runs
	DigestModel          string `json:"digestModel"`          // "" = summary model
	AutoDigest           bool   `json:"autoDigest"`           // generate a digest automatically
	DigestEveryH         int    `json:"digestEveryH"`         // hours between automatic digests
	DigestMaxThreads     int    `json:"digestMaxThreads"`     // threads fed to the digest
	NotifyNeedsMe        bool   `json:"notifyNeedsMe"`        // desktop notification when a thread enters "needs me"
	NotifyImpactMinLevel int    `json:"notifyImpactMinLevel"` // notify for analyses at this level or above (4 = never)
	NotifyMaxPerRun      int    `json:"notifyMaxPerRun"`
}

func DefaultProseSettings() ProseSettings {
	return ProseSettings{TopN: 3, SummarizeEveryMin: 15, AutoDigest: true, DigestEveryH: 24, DigestMaxThreads: 40, NotifyNeedsMe: true, NotifyImpactMinLevel: 3, NotifyMaxPerRun: 5}
}

func (p *Pipeline) ProseSettings(ctx context.Context) (ProseSettings, error) {
	s := DefaultProseSettings()
	if _, err := p.deps.DB.GetSetting(ctx, SettingProse, &s); err != nil {
		return s, err
	}
	if s.SummarizeEveryMin <= 0 {
		s.SummarizeEveryMin = 15
	}
	if s.DigestEveryH <= 0 {
		s.DigestEveryH = 24
	}
	if s.DigestMaxThreads <= 0 {
		s.DigestMaxThreads = 40
	}
	if s.NotifyMaxPerRun <= 0 {
		s.NotifyMaxPerRun = 5
	}
	return s, nil
}

func (p *Pipeline) SetProseSettings(ctx context.Context, s ProseSettings) error {
	return p.deps.DB.SetSetting(ctx, SettingProse, s)
}

// proseState remembers when automatic runs last happened.
type proseState struct {
	LastTopAt    *time.Time `json:"lastTopAt"`
	LastDigestAt *time.Time `json:"lastDigestAt"`
}

func (p *Pipeline) proseStateGet(ctx context.Context) proseState {
	var st proseState
	_, _ = p.deps.DB.GetSetting(ctx, SettingProseState, &st)
	return st
}

// Notification is what the app shows on the desktop (and the CLI prints).
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

// generator resolves the Ollama generator for a model (test override aware).
func (p *Pipeline) generator(ctx context.Context, model string) (llm.Generator, string, error) {
	if p.generatorOverride != nil {
		return p.generatorOverride, "override", nil
	}
	js, _ := p.JudgeSettings(ctx)
	is, _ := p.ImpactSettings(ctx)
	if model == "" {
		model = is.NoteModel
	}
	if model == "" {
		model = js.OllamaJudgeModel
	}
	if model == "" {
		return nil, "", fmt.Errorf("%w: no Ollama model configured (Settings → Triage judge / Prose)", llm.ErrUnavailable)
	}
	return llmollama.New(llmollama.Config{BaseURL: js.OllamaURL, Model: model}), model, nil
}

// --- thread summaries -------------------------------------------------------

const summarySystemPrompt = `You summarise one software notification thread for the developer who received it.
Use only the given state. Be concrete: name the repository, item, people and the ask. Two sentences max for "summary".
"key_points" adds details NOT already in "summary" (decisions, blockers, numbers, files); at most 4, and empty is fine.
"asks_of_me" lists only explicit requests aimed at the user (login given as me.login); leave it empty otherwise.
If a previous_summary is given, write "changed_since_last_read" describing what is new since it; otherwise leave it empty.
Answer ONLY with JSON matching the schema.`

// SummaryView pairs a stored summary with its decoded content and staleness.
type SummaryView struct {
	Summary store.Summary     `json:"summary"`
	Content llm.ThreadSummary `json:"content"`
	Stale   bool              `json:"stale"`
}

// Summary returns the stored summary for a thread (ErrNotFound when none).
func (p *Pipeline) Summary(ctx context.Context, acct store.Account, threadID string) (SummaryView, error) {
	t, err := p.deps.DB.GetThread(ctx, acct.ID, threadID)
	if err != nil {
		return SummaryView{}, err
	}
	s, err := p.deps.DB.GetSummary(ctx, acct.ID, threadID)
	if err != nil {
		return SummaryView{}, err
	}
	v := SummaryView{Summary: s, Stale: s.ThreadVersion != t.Version()}
	_ = json.Unmarshal(s.ContentJSON, &v.Content)
	return v, nil
}

// Summarize generates (or, unless force, reuses) the summary for a thread.
func (p *Pipeline) Summarize(ctx context.Context, acct store.Account, threadID string, force bool) (SummaryView, error) {
	t, err := p.deps.DB.GetThread(ctx, acct.ID, threadID)
	if err != nil {
		return SummaryView{}, err
	}
	version := t.Version()
	var previous *llm.ThreadSummary
	if s, err := p.deps.DB.GetSummary(ctx, acct.ID, threadID); err == nil {
		var c llm.ThreadSummary
		_ = json.Unmarshal(s.ContentJSON, &c)
		if s.ThreadVersion == version && !force {
			return SummaryView{Summary: s, Content: c}, nil
		}
		previous = &c
	}
	// Enrich first when the source can, so the body and latest comment are present.
	if t.EnrichedVersion != version {
		if src, err := p.Source(ctx, acct); err == nil {
			if en, ok := src.(source.Enricher); ok {
				if e, err := en.Enrich(ctx, t); err == nil {
					e.EnrichedVersion = version
					if p.deps.DB.SetThreadEnrichment(ctx, acct.ID, threadID, e) == nil {
						t.Enrichment = e
					}
				}
			}
		}
	}
	settings, _ := p.ProseSettings(ctx)
	gen, model, err := p.generator(ctx, settings.SummaryModel)
	if err != nil {
		return SummaryView{}, err
	}
	js, _ := p.JudgeSettings(ctx)
	state := map[string]any{"thread": BuildTriageState(acct, t, js.ProfileInterests), "html_url": t.HTMLURL}
	if previous != nil {
		state["previous_summary"] = previous
	}
	userJSON, _ := json.MarshalIndent(state, "", "  ")
	res, err := gen.Generate(ctx, summarySystemPrompt, string(userJSON), llm.ThreadSummarySchema())
	if err != nil {
		return SummaryView{}, err
	}
	var content llm.ThreadSummary
	if err := json.Unmarshal(res.JSON, &content); err != nil {
		return SummaryView{}, fmt.Errorf("summary: decode: %w", err)
	}
	usage, _ := json.Marshal(res.Usage)
	s := store.Summary{AccountID: acct.ID, ThreadID: threadID, ThreadVersion: version, Model: firstNonEmpty(res.Model, model), ContentJSON: res.JSON, UsageJSON: usage, LatencyMs: res.Latency.Milliseconds(), CreatedAt: time.Now()}
	if err := p.deps.DB.PutSummary(ctx, s); err != nil {
		return SummaryView{}, err
	}
	p.deps.Emit(EventSummaryUpdated, map[string]any{"accountId": acct.ID, "threadId": threadID})
	return SummaryView{Summary: s, Content: content}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SummarizeTop summarises the n highest-priority unread threads lacking a
// current summary (the lazy policy's "top-N after sync"). n ≤ 0 uses settings.
func (p *Pipeline) SummarizeTop(ctx context.Context, n int) (int, error) {
	settings, err := p.ProseSettings(ctx)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		n = settings.TopN
	}
	if n <= 0 {
		return 0, nil
	}
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{Limit: 500})
	if err != nil {
		return 0, err
	}
	scored, err := p.ScoreThreads(ctx, threads)
	if err != nil {
		return 0, err
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score.Pinned != scored[j].Score.Pinned {
			return scored[i].Score.Pinned
		}
		return scored[i].Score.Priority > scored[j].Score.Priority
	})
	have, err := p.deps.DB.SummariesByKey(ctx, 0)
	if err != nil {
		return 0, err
	}
	accounts := map[int64]store.Account{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			accounts[a.ID] = a
		}
	}
	done := 0
	for _, sc := range scored {
		if done >= n || ctx.Err() != nil {
			break
		}
		t := sc.Thread
		if s, ok := have[store.JudgmentKey(t.AccountID, t.ThreadID)]; ok && s.ThreadVersion == t.Version() {
			continue
		}
		acct, ok := accounts[t.AccountID]
		if !ok {
			continue
		}
		if _, err := p.Summarize(ctx, acct, t.ThreadID, false); err != nil {
			if errors.Is(err, llm.ErrUnavailable) {
				return done, err
			}
			p.deps.Logger.Warn("summarize", "thread", t.ThreadID, "err", err)
			continue
		}
		done++
	}
	return done, nil
}

// --- digest -----------------------------------------------------------------

const digestSystemPrompt = `You write a concise digest of a developer's notification inbox for a period.
Input: scored threads (highest priority first) with judged category, relation to the user, summaries when available, and impact analyses of upstream pull requests.
Group into sections such as "Needs you", "Impact on your projects", "Worth knowing", "Resolved". Keep each item to one line with a concrete "why".
Reference items by the given ref exactly. Suggest at most five actions. Answer ONLY with JSON matching the schema.`

// DigestView pairs a stored digest with its decoded content.
type DigestView struct {
	Digest  store.Digest `json:"digest"`
	Content llm.Digest   `json:"content"`
}

// GenerateDigest writes a digest for threads active since `since`.
func (p *Pipeline) GenerateDigest(ctx context.Context, since time.Time) (DigestView, error) {
	settings, err := p.ProseSettings(ctx)
	if err != nil {
		return DigestView{}, err
	}
	gen, model, err := p.generator(ctx, firstNonEmpty(settings.DigestModel, settings.SummaryModel))
	if err != nil {
		return DigestView{}, err
	}
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{IncludeRead: true, IncludeDone: true, Limit: 2000})
	if err != nil {
		return DigestView{}, err
	}
	var recent []store.Thread
	for _, t := range threads {
		if t.UpdatedAt.After(since) || t.FirstSeenAt.After(since) {
			recent = append(recent, t)
		}
	}
	scored, err := p.ScoreThreads(ctx, recent)
	if err != nil {
		return DigestView{}, err
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score.Priority > scored[j].Score.Priority })
	if len(scored) > settings.DigestMaxThreads {
		scored = scored[:settings.DigestMaxThreads]
	}
	summaries, _ := p.deps.DB.SummariesByKey(ctx, 0)
	accounts := map[int64]store.Account{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			accounts[a.ID] = a
		}
	}
	type item struct {
		Ref        string   `json:"ref"`
		Title      string   `json:"title"`
		Kind       string   `json:"activity_kind"`
		Category   string   `json:"category,omitempty"`
		Bucket     string   `json:"bucket"`
		Percent    int      `json:"priority_percent"`
		Relations  []string `json:"my_relation"`
		Actor      string   `json:"actor,omitempty"`
		Read       bool     `json:"read"`
		Summary    string   `json:"summary,omitempty"`
		AsksOfMe   []string `json:"asks_of_me,omitempty"`
		LatestBody string   `json:"latest_activity,omitempty"`
		Impact     string   `json:"impact_level,omitempty"`
	}
	var items []item
	for _, sc := range scored {
		t := sc.Thread
		it := item{Ref: refFor(accounts[t.AccountID], t), Title: t.Title, Kind: t.ActivityKind, Category: sc.Score.Category, Bucket: sc.Score.Bucket, Percent: sc.Score.Percent,
			Relations: t.RelationTags, Actor: t.Actor, Read: t.IsRead()}
		if s, ok := summaries[store.JudgmentKey(t.AccountID, t.ThreadID)]; ok {
			var c llm.ThreadSummary
			if json.Unmarshal(s.ContentJSON, &c) == nil {
				it.Summary, it.AsksOfMe = c.Summary, c.AsksOfMe
			}
		}
		if it.Summary == "" && t.LatestBody != "" {
			it.LatestBody = source.TrimText(t.LatestBody, 300)
		}
		if sc.Score.ImpactLevel >= 1 {
			it.Impact = []string{"none", "possible", "likely", "certain"}[sc.Score.ImpactLevel]
		}
		items = append(items, it)
	}
	var impacts []map[string]any
	if views, err := p.ListImpact(ctx, store.AnalysisQuery{MinLevel: 2, Any: true, Limit: 30}); err == nil {
		for _, v := range views {
			if v.Analysis.AnalysedAt == nil || v.Analysis.AnalysedAt.Before(since) {
				continue
			}
			m := map[string]any{"ref": fmt.Sprintf("%s#%d", v.Analysis.Repo, v.Analysis.Number), "title": v.Analysis.Title, "state": v.Analysis.State,
				"impact_level": []string{"none", "possible", "likely", "certain"}[v.Analysis.ImpactLevel], "change_kind": v.Analysis.ChangeKind}
			if v.Note != nil {
				m["note"] = v.Note.WhyItMattersForDownstream
			}
			impacts = append(impacts, m)
		}
	}
	js, _ := p.JudgeSettings(ctx)
	now := time.Now()
	user := map[string]any{"period": map[string]any{"from": since.UTC().Format(time.RFC3339), "to": now.UTC().Format(time.RFC3339)},
		"me": map[string]any{"interests": js.ProfileInterests}, "threads": items, "impact_analyses": impacts}
	userJSON, _ := json.MarshalIndent(user, "", "  ")
	res, err := gen.Generate(ctx, digestSystemPrompt, string(userJSON), llm.DigestSchema())
	if err != nil {
		return DigestView{}, err
	}
	var content llm.Digest
	if err := json.Unmarshal(res.JSON, &content); err != nil {
		return DigestView{}, fmt.Errorf("digest: decode: %w", err)
	}
	usage, _ := json.Marshal(res.Usage)
	d := store.Digest{PeriodStart: since, PeriodEnd: now, Model: firstNonEmpty(res.Model, model), ContentJSON: res.JSON, UsageJSON: usage, LatencyMs: res.Latency.Milliseconds(), ThreadCount: len(items), CreatedAt: now}
	id, err := p.deps.DB.PutDigest(ctx, d)
	if err != nil {
		return DigestView{}, err
	}
	d.ID = id
	st := p.proseStateGet(ctx)
	st.LastDigestAt = &now
	_ = p.deps.DB.SetSetting(ctx, SettingProseState, st)
	p.deps.Emit(EventDigestUpdated, nil)
	return DigestView{Digest: d, Content: content}, nil
}

func refFor(acct store.Account, t store.Thread) string {
	sep := "#"
	if t.SubjectType == "MergeRequest" {
		sep = "!"
	}
	if t.SubjectNumber == 0 {
		return t.Repo + " (" + t.SubjectType + ")"
	}
	return fmt.Sprintf("%s%s%d", t.Repo, sep, t.SubjectNumber)
}

// Digests lists stored digests with decoded content.
func (p *Pipeline) Digests(ctx context.Context, limit int) ([]DigestView, error) {
	ds, err := p.deps.DB.ListDigests(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]DigestView, 0, len(ds))
	for _, d := range ds {
		v := DigestView{Digest: d}
		_ = json.Unmarshal(d.ContentJSON, &v.Content)
		out = append(out, v)
	}
	return out, nil
}

// --- notifications ----------------------------------------------------------

// NotifyNew sends desktop notifications for threads that newly need attention
// and for high-impact analyses, de-duplicated by version / head sha.
func (p *Pipeline) NotifyNew(ctx context.Context) (int, error) {
	settings, err := p.ProseSettings(ctx)
	if err != nil {
		return 0, err
	}
	if p.deps.Notify == nil || (!settings.NotifyNeedsMe && settings.NotifyImpactMinLevel > 3) {
		return 0, nil
	}
	sent := 0
	var pending []Notification
	if settings.NotifyNeedsMe {
		threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{Limit: 500})
		if err != nil {
			return 0, err
		}
		scored, err := p.ScoreThreads(ctx, threads)
		if err != nil {
			return 0, err
		}
		for _, sc := range scored {
			if !(sc.Score.Pinned || sc.Score.Bucket == scoring.BucketNeedsMe) || !sc.Score.Judged {
				continue
			}
			t := sc.Thread
			key := fmt.Sprintf("thread:%d:%s:%s", t.AccountID, t.ThreadID, t.Version())
			if ok, err := p.deps.DB.MarkNotified(ctx, key); err != nil || !ok {
				continue
			}
			what := strings.ReplaceAll(sc.Score.Category, "_", " ")
			if sc.Score.Pinned {
				what = "blocking or failing"
			}
			pending = append(pending, Notification{Title: fmt.Sprintf("%s — %s", what, t.Repo), Body: t.Title, URL: t.HTMLURL})
		}
	}
	if settings.NotifyImpactMinLevel <= 3 {
		if list, err := p.deps.DB.ListAnalyses(ctx, store.AnalysisQuery{MinLevel: settings.NotifyImpactMinLevel, Any: true, Limit: 100}); err == nil {
			for _, a := range list {
				key := fmt.Sprintf("impact:%d:%s#%d:%s", a.AccountID, a.Repo, a.Number, a.HeadSHA)
				if ok, err := p.deps.DB.MarkNotified(ctx, key); err != nil || !ok {
					continue
				}
				level := []string{"none", "possible", "likely", "certain"}[a.ImpactLevel]
				pending = append(pending, Notification{Title: fmt.Sprintf("%s impact (%s) — %s#%d", level, a.State, a.Repo, a.Number), Body: a.Title, URL: a.HTMLURL})
			}
		}
	}
	for i, n := range pending {
		if i >= settings.NotifyMaxPerRun {
			p.deps.Notify(Notification{Title: fmt.Sprintf("%d more items need attention", len(pending)-i), Body: "Open GitInbox to see them"})
			sent++
			break
		}
		p.deps.Notify(n)
		sent++
	}
	return sent, nil
}

// proseAfterAnalyze is the scheduler's prose step: notifications, throttled
// top-N summaries, and the automatic digest when due.
func (p *Pipeline) proseAfterAnalyze(ctx context.Context) {
	if _, err := p.NotifyNew(ctx); err != nil && ctx.Err() == nil {
		p.deps.Logger.Warn("notify", "err", err)
	}
	settings, err := p.ProseSettings(ctx)
	if err != nil {
		return
	}
	st := p.proseStateGet(ctx)
	now := time.Now()
	if settings.TopN > 0 && (st.LastTopAt == nil || now.Sub(*st.LastTopAt) >= time.Duration(settings.SummarizeEveryMin)*time.Minute) {
		st.LastTopAt = &now
		_ = p.deps.DB.SetSetting(ctx, SettingProseState, st)
		if n, err := p.SummarizeTop(ctx, settings.TopN); err != nil && ctx.Err() == nil && !errors.Is(err, llm.ErrUnavailable) {
			p.deps.Logger.Warn("summarize top", "err", err)
		} else if n > 0 {
			p.deps.Emit(EventInboxUpdated, nil)
		}
	}
	if settings.AutoDigest && (st.LastDigestAt == nil || now.Sub(*st.LastDigestAt) >= time.Duration(settings.DigestEveryH)*time.Hour) {
		since := now.Add(-time.Duration(settings.DigestEveryH) * time.Hour)
		if st.LastDigestAt != nil {
			since = *st.LastDigestAt
		}
		if _, err := p.GenerateDigest(ctx, since); err != nil && ctx.Err() == nil && !errors.Is(err, llm.ErrUnavailable) {
			p.deps.Logger.Warn("digest", "err", err)
		}
	}
}
