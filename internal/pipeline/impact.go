package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gitinbox/internal/judge"
	"gitinbox/internal/llm"
	llmollama "gitinbox/internal/llm/ollama"
	"gitinbox/internal/profiles"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Settings key and events for the impact step.
const (
	SettingImpact       = "impact.settings"
	EventImpactUpdated  = "impact:updated"
	impactLevelLikely   = 2
	impactAnalysisLimit = 50
)

// ImpactSettings are user-editable (Settings view).
type ImpactSettings struct {
	AutoAnalyze        bool   `json:"autoAnalyze"`        // analyse PR/MR threads in profile repos after each sync
	MaxPerRun          int    `json:"maxPerRun"`          // analyses per run
	NoteModel          string `json:"noteModel"`          // Ollama model for impact notes; "" = use the judge model, notes on demand
	AutoNoteMinLevel   int    `json:"autoNoteMinLevel"`   // write a note automatically at this level or above (2 = likely); 4 = never
	ScanLanded         bool   `json:"scanLanded"`         // list recently merged PRs in profile repos
	ScanLandedEveryMin int    `json:"scanLandedEveryMin"` // minutes between landed scans
	LandedLookbackDays int    `json:"landedLookbackDays"` // how far back a landed scan looks
}

func DefaultImpactSettings() ImpactSettings {
	return ImpactSettings{AutoAnalyze: true, MaxPerRun: impactAnalysisLimit, AutoNoteMinLevel: 4, ScanLanded: true, ScanLandedEveryMin: 30, LandedLookbackDays: 7}
}

func (p *Pipeline) ImpactSettings(ctx context.Context) (ImpactSettings, error) {
	s := DefaultImpactSettings()
	if _, err := p.deps.DB.GetSetting(ctx, SettingImpact, &s); err != nil {
		return s, err
	}
	if s.MaxPerRun <= 0 {
		s.MaxPerRun = impactAnalysisLimit
	}
	if s.LandedLookbackDays <= 0 {
		s.LandedLookbackDays = 7
	}
	if s.ScanLandedEveryMin <= 0 {
		s.ScanLandedEveryMin = 30
	}
	return s, nil
}

func (p *Pipeline) SetImpactSettings(ctx context.Context, s ImpactSettings) error {
	return p.deps.DB.SetSetting(ctx, SettingImpact, s)
}

// --- profiles ---------------------------------------------------------------

// Profiles returns built-ins (with enable flags applied) and user profiles; a
// user profile with the same id overrides the built-in. Unparseable user
// profiles are reported in problems and skipped.
func (p *Pipeline) Profiles(ctx context.Context) ([]profiles.Profile, []string, error) {
	builtins, err := profiles.Builtins()
	if err != nil {
		return nil, nil, err
	}
	flags, err := p.deps.DB.ProfileFlags(ctx)
	if err != nil {
		return nil, nil, err
	}
	users, err := p.deps.DB.ListUserProfiles(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []profiles.Profile
	var problems []string
	overridden := map[string]bool{}
	for _, u := range users {
		prof, err := profiles.Parse([]byte(u.YAML))
		if err != nil {
			problems = append(problems, u.ID+": "+err.Error())
			continue
		}
		if prof.ID != u.ID {
			problems = append(problems, fmt.Sprintf("%s: yaml id %q differs from row id", u.ID, prof.ID))
			continue
		}
		prof.Source = "user"
		prof.Enabled = u.Enabled
		out = append(out, prof)
		overridden[prof.ID] = true
	}
	for _, b := range builtins {
		if overridden[b.ID] {
			continue
		}
		if en, ok := flags[b.ID]; ok {
			b.Enabled = en
		}
		out = append(out, b)
	}
	return out, problems, nil
}

// ProfileFor finds the enabled, non-generic profile covering a repo.
func (p *Pipeline) ProfileFor(ctx context.Context, acct store.Account, repo string) (profiles.Profile, bool) {
	all, _, err := p.Profiles(ctx)
	if err != nil {
		return profiles.Profile{}, false
	}
	for _, prof := range all {
		if prof.Enabled && prof.ID != profiles.GenericID && prof.MatchesRepo(acct.Forge, acct.Host, repo) {
			return prof, true
		}
	}
	return profiles.Profile{}, false
}

func (p *Pipeline) profileByID(ctx context.Context, id string) (profiles.Profile, error) {
	all, _, err := p.Profiles(ctx)
	if err != nil {
		return profiles.Profile{}, err
	}
	for _, prof := range all {
		if prof.ID == id {
			return prof, nil
		}
	}
	return profiles.Profile{}, fmt.Errorf("profile %q not found", id)
}

// SaveProfile validates and stores a user profile (YAML).
func (p *Pipeline) SaveProfile(ctx context.Context, yamlSrc string, enabled bool) (profiles.Profile, error) {
	prof, err := profiles.Parse([]byte(yamlSrc))
	if err != nil {
		return profiles.Profile{}, err
	}
	if err := p.deps.DB.PutUserProfile(ctx, prof.ID, yamlSrc, enabled); err != nil {
		return profiles.Profile{}, err
	}
	prof.Source = "user"
	prof.Enabled = enabled
	return prof, nil
}

// --- analysis ---------------------------------------------------------------

// ImpactReport summarises one analysis run.
type ImpactReport struct {
	AccountID  int64         `json:"accountId"`
	Login      string        `json:"login"`
	Candidates int           `json:"candidates"`
	Analysed   int           `json:"analysed"`
	Skipped    int           `json:"skipped"` // unchanged head sha
	Failed     int           `json:"failed"`
	Landed     int           `json:"landed"` // merged PRs found by the scan
	Duration   time.Duration `json:"duration"`
	Errors     []string      `json:"errors"`
	Error      string        `json:"error"`
}

// AnalyzePR analyses one pull/merge request against a profile (explicit id,
// the matching profile, or the generic fallback). Unless force, an analysis
// for the same head sha and question set is reused (state is refreshed).
// The second result reports whether the judge actually ran.
func (p *Pipeline) AnalyzePR(ctx context.Context, acct store.Account, repo string, number int, profileID string, force bool) (store.Analysis, bool, error) {
	var prof profiles.Profile
	var err error
	if profileID != "" {
		if prof, err = p.profileByID(ctx, profileID); err != nil {
			return store.Analysis{}, false, err
		}
	} else if m, ok := p.ProfileFor(ctx, acct, repo); ok {
		prof = m
	} else if prof, err = p.profileByID(ctx, profiles.GenericID); err != nil {
		return store.Analysis{}, false, err
	}
	src, err := p.Source(ctx, acct)
	if err != nil {
		return store.Analysis{}, false, err
	}
	changer, ok := src.(source.Changer)
	if !ok {
		return store.Analysis{}, false, fmt.Errorf("account %s cannot fetch changes", acct.Login)
	}
	cs, err := changer.Changes(ctx, repo, number)
	if err != nil {
		return store.Analysis{}, false, err
	}
	existing, gerr := p.deps.DB.GetAnalysis(ctx, acct.ID, repo, number)
	a := store.Analysis{AccountID: acct.ID, Forge: acct.Forge, Repo: repo, Number: number, Kind: cs.Kind, HeadSHA: cs.HeadSHA, ProfileID: prof.ID,
		Title: cs.Title, HTMLURL: cs.HTMLURL, Author: cs.Author, State: cs.State, MergedAt: cs.MergedAt, ImpactLevel: -1}
	if !cs.UpdatedAt.IsZero() {
		u := cs.UpdatedAt
		a.UpdatedAt = &u
	}
	if gerr == nil {
		a.CreatedAt = existing.CreatedAt
		a.ThreadVersion = existing.ThreadVersion
		if !force && existing.HeadSHA == cs.HeadSHA && existing.QuestionsVersion == judge.ImpactVersion && existing.ImpactLevel >= 0 && existing.ProfileID == prof.ID {
			existing.State, existing.MergedAt, existing.UpdatedAt, existing.Title = a.State, a.MergedAt, a.UpdatedAt, a.Title
			if err := p.deps.DB.PutAnalysis(ctx, existing); err != nil {
				return existing, false, err
			}
			return existing, false, nil
		}
	}

	report := profiles.Analyze(prof, cs.Title, cs.Body, cs.Labels, cs.Files)
	a.ReportJSON, _ = json.Marshal(report)
	state := BuildImpactState(acct, cs, report, prof)

	j, _, err := p.Judge(ctx)
	if err != nil {
		a.Error = err.Error()
		_ = p.deps.DB.PutAnalysis(ctx, a)
		return a, false, err
	}
	res, err := j.Ask(ctx, state, judge.Impact().Questions)
	if err != nil {
		a.Error = err.Error()
		_ = p.deps.DB.PutAnalysis(ctx, a)
		return a, false, err
	}
	a.QuestionsVersion = judge.ImpactVersion
	a.Provider, a.Model, a.Calibrated = res.Provider, res.Model, res.Calibrated
	a.AnswersJSON, _ = json.Marshal(res.Answers)
	a.UsageJSON, _ = json.Marshal(res.Usage)
	if imp, ok := res.Answers["downstream_impact"]; ok {
		a.ImpactScore = imp.Score
		a.ImpactLevel = int(math.Round(imp.Score))
		if a.ImpactLevel < 0 {
			a.ImpactLevel = 0
		}
		if a.ImpactLevel > 3 {
			a.ImpactLevel = 3
		}
	}
	if ck, ok := res.Answers["change_kind"]; ok {
		a.ChangeKind = ck.Choice
	}
	now := time.Now()
	a.AnalysedAt = &now
	a.Error = ""
	if err := p.deps.DB.PutAnalysis(ctx, a); err != nil {
		return a, true, err
	}
	p.deps.Emit(EventImpactUpdated, nil)
	p.deps.Emit(EventInboxUpdated, nil)

	if settings, _ := p.ImpactSettings(ctx); a.ImpactLevel >= settings.AutoNoteMinLevel {
		go func() {
			if _, err := p.ImpactNote(context.Background(), acct, repo, number); err != nil {
				p.deps.Logger.Warn("impact note", "repo", repo, "number", number, "err", err)
			}
		}()
	}
	return a, true, nil
}

// BuildImpactState assembles the JSON state for impact.v1.
func BuildImpactState(acct store.Account, cs source.ChangeSet, r profiles.Report, prof profiles.Profile) judge.ImpactState {
	st := judge.ImpactState{
		PR: judge.ImpactPR{Forge: acct.Forge, Repo: cs.Repo, Number: cs.Number, Title: cs.Title, Body: source.TrimText(cs.Body, 2000), Labels: cs.Labels, Base: cs.Base,
			State: cs.State, Draft: cs.Draft, Author: cs.Author, FilesTotal: cs.FilesTotal, Additions: cs.Additions, Deletions: cs.Deletions},
		Signals:               r.Signals,
		DownstreamDescription: strings.TrimSpace(prof.DownstreamDescription),
	}
	if st.DownstreamDescription == "" {
		st.DownstreamDescription = "(not provided)"
	}
	for _, l := range r.Layers {
		st.LayersTouched = append(st.LayersTouched, judge.ImpactLayer{ID: l.ID, Label: l.Label, Weight: l.Weight, Files: l.Files, Additions: l.Additions, Deletions: l.Deletions})
	}
	for _, h := range r.SurfaceHits {
		st.SurfaceHits = append(st.SurfaceHits, judge.ImpactHit{Pattern: h.PatternID, Path: h.Path, Line: h.Line})
	}
	for _, f := range r.TopFiles {
		layer := ""
		if l, ok := prof.LayerFor(f.Path); ok {
			layer = l.ID
		}
		st.ChangedFiles = append(st.ChangedFiles, judge.ImpactFile{Path: f.Path, Layer: layer, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
	}
	return st
}

// AnalyzePending analyses unread PR/MR threads in profile repos that have no
// analysis for their current thread version, then (when due) scans profile
// repos for recently merged changes.
func (p *Pipeline) AnalyzePending(ctx context.Context, acct store.Account, limit int) (ImpactReport, error) {
	start := time.Now()
	rep := ImpactReport{AccountID: acct.ID, Login: acct.Login}
	settings, err := p.ImpactSettings(ctx)
	if err != nil {
		return rep, err
	}
	if limit <= 0 {
		limit = settings.MaxPerRun
	}
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: acct.ID, Limit: limit * 4})
	if err != nil {
		return rep, err
	}
	have, err := p.deps.DB.AnalysesByKey(ctx, acct.ID)
	if err != nil {
		return rep, err
	}
	type cand struct {
		t    store.Thread
		prof profiles.Profile
	}
	var cands []cand
	for _, t := range threads {
		if (t.SubjectType != "PullRequest" && t.SubjectType != "MergeRequest") || t.SubjectNumber == 0 {
			continue
		}
		prof, ok := p.ProfileFor(ctx, acct, t.Repo)
		if !ok {
			continue
		}
		if a, ok := have[store.AnalysisKey(acct.ID, t.Repo, t.SubjectNumber)]; ok && a.ThreadVersion == t.Version() && a.ImpactLevel >= 0 {
			continue
		}
		cands = append(cands, cand{t, prof})
		if len(cands) >= limit {
			break
		}
	}
	rep.Candidates = len(cands)
	var fatal error
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		a, ran, err := p.AnalyzePR(ctx, acct, c.t.Repo, c.t.SubjectNumber, c.prof.ID, false)
		if err != nil {
			rep.Failed++
			if len(rep.Errors) < 10 {
				rep.Errors = append(rep.Errors, fmt.Sprintf("%s#%d: %v", c.t.Repo, c.t.SubjectNumber, err))
			}
			if errors.Is(err, judge.ErrUnauthorized) || errors.Is(err, judge.ErrInvalidRequest) || errors.Is(err, ErrJudgeOff) || errors.Is(err, judge.ErrUnavailable) {
				fatal = err
				break
			}
			continue
		}
		a.ThreadVersion = c.t.Version() // not re-analysed until the thread changes
		if err := p.deps.DB.PutAnalysis(ctx, a); err != nil {
			return rep, err
		}
		if ran {
			rep.Analysed++
		} else {
			rep.Skipped++
		}
	}
	if fatal == nil && settings.ScanLanded {
		n, err := p.ScanLanded(ctx, acct, settings)
		rep.Landed = n
		if err != nil {
			rep.Errors = append(rep.Errors, "landed scan: "+err.Error())
		}
	}
	rep.Duration = time.Since(start)
	if rep.Analysed > 0 || rep.Landed > 0 {
		p.deps.Emit(EventImpactUpdated, nil)
	}
	if fatal != nil {
		rep.Error = fatal.Error()
		return rep, fatal
	}
	return rep, nil
}

// ScanLanded lists recently merged PRs in the account's profile repos and
// analyses the ones not seen yet. Throttled by ScanLandedEveryMin.
func (p *Pipeline) ScanLanded(ctx context.Context, acct store.Account, settings ImpactSettings) (int, error) {
	state, err := p.deps.DB.GetSyncState(ctx, acct.ID)
	if err != nil {
		return 0, err
	}
	if state.LastLandedScan != nil && time.Since(*state.LastLandedScan) < time.Duration(settings.ScanLandedEveryMin)*time.Minute {
		return 0, nil
	}
	src, err := p.Source(ctx, acct)
	if err != nil {
		return 0, err
	}
	lister, ok := src.(source.LandedLister)
	if !ok {
		return 0, nil
	}
	all, _, err := p.Profiles(ctx)
	if err != nil {
		return 0, err
	}
	have, err := p.deps.DB.AnalysesByKey(ctx, acct.ID)
	if err != nil {
		return 0, err
	}
	since := time.Now().Add(-time.Duration(settings.LandedLookbackDays) * 24 * time.Hour)
	if state.LastLandedScan != nil && state.LastLandedScan.After(since) {
		since = state.LastLandedScan.Add(-time.Hour)
	}
	found := 0
	var firstErr error
	for _, prof := range all {
		if !prof.Enabled || prof.ID == profiles.GenericID {
			continue
		}
		for _, entry := range prof.Repos {
			repo, ok := repoForAccount(entry, acct)
			if !ok {
				continue
			}
			refs, err := lister.RecentlyMerged(ctx, repo, since, 30)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, ref := range refs {
				if _, seen := have[store.AnalysisKey(acct.ID, repo, ref.Number)]; seen {
					continue
				}
				if _, _, err := p.AnalyzePR(ctx, acct, repo, ref.Number, prof.ID, false); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					if errors.Is(err, judge.ErrUnauthorized) || errors.Is(err, ErrJudgeOff) || errors.Is(err, judge.ErrUnavailable) {
						return found, err
					}
					continue
				}
				found++
			}
		}
	}
	now := time.Now()
	state.LastLandedScan = &now
	_ = p.deps.DB.PutSyncState(ctx, state)
	return found, firstErr
}

// repoForAccount resolves a profile repo entry to a repo path on this account's forge.
func repoForAccount(entry string, acct store.Account) (string, bool) {
	e := strings.TrimSpace(entry)
	lower := strings.ToLower(e)
	switch {
	case strings.HasPrefix(lower, "github:"):
		if acct.Forge != store.ForgeGitHub {
			return "", false
		}
		return e[len("github:"):], true
	case strings.HasPrefix(lower, "gitlab:"):
		if acct.Forge != store.ForgeGitLab {
			return "", false
		}
		rest := e[len("gitlab:"):]
		if strings.HasPrefix(strings.ToLower(rest), strings.ToLower(acct.Host)+"/") {
			return rest[len(acct.Host)+1:], true
		}
		if first := strings.SplitN(rest, "/", 2)[0]; strings.Contains(first, ".") {
			return "", false // host-qualified for another host
		}
		return rest, true
	default:
		if acct.Forge != store.ForgeGitHub {
			return "", false
		}
		return e, true
	}
}

// --- notes ------------------------------------------------------------------

const noteSystemPrompt = `You write short, concrete impact notes for a developer who builds on top of an upstream project.
You are given the pull request metadata, the layers it touches, detected public-surface changes, the judged impact, and what the developer builds.
Write for that developer: what changed, why it matters to their code, which surfaces changed, concrete checks to run, and migration hints if anything breaks.
Be specific (name files, functions, fields). Say when evidence is thin. Answer ONLY with JSON matching the schema.`

// ImpactNote generates (and stores) the structured note for an analysed PR.
func (p *Pipeline) ImpactNote(ctx context.Context, acct store.Account, repo string, number int) (llm.ImpactNote, error) {
	settings, err := p.ImpactSettings(ctx)
	if err != nil {
		return llm.ImpactNote{}, err
	}
	js, _ := p.JudgeSettings(ctx)
	model := settings.NoteModel
	if model == "" {
		model = js.OllamaJudgeModel
	}
	if model == "" && p.generatorOverride == nil {
		return llm.ImpactNote{}, fmt.Errorf("%w: no Ollama model configured for notes", llm.ErrUnavailable)
	}
	a, err := p.deps.DB.GetAnalysis(ctx, acct.ID, repo, number)
	if err != nil {
		return llm.ImpactNote{}, err
	}
	if a.ImpactLevel < 0 {
		return llm.ImpactNote{}, errors.New("analyse the pull request first")
	}
	prof, err := p.profileByID(ctx, a.ProfileID)
	if err != nil {
		return llm.ImpactNote{}, err
	}
	var report profiles.Report
	_ = json.Unmarshal(a.ReportJSON, &report)
	var answers map[string]judge.Answer
	_ = json.Unmarshal(a.AnswersJSON, &answers)
	user := map[string]any{
		"pr":                     map[string]any{"repo": a.Repo, "number": a.Number, "title": a.Title, "state": a.State, "author": a.Author, "url": a.HTMLURL},
		"downstream_description": prof.DownstreamDescription,
		"layers_touched":         report.Layers,
		"signals":                report.Signals,
		"surface_hits":           report.SurfaceHits,
		"changed_files":          report.TopFiles,
		"judged":                 map[string]any{"change_kind": a.ChangeKind, "impact_level": a.ImpactLevel, "answers": answers},
	}
	userJSON, _ := json.MarshalIndent(user, "", "  ")
	gen := p.noteGenerator(js.OllamaURL, model)
	res, err := gen.Generate(ctx, noteSystemPrompt, string(userJSON), llm.ImpactNoteSchema())
	if err != nil {
		return llm.ImpactNote{}, err
	}
	var note llm.ImpactNote
	if err := json.Unmarshal(res.JSON, &note); err != nil {
		return llm.ImpactNote{}, fmt.Errorf("impact note: decode: %w", err)
	}
	if err := p.deps.DB.SetAnalysisNote(ctx, acct.ID, repo, number, res.JSON, res.Model); err != nil {
		return note, err
	}
	p.deps.Emit(EventImpactUpdated, nil)
	return note, nil
}

// noteGenerator builds the generator (overridable in tests).
func (p *Pipeline) noteGenerator(baseURL, model string) llm.Generator {
	if p.generatorOverride != nil {
		return p.generatorOverride
	}
	return llmollama.New(llmollama.Config{BaseURL: baseURL, Model: model})
}

// ImpactView is one row of the Impact view.
type ImpactView struct {
	Analysis store.Analysis          `json:"analysis"`
	Report   profiles.Report         `json:"report"`
	Answers  map[string]judge.Answer `json:"answers"`
	Note     *llm.ImpactNote         `json:"note"`
}

// ListImpact returns analysed PRs for the Impact view.
func (p *Pipeline) ListImpact(ctx context.Context, q store.AnalysisQuery) ([]ImpactView, error) {
	list, err := p.deps.DB.ListAnalyses(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]ImpactView, 0, len(list))
	for _, a := range list {
		out = append(out, impactViewOf(a))
	}
	return out, nil
}

// GetImpact returns the analysis view for one PR/MR, or nil when not analysed.
func (p *Pipeline) GetImpact(ctx context.Context, accountID int64, repo string, number int) (*ImpactView, error) {
	a, err := p.deps.DB.GetAnalysis(ctx, accountID, repo, number)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.ImpactLevel < 0 && a.Error == "" {
		return nil, nil
	}
	v := impactViewOf(a)
	return &v, nil
}

// impactViewOf decodes an analysis row for the UI.
func impactViewOf(a store.Analysis) ImpactView {
	v := ImpactView{Analysis: a}
	_ = json.Unmarshal(a.ReportJSON, &v.Report)
	_ = json.Unmarshal(a.AnswersJSON, &v.Answers)
	if len(a.NoteJSON) > 0 {
		var n llm.ImpactNote
		if json.Unmarshal(a.NoteJSON, &n) == nil {
			v.Note = &n
		}
	}
	return v
}
