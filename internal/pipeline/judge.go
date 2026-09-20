package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ghinbox/internal/judge"
	"ghinbox/internal/judge/jev"
	"ghinbox/internal/judge/ollama"
	"ghinbox/internal/scoring"
	"ghinbox/internal/secrets"
	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// Settings keys and secret names for the judge step.
const (
	SettingJudge   = "judge.settings"
	SettingWeights = "scoring.weights"
	SecretJevKey   = "jev.api_key"

	EventJudgmentsUpdated = "judgments:updated"
)

// JudgeSettings are user-editable (Settings view). The Jev key lives in secrets.
type JudgeSettings struct {
	Provider         string `json:"provider"` // auto | jev | ollama | off
	JevModel         string `json:"jevModel"`
	JevBaseURL       string `json:"jevBaseUrl"`
	OllamaURL        string `json:"ollamaUrl"`
	OllamaJudgeModel string `json:"ollamaJudgeModel"`
	MaxPerRun        int    `json:"maxPerRun"`
	Concurrency      int    `json:"concurrency"`
	ProfileInterests string `json:"profileInterests"`
}

// DefaultJudgeSettings are used until the user saves their own.
func DefaultJudgeSettings() JudgeSettings {
	return JudgeSettings{Provider: "auto", JevModel: jev.DefaultModel, JevBaseURL: jev.DefaultBaseURL, OllamaURL: ollama.DefaultURL, MaxPerRun: 200, Concurrency: 3}
}

func (p *Pipeline) JudgeSettings(ctx context.Context) (JudgeSettings, error) {
	s := DefaultJudgeSettings()
	if _, err := p.deps.DB.GetSetting(ctx, SettingJudge, &s); err != nil {
		return s, err
	}
	if s.MaxPerRun <= 0 {
		s.MaxPerRun = 200
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 3
	}
	return s, nil
}

func (p *Pipeline) SetJudgeSettings(ctx context.Context, s JudgeSettings) error {
	return p.deps.DB.SetSetting(ctx, SettingJudge, s)
}

// HasJevKey reports whether a Jev API key is stored.
func (p *Pipeline) HasJevKey() bool {
	_, err := p.deps.Secrets.Get(SecretJevKey)
	return err == nil
}

func (p *Pipeline) SetJevKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return p.deps.Secrets.Delete(SecretJevKey)
	}
	return p.deps.Secrets.Set(SecretJevKey, key)
}

func (p *Pipeline) Weights(ctx context.Context) (scoring.Weights, error) {
	w := scoring.Defaults()
	if _, err := p.deps.DB.GetSetting(ctx, SettingWeights, &w); err != nil {
		return w, err
	}
	return w, nil
}

func (p *Pipeline) SetWeights(ctx context.Context, w scoring.Weights) error {
	if err := p.deps.DB.SetSetting(ctx, SettingWeights, w); err != nil {
		return err
	}
	p.deps.Emit(EventInboxUpdated, nil)
	return nil
}

// ErrJudgeOff is returned when no provider is configured/selected.
var ErrJudgeOff = errors.New("judge: no provider configured (add a Jev key or an Ollama model in Settings)")

// Judge builds the configured provider: explicit choice, or auto = Jev when a
// key exists, else Ollama when a model is set.
func (p *Pipeline) Judge(ctx context.Context) (judge.Judge, JudgeSettings, error) {
	s, err := p.JudgeSettings(ctx)
	if err != nil {
		return nil, s, err
	}
	if p.judgeOverride != nil {
		return p.judgeOverride, s, nil
	}
	key, keyErr := p.deps.Secrets.Get(SecretJevKey)
	useJev := func() (judge.Judge, error) {
		if keyErr != nil || key == "" {
			return nil, fmt.Errorf("%w: Jev selected but no API key stored", ErrJudgeOff)
		}
		return jev.New(jev.Config{APIKey: key, BaseURL: s.JevBaseURL, Model: s.JevModel}), nil
	}
	useOllama := func() (judge.Judge, error) {
		if s.OllamaJudgeModel == "" {
			return nil, fmt.Errorf("%w: Ollama selected but no judge model set", ErrJudgeOff)
		}
		return ollama.New(ollama.Config{BaseURL: s.OllamaURL, Model: s.OllamaJudgeModel}), nil
	}
	switch s.Provider {
	case "off":
		return nil, s, ErrJudgeOff
	case "jev":
		j, err := useJev()
		return j, s, err
	case "ollama":
		j, err := useOllama()
		return j, s, err
	default: // auto
		if keyErr == nil && key != "" {
			j, err := useJev()
			return j, s, err
		}
		if s.OllamaJudgeModel != "" {
			j, err := useOllama()
			return j, s, err
		}
		return nil, s, ErrJudgeOff
	}
}

// JudgeReport summarises one judging run for an account.
type JudgeReport struct {
	AccountID  int64         `json:"accountId"`
	Login      string        `json:"login"`
	Provider   string        `json:"provider"`
	Model      string        `json:"model"`
	Candidates int           `json:"candidates"`
	Judged     int           `json:"judged"`
	Enriched   int           `json:"enriched"`
	Failed     int           `json:"failed"`
	Usage      judge.Usage   `json:"usage"`
	Duration   time.Duration `json:"duration"`
	Errors     []string      `json:"errors"`
	Error      string        `json:"error"`
}

// JudgeAll judges pending threads for every account.
func (p *Pipeline) JudgeAll(ctx context.Context) ([]JudgeReport, error) {
	accts, err := p.deps.DB.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var reports []JudgeReport
	for _, a := range accts {
		rep, err := p.JudgeAccount(ctx, a, 0)
		if err != nil && rep.Error == "" {
			rep.Error = err.Error()
		}
		reports = append(reports, rep)
		if errors.Is(err, ErrJudgeOff) || errors.Is(err, judge.ErrUnauthorized) || errors.Is(err, judge.ErrInvalidRequest) {
			break // configuration problem: same for every account
		}
		if ctx.Err() != nil {
			break
		}
	}
	return reports, nil
}

// JudgeAccount enriches and judges up to limit (0 = settings.MaxPerRun) stale or
// unjudged visible threads for one account.
func (p *Pipeline) JudgeAccount(ctx context.Context, acct store.Account, limit int) (JudgeReport, error) {
	start := time.Now()
	rep := JudgeReport{AccountID: acct.ID, Login: acct.Login}
	j, settings, err := p.Judge(ctx)
	if err != nil {
		rep.Error = err.Error()
		return rep, err
	}
	rep.Provider = j.Name()
	if limit <= 0 {
		limit = settings.MaxPerRun
	}
	set := judge.Triage()
	cands, err := p.deps.DB.JudgeCandidates(ctx, acct.ID, set.Version, limit)
	if err != nil {
		return rep, err
	}
	rep.Candidates = len(cands)
	if len(cands) == 0 {
		rep.Duration = time.Since(start)
		return rep, nil
	}
	src, err := p.Source(ctx, acct)
	if err != nil {
		rep.Error = err.Error()
		return rep, err
	}
	enricher, _ := src.(source.Enricher)

	var mu sync.Mutex
	var fatal error
	sem := make(chan struct{}, settings.Concurrency)
	var wg sync.WaitGroup
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, t := range cands {
		if runCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t store.Thread) {
			defer wg.Done()
			defer func() { <-sem }()
			res, enriched, err := p.judgeThread(runCtx, j, settings, acct, enricher, t)
			mu.Lock()
			defer mu.Unlock()
			if enriched {
				rep.Enriched++
			}
			if err != nil {
				rep.Failed++
				if len(rep.Errors) < 10 {
					rep.Errors = append(rep.Errors, t.ThreadID+": "+err.Error())
				}
				if errors.Is(err, judge.ErrUnauthorized) || errors.Is(err, judge.ErrInvalidRequest) || errors.Is(err, judge.ErrUnavailable) {
					if fatal == nil {
						fatal = err
					}
					cancel()
				}
				return
			}
			rep.Judged++
			rep.Model = res.Model
			rep.Usage.InputTokens += res.Usage.InputTokens
			rep.Usage.OutputTokens += res.Usage.OutputTokens
		}(t)
	}
	wg.Wait()
	rep.Duration = time.Since(start)
	if rep.Judged > 0 {
		p.deps.Emit(EventJudgmentsUpdated, rep)
		p.deps.Emit(EventInboxUpdated, nil)
	}
	if fatal != nil {
		rep.Error = fatal.Error()
		return rep, fatal
	}
	return rep, nil
}

// judgeThread enriches (if stale) and judges one thread, storing both.
func (p *Pipeline) judgeThread(ctx context.Context, j judge.Judge, settings JudgeSettings, acct store.Account, enricher source.Enricher, t store.Thread) (judge.Response, bool, error) {
	enriched := false
	version := t.Version()
	if enricher != nil && t.EnrichedVersion != version {
		e, err := enricher.Enrich(ctx, t)
		if err != nil {
			p.deps.Logger.Warn("enrich failed; judging without it", "thread", t.ThreadID, "err", err)
		} else {
			e.EnrichedVersion = version
			if err := p.deps.DB.SetThreadEnrichment(ctx, acct.ID, t.ThreadID, e); err == nil {
				t.Enrichment = e
				enriched = true
			}
		}
	}
	state := BuildTriageState(acct, t, settings.ProfileInterests)
	res, err := j.Ask(ctx, state, judge.Triage().Questions)
	if err != nil {
		return res, enriched, err
	}
	answers, _ := json.Marshal(res.Answers)
	usage, _ := json.Marshal(res.Usage)
	err = p.deps.DB.PutJudgment(ctx, store.Judgment{
		AccountID: acct.ID, ThreadID: t.ThreadID, ThreadVersion: version, QuestionsVersion: judge.TriageVersion,
		Provider: res.Provider, Model: res.Model, Calibrated: res.Calibrated, AnswersJSON: answers, UsageJSON: usage,
		LatencyMs: res.Latency.Milliseconds(),
	})
	return res, enriched, err
}

// BuildTriageState assembles the JSON state for triage.v1 from a thread.
func BuildTriageState(acct store.Account, t store.Thread, interests string) judge.TriageState {
	isAuthor := t.ItemAuthor != "" && strings.EqualFold(t.ItemAuthor, acct.Login)
	for _, r := range t.RelationTags {
		if r == "author" {
			isAuthor = true
		}
	}
	st := judge.TriageState{
		Thread: judge.TriageThread{
			Forge: acct.Forge, Repo: t.Repo, SubjectType: t.SubjectType, Number: t.SubjectNumber, Title: t.Title,
			Body: source.TrimText(t.ItemBody, 1500), State: t.ItemState, IsDraft: t.ItemDraft, Labels: t.ItemLabels,
			Author: t.ItemAuthor, ActivityKind: t.ActivityKind, Reason: t.Reason, UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
		},
		Me:      judge.TriageMe{Login: acct.Login, RelationTags: t.RelationTags, IsAuthor: isAuthor},
		Profile: judge.TriageProfile{Interests: interests},
	}
	if st.Profile.Interests == "" {
		st.Profile.Interests = "(not provided)"
	}
	author := t.LatestAuthor
	if author == "" {
		author = t.Actor
	}
	if t.LatestBody != "" || author != "" {
		act := &judge.TriageActivity{Author: author, Body: source.TrimText(t.LatestBody, 1500), Kind: activityKindLabel(t)}
		if t.LatestAt != nil {
			act.CreatedAt = t.LatestAt.UTC().Format(time.RFC3339)
		}
		st.LatestActivity = act
	}
	return st
}

func activityKindLabel(t store.Thread) string {
	switch t.ActivityKind {
	case "new_issue", "new_pr":
		if t.LatestCommentURL == "" || t.LatestCommentURL == t.SubjectURL {
			return "item_created_or_pushed"
		}
	case "comment", "mention":
		return "comment"
	case "review", "review_requested":
		return t.ActivityKind
	}
	return t.ActivityKind
}

// Explanation is the judged view of one thread for the UI.
type Explanation struct {
	Thread    store.Thread            `json:"thread"`
	Version   string                  `json:"version"`
	Judgment  *store.Judgment         `json:"judgment"`
	Answers   map[string]judge.Answer `json:"answers"`
	Stale     bool                    `json:"stale"`
	Score     scoring.Result          `json:"score"`
	State     judge.TriageState       `json:"state"`
	Questions []judge.Question        `json:"questions"`
	Weights   scoring.Weights         `json:"weights"`
}

// Explain returns state, answers and score for a thread (judging it first when asked).
func (p *Pipeline) Explain(ctx context.Context, acct store.Account, threadID string, judgeNow bool) (Explanation, error) {
	t, err := p.deps.DB.GetThread(ctx, acct.ID, threadID)
	if err != nil {
		return Explanation{}, err
	}
	settings, _ := p.JudgeSettings(ctx)
	w, _ := p.Weights(ctx)
	if judgeNow {
		j, _, err := p.Judge(ctx)
		if err != nil {
			return Explanation{}, err
		}
		src, err := p.Source(ctx, acct)
		if err != nil {
			return Explanation{}, err
		}
		enricher, _ := src.(source.Enricher)
		if _, _, err := p.judgeThread(ctx, j, settings, acct, enricher, t); err != nil {
			return Explanation{}, err
		}
		p.deps.Emit(EventInboxUpdated, nil)
		t, _ = p.deps.DB.GetThread(ctx, acct.ID, threadID)
	}
	ex := Explanation{Thread: t, Version: t.Version(), State: BuildTriageState(acct, t, settings.ProfileInterests), Questions: judge.Triage().Questions, Weights: w}
	if j, err := p.deps.DB.GetJudgment(ctx, acct.ID, threadID, judge.TriageVersion); err == nil {
		ex.Judgment = &j
		ex.Stale = j.ThreadVersion != ex.Version
		_ = json.Unmarshal(j.AnswersJSON, &ex.Answers)
	}
	ex.Score = ScoreThread(w, t, ex.Judgment, ex.Answers, acct.Login, time.Now())
	return ex, nil
}

// ScoreThread scores one thread from its stored judgment (nil = unjudged), without impact data.
func ScoreThread(w scoring.Weights, t store.Thread, j *store.Judgment, answers map[string]judge.Answer, login string, now time.Time) scoring.Result {
	return scoreThread(w, t, j, answers, nil, login, now)
}

// Scored pairs a thread with its score; used by the inbox listing.
type Scored struct {
	Thread store.Thread   `json:"thread"`
	Score  scoring.Result `json:"score"`
}

// ScoreThreads scores many threads with the stored judgments and current weights.
func (p *Pipeline) ScoreThreads(ctx context.Context, threads []store.Thread) ([]Scored, error) {
	w, err := p.Weights(ctx)
	if err != nil {
		return nil, err
	}
	js, err := p.deps.DB.ListJudgments(ctx, 0, judge.TriageVersion)
	if err != nil {
		return nil, err
	}
	logins := map[int64]string{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			logins[a.ID] = a.Login
		}
	}
	analyses, err := p.deps.DB.AnalysesByKey(ctx, 0)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]Scored, 0, len(threads))
	for _, t := range threads {
		var jp *store.Judgment
		var answers map[string]judge.Answer
		if j, ok := js[store.JudgmentKey(t.AccountID, t.ThreadID)]; ok {
			jp = &j
			_ = json.Unmarshal(j.AnswersJSON, &answers)
		}
		var ap *store.Analysis
		if t.SubjectNumber != 0 && (t.SubjectType == "PullRequest" || t.SubjectType == "MergeRequest") {
			if a, ok := analyses[store.AnalysisKey(t.AccountID, t.Repo, t.SubjectNumber)]; ok && a.ImpactLevel >= 0 {
				ap = &a
			}
		}
		out = append(out, Scored{Thread: t, Score: scoreThread(w, t, jp, answers, ap, logins[t.AccountID], now)})
	}
	return out, nil
}

// scoreThread is ScoreThread with an optional impact analysis.
func scoreThread(w scoring.Weights, t store.Thread, j *store.Judgment, answers map[string]judge.Answer, a *store.Analysis, login string, now time.Time) scoring.Result {
	in := scoring.Inputs{Kind: t.ActivityKind, Relations: t.RelationTags, UpdatedAt: t.UpdatedAt, ImpactLevel: -1}
	in.IsAuthor = t.ItemAuthor != "" && strings.EqualFold(t.ItemAuthor, login)
	for _, r := range t.RelationTags {
		if r == "author" {
			in.IsAuthor = true
		}
	}
	if j != nil && j.ThreadVersion == t.Version() && answers != nil {
		in.Answers = answers
		in.Calibrated = j.Calibrated
	}
	if a != nil {
		in.ImpactLevel = a.ImpactLevel
		in.ImpactMerged = a.State == "merged"
	}
	return scoring.Score(w, in, now)
}

// SecretsBackendKeyHint documents where the key lives, for Diagnostics.
func SecretsBackendKeyHint() string {
	return secrets.AccountKey(0)[:len("account:")] + "… / " + SecretJevKey
}
