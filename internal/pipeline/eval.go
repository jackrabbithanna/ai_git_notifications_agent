package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitinbox/internal/eval"
	"gitinbox/internal/judge"
	"gitinbox/internal/judge/jev"
	"gitinbox/internal/judge/ollama"
	"gitinbox/internal/scoring"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// EventLabelsUpdated fires when a label changes.
const EventLabelsUpdated = "labels:updated"

// PrimaryProvider names the judgments table (whatever produced them) in eval.
const PrimaryProvider = "primary"

// --- labeling ---------------------------------------------------------------

// LabelQueueItem is a thread offered for labeling, with the app's current view.
type LabelQueueItem struct {
	Thread   store.Thread            `json:"thread"`
	Score    scoring.Result          `json:"score"`
	Answers  map[string]judge.Answer `json:"answers"`
	Label    *store.Label            `json:"label"`
	Provider string                  `json:"provider"`
}

// LabelQueue lists threads to label: unlabeled first (judged ones prioritised,
// by priority), then labeled ones. accountID 0 = all.
func (p *Pipeline) LabelQueue(ctx context.Context, accountID int64, limit int, includeLabeled bool) ([]LabelQueueItem, error) {
	threads, err := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: accountID, IncludeRead: true, IncludeDone: true, IncludeNoise: true, Limit: 2000})
	if err != nil {
		return nil, err
	}
	scored, err := p.ScoreThreads(ctx, threads)
	if err != nil {
		return nil, err
	}
	labels, err := p.deps.DB.ListLabels(ctx, accountID)
	if err != nil {
		return nil, err
	}
	js, _ := p.deps.DB.ListJudgments(ctx, accountID, judge.TriageVersion)
	var out []LabelQueueItem
	for _, sc := range scored {
		key := store.JudgmentKey(sc.Thread.AccountID, sc.Thread.ThreadID)
		l, labeled := labels[key]
		if labeled && !includeLabeled {
			continue
		}
		it := LabelQueueItem{Thread: sc.Thread, Score: sc.Score}
		if j, ok := js[key]; ok {
			it.Provider = j.Provider
			_ = json.Unmarshal(j.AnswersJSON, &it.Answers)
		}
		if labeled {
			lc := l
			it.Label = &lc
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].Label != nil, out[j].Label != nil
		if li != lj {
			return !li // unlabeled first
		}
		ji, jj := out[i].Score.Judged, out[j].Score.Judged
		if ji != jj {
			return ji // judged first (their answers are what we evaluate)
		}
		return out[i].Score.Priority > out[j].Score.Priority
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SetLabel validates and stores a label; an empty label (nothing set) deletes it.
func (p *Pipeline) SetLabel(ctx context.Context, l store.Label) error {
	if l.Category != "" {
		valid := false
		for _, o := range judge.Triage().Questions[0].Options {
			if o.ID == l.Category {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("unknown category %q", l.Category)
		}
	}
	if l.Urgency < -1 || l.Urgency > 3 || l.Relevance < -1 || l.Relevance > 2 || l.Priority < -1 || l.Priority > 3 {
		return errors.New("urgency 0..3, relevance 0..2, priority 0..3 (or -1 to unset)")
	}
	if _, err := p.deps.DB.GetThread(ctx, l.AccountID, l.ThreadID); err != nil {
		return err
	}
	empty := l.Category == "" && l.RequiresAction == nil && l.Urgency < 0 && l.Relevance < 0 && l.Priority < 0 && l.Resolved == nil && l.Noise == nil && l.Note == ""
	var err error
	if empty {
		err = p.deps.DB.DeleteLabel(ctx, l.AccountID, l.ThreadID)
	} else {
		err = p.deps.DB.PutLabel(ctx, l)
	}
	if err == nil {
		p.deps.Emit(EventLabelsUpdated, nil)
	}
	return err
}

// SetPRLabel stores the impact ground truth for a PR (level -1 and empty kind deletes nothing; it just unsets).
func (p *Pipeline) SetPRLabel(ctx context.Context, l store.PRLabel) error {
	if l.ImpactLevel < -1 || l.ImpactLevel > 3 {
		return errors.New("impact level 0..3 (or -1 to unset)")
	}
	if err := p.deps.DB.PutPRLabel(ctx, l); err != nil {
		return err
	}
	p.deps.Emit(EventLabelsUpdated, nil)
	return nil
}

// --- samples ----------------------------------------------------------------

// Samples assembles eval samples for a provider: PrimaryProvider uses the
// judgments table; any other name uses eval_judgments for that provider.
func (p *Pipeline) Samples(ctx context.Context, provider string) ([]eval.Sample, string, error) {
	labels, err := p.deps.DB.ListLabels(ctx, 0)
	if err != nil {
		return nil, "", err
	}
	if len(labels) == 0 {
		return nil, "", nil
	}
	type ans struct {
		answers    map[string]judge.Answer
		calibrated bool
		version    string
		model      string
	}
	byKey := map[string]ans{}
	model := ""
	if provider == "" || provider == PrimaryProvider {
		js, err := p.deps.DB.ListJudgments(ctx, 0, judge.TriageVersion)
		if err != nil {
			return nil, "", err
		}
		for k, j := range js {
			var a map[string]judge.Answer
			_ = json.Unmarshal(j.AnswersJSON, &a)
			byKey[k] = ans{a, j.Calibrated, j.ThreadVersion, j.Provider + " " + j.Model}
			model = j.Model
		}
	} else {
		js, err := p.deps.DB.ListEvalJudgments(ctx, provider)
		if err != nil {
			return nil, "", err
		}
		for k, j := range js {
			var a map[string]judge.Answer
			_ = json.Unmarshal(j.AnswersJSON, &a)
			byKey[k] = ans{a, j.Calibrated, j.ThreadVersion, j.Model}
			model = j.Model
		}
	}
	analyses, _ := p.deps.DB.AnalysesByKey(ctx, 0)
	accounts := map[int64]store.Account{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			accounts[a.ID] = a
		}
	}
	now := time.Now()
	var samples []eval.Sample
	for key, l := range labels {
		t, err := p.deps.DB.GetThread(ctx, l.AccountID, l.ThreadID)
		if err != nil {
			continue
		}
		s := eval.Sample{Key: key, Title: t.Title, Repo: t.Repo, Kind: t.ActivityKind, Relations: t.RelationTags, Filter: t.FilterVerdict, ImpactLevel: -1,
			Category: l.Category, RequiresAction: l.RequiresAction, Urgency: l.Urgency, Relevance: l.Relevance, Priority: l.Priority, Resolved: l.Resolved, Noise: l.Noise,
			UpdatedAgeH: now.Sub(t.UpdatedAt).Hours()}
		login := accounts[t.AccountID].Login
		s.IsAuthor = t.ItemAuthor != "" && strings.EqualFold(t.ItemAuthor, login)
		for _, r := range t.RelationTags {
			if r == "author" {
				s.IsAuthor = true
			}
		}
		if a, ok := byKey[key]; ok && a.answers != nil {
			s.Answers, s.Calibrated = a.answers, a.calibrated
		}
		if (t.SubjectType == "PullRequest" || t.SubjectType == "MergeRequest") && t.SubjectNumber != 0 {
			if an, ok := analyses[store.AnalysisKey(t.AccountID, t.Repo, t.SubjectNumber)]; ok && an.ImpactLevel >= 0 {
				s.ImpactLevel = an.ImpactLevel
			}
		}
		samples = append(samples, s)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Key < samples[j].Key })
	return samples, model, nil
}

// --- eval judging -----------------------------------------------------------

// EvalJudgeReport summarises an eval re-judging run.
type EvalJudgeReport struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Labeled  int           `json:"labeled"`
	Judged   int           `json:"judged"`
	Skipped  int           `json:"skipped"` // already judged at this thread version
	Failed   int           `json:"failed"`
	Usage    judge.Usage   `json:"usage"`
	Duration time.Duration `json:"duration"`
	Errors   []string      `json:"errors"`
}

// EvalJudge re-judges every labeled thread with a named provider into
// eval_judgments (provider "jev" or "ollama"; model "" = settings).
func (p *Pipeline) EvalJudge(ctx context.Context, provider, model string, force bool) (EvalJudgeReport, error) {
	start := time.Now()
	rep := EvalJudgeReport{Provider: provider, Model: model}
	settings, err := p.JudgeSettings(ctx)
	if err != nil {
		return rep, err
	}
	var j judge.Judge
	switch provider {
	case "jev":
		key, err := p.deps.Secrets.Get(SecretJevKey)
		if err != nil || key == "" {
			return rep, fmt.Errorf("%w: no Jev key stored", ErrJudgeOff)
		}
		if model == "" {
			model = settings.JevModel
		}
		j = jev.New(jev.Config{APIKey: key, BaseURL: settings.JevBaseURL, Model: model})
	case "ollama":
		if model == "" {
			model = settings.OllamaJudgeModel
		}
		if model == "" {
			return rep, fmt.Errorf("%w: no Ollama model given", ErrJudgeOff)
		}
		j = ollama.New(ollama.Config{BaseURL: settings.OllamaURL, Model: model})
	default:
		if p.judgeOverride != nil {
			j = p.judgeOverride
		} else {
			return rep, fmt.Errorf("eval judge: provider must be jev or ollama, got %q", provider)
		}
	}
	rep.Model = model
	labels, err := p.deps.DB.ListLabels(ctx, 0)
	if err != nil {
		return rep, err
	}
	rep.Labeled = len(labels)
	have, _ := p.deps.DB.ListEvalJudgments(ctx, provider)
	accounts := map[int64]store.Account{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			accounts[a.ID] = a
		}
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		l := labels[key]
		t, err := p.deps.DB.GetThread(ctx, l.AccountID, l.ThreadID)
		if err != nil {
			continue
		}
		if ej, ok := have[key]; ok && ej.ThreadVersion == t.Version() && !force {
			rep.Skipped++
			continue
		}
		acct := accounts[l.AccountID]
		if t.EnrichedVersion != t.Version() {
			if src, err := p.Source(ctx, acct); err == nil {
				if en, ok := src.(source.Enricher); ok {
					if e, err := en.Enrich(ctx, t); err == nil {
						e.EnrichedVersion = t.Version()
						if p.deps.DB.SetThreadEnrichment(ctx, acct.ID, t.ThreadID, e) == nil {
							t.Enrichment = e
						}
					}
				}
			}
		}
		res, err := j.Ask(ctx, BuildTriageState(acct, t, settings.ProfileInterests), judge.Triage().Questions)
		if err != nil {
			rep.Failed++
			if len(rep.Errors) < 10 {
				rep.Errors = append(rep.Errors, key+": "+err.Error())
			}
			if errors.Is(err, judge.ErrUnauthorized) || errors.Is(err, judge.ErrInvalidRequest) || errors.Is(err, judge.ErrUnavailable) {
				rep.Duration = time.Since(start)
				return rep, err
			}
			continue
		}
		answers, _ := json.Marshal(res.Answers)
		usage, _ := json.Marshal(res.Usage)
		if err := p.deps.DB.PutEvalJudgment(ctx, store.EvalJudgment{AccountID: l.AccountID, ThreadID: l.ThreadID, Provider: provider, Model: res.Model, Calibrated: res.Calibrated,
			ThreadVersion: t.Version(), AnswersJSON: answers, UsageJSON: usage, LatencyMs: res.Latency.Milliseconds()}); err != nil {
			return rep, err
		}
		rep.Judged++
		rep.Usage.InputTokens += res.Usage.InputTokens
		rep.Usage.OutputTokens += res.Usage.OutputTokens
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

// --- evaluate & tune --------------------------------------------------------

// Evaluate computes the report for one provider under the current weights.
func (p *Pipeline) Evaluate(ctx context.Context, provider string) (eval.Report, error) {
	samples, model, err := p.Samples(ctx, provider)
	if err != nil {
		return eval.Report{}, err
	}
	w, err := p.Weights(ctx)
	if err != nil {
		return eval.Report{}, err
	}
	if provider == "" {
		provider = PrimaryProvider
	}
	return eval.Evaluate(samples, w, provider, model), nil
}

// Tune searches weights on the primary provider's samples; apply stores them.
func (p *Pipeline) Tune(ctx context.Context, iters int, apply bool) (eval.TuneResult, error) {
	samples, _, err := p.Samples(ctx, PrimaryProvider)
	if err != nil {
		return eval.TuneResult{}, err
	}
	w, err := p.Weights(ctx)
	if err != nil {
		return eval.TuneResult{}, err
	}
	if iters <= 0 {
		iters = 400
	}
	res := eval.Tune(samples, w, iters, 42)
	if apply && res.NDCGTo > res.NDCGFrom {
		if err := p.SetWeights(ctx, res.After); err != nil {
			return res, err
		}
	}
	return res, nil
}

// EvalOverview lists what is available to evaluate.
type EvalOverview struct {
	Labels    int            `json:"labels"`
	PRLabels  int            `json:"prLabels"`
	Providers map[string]int `json:"providers"` // eval_judgments per provider
	Primary   int            `json:"primary"`   // labeled threads with a primary judgment
}

func (p *Pipeline) EvalOverview(ctx context.Context) (EvalOverview, error) {
	labels, err := p.deps.DB.ListLabels(ctx, 0)
	if err != nil {
		return EvalOverview{}, err
	}
	prl, _ := p.deps.DB.ListPRLabels(ctx)
	prov, _ := p.deps.DB.EvalProviders(ctx)
	ov := EvalOverview{Labels: len(labels), PRLabels: len(prl), Providers: prov}
	if js, err := p.deps.DB.ListJudgments(ctx, 0, judge.TriageVersion); err == nil {
		for k := range labels {
			if _, ok := js[k]; ok {
				ov.Primary++
			}
		}
	}
	return ov, nil
}

// ImpactEval compares PR labels with stored analyses.
type ImpactEval struct {
	N         int      `json:"n"`
	Exact     float64  `json:"exact"`
	WithinOne float64  `json:"withinOne"`
	MAE       float64  `json:"mae"`
	KindAcc   float64  `json:"kindAccuracy"`
	KindN     int      `json:"kindN"`
	Mistakes  []string `json:"mistakes"`
}

func (p *Pipeline) EvaluateImpact(ctx context.Context) (ImpactEval, error) {
	labels, err := p.deps.DB.ListPRLabels(ctx)
	if err != nil {
		return ImpactEval{}, err
	}
	analyses, err := p.deps.DB.AnalysesByKey(ctx, 0)
	if err != nil {
		return ImpactEval{}, err
	}
	var ev ImpactEval
	var exact, within, mae, kindOK float64
	for k, l := range labels {
		a, ok := analyses[k]
		if !ok || a.ImpactLevel < 0 {
			continue
		}
		if l.ImpactLevel >= 0 {
			ev.N++
			d := float64(a.ImpactLevel - l.ImpactLevel)
			if d < 0 {
				d = -d
			}
			mae += d
			if d == 0 {
				exact++
			}
			if d <= 1 {
				within++
			}
			if d > 0 && len(ev.Mistakes) < 30 {
				ev.Mistakes = append(ev.Mistakes, fmt.Sprintf("%s#%d: labeled %d, judged %d (%s)", a.Repo, a.Number, l.ImpactLevel, a.ImpactLevel, a.ChangeKind))
			}
		}
		if l.ChangeKind != "" {
			ev.KindN++
			if l.ChangeKind == a.ChangeKind {
				kindOK++
			}
		}
	}
	if ev.N > 0 {
		ev.Exact, ev.WithinOne, ev.MAE = exact/float64(ev.N), within/float64(ev.N), mae/float64(ev.N)
	}
	if ev.KindN > 0 {
		ev.KindAcc = kindOK / float64(ev.KindN)
	}
	return ev, nil
}

// --- export / import --------------------------------------------------------

// ExportLabels writes labels as JSON lines (one per thread; forge/host/repo make them portable).
func (p *Pipeline) ExportLabels(ctx context.Context, w io.Writer) (int, error) {
	labels, err := p.deps.DB.ListLabels(ctx, 0)
	if err != nil {
		return 0, err
	}
	accounts := map[int64]store.Account{}
	if accts, err := p.deps.DB.ListAccounts(ctx); err == nil {
		for _, a := range accts {
			accounts[a.ID] = a
		}
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	enc := json.NewEncoder(w)
	n := 0
	for _, k := range keys {
		l := labels[k]
		t, err := p.deps.DB.GetThread(ctx, l.AccountID, l.ThreadID)
		if err != nil {
			continue
		}
		a := accounts[l.AccountID]
		rec := map[string]any{"forge": a.Forge, "host": a.Host, "login": a.Login, "thread_id": l.ThreadID, "repo": t.Repo, "subject_type": t.SubjectType, "number": t.SubjectNumber, "title": t.Title,
			"category": l.Category, "requires_action": l.RequiresAction, "urgency": l.Urgency, "relevance": l.Relevance, "priority": l.Priority, "resolved": l.Resolved, "noise": l.Noise, "note": l.Note}
		if err := enc.Encode(rec); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ImportLabels reads JSON lines written by ExportLabels (matching on login/host + thread_id).
func (p *Pipeline) ImportLabels(ctx context.Context, r io.Reader) (int, error) {
	accts, err := p.deps.DB.ListAccounts(ctx)
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec struct {
			Host, Login, ThreadID, Category, Note string
			RequiresAction, Resolved, Noise       *bool
			Urgency, Relevance, Priority          *int
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return n, err
		}
		get := func(k string, v any) { _ = json.Unmarshal(raw[k], v) }
		get("host", &rec.Host)
		get("login", &rec.Login)
		get("thread_id", &rec.ThreadID)
		get("category", &rec.Category)
		get("note", &rec.Note)
		get("requires_action", &rec.RequiresAction)
		get("resolved", &rec.Resolved)
		get("noise", &rec.Noise)
		get("urgency", &rec.Urgency)
		get("relevance", &rec.Relevance)
		get("priority", &rec.Priority)
		var acct *store.Account
		for i := range accts {
			if strings.EqualFold(accts[i].Login, rec.Login) && strings.EqualFold(accts[i].Host, rec.Host) {
				acct = &accts[i]
			}
		}
		if acct == nil {
			continue
		}
		l := store.Label{AccountID: acct.ID, ThreadID: rec.ThreadID, Category: rec.Category, RequiresAction: rec.RequiresAction, Resolved: rec.Resolved, Noise: rec.Noise, Note: rec.Note, Urgency: -1, Relevance: -1, Priority: -1}
		if rec.Urgency != nil {
			l.Urgency = *rec.Urgency
		}
		if rec.Relevance != nil {
			l.Relevance = *rec.Relevance
		}
		if rec.Priority != nil {
			l.Priority = *rec.Priority
		}
		if err := p.SetLabel(ctx, l); err != nil {
			return n, fmt.Errorf("%s: %w", rec.ThreadID, err)
		}
		n++
	}
	return n, sc.Err()
}

// --- report -----------------------------------------------------------------

// ReportMarkdown renders reports for the primary judgments and every eval provider.
func (p *Pipeline) ReportMarkdown(ctx context.Context) (string, error) {
	ov, err := p.EvalOverview(ctx)
	if err != nil {
		return "", err
	}
	w, _ := p.Weights(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "# GitInbox evaluation — %s\n\n", time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "Labeled threads: %d (%d with a primary judgment) · labeled PRs: %d · eval providers: %v\n\n", ov.Labels, ov.Primary, ov.PRLabels, ov.Providers)
	providers := []string{PrimaryProvider}
	names := make([]string, 0, len(ov.Providers))
	for k := range ov.Providers {
		names = append(names, k)
	}
	sort.Strings(names)
	providers = append(providers, names...)
	fmt.Fprintf(&b, "| provider | model | judged | category acc | sure/unsure acc | needs-action P/R/F1 | Brier | resolved F1 | urgency exact/±1/MAE | relevance exact/±1/MAE | NDCG@10/@25 | ρ | needs-me bucket F1 |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	var reports []eval.Report
	for _, prov := range providers {
		r, err := p.Evaluate(ctx, prov)
		if err != nil {
			return "", err
		}
		reports = append(reports, r)
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %.0f%% (n=%d) | %.0f%% / %.0f%% (%d unsure) | %.2f/%.2f/%.2f | %.3f | %.2f | %.0f%%/%.0f%%/%.2f | %.0f%%/%.0f%%/%.2f | %.2f/%.2f | %.2f | %.2f |\n",
			r.Provider, r.Model, r.Judged, r.Samples, 100*r.Category.Accuracy, r.Category.N, 100*r.Category.AccSure, 100*r.Category.AccUnsure, r.Category.Unsure,
			r.RequiresAction.Precision, r.RequiresAction.Recall, r.RequiresAction.F1, r.RequiresAction.Brier, r.Resolved.F1,
			100*r.Urgency.Exact, 100*r.Urgency.WithinOne, r.Urgency.MAE, 100*r.Relevance.Exact, 100*r.Relevance.WithinOne, r.Relevance.MAE,
			r.Ranking.NDCG10, r.Ranking.NDCG25, r.Ranking.Spearman, r.NeedsMeBucket.F1)
	}
	if len(reports) > 0 {
		r := reports[0]
		fmt.Fprintf(&b, "\n## Filter (rule-based noise) vs labels\n\nprecision %.2f · recall %.2f · F1 %.2f (n=%d)\n", r.Filter.Precision, r.Filter.Recall, r.Filter.F1, r.Filter.N)
		for _, m := range r.FilterMissed {
			fmt.Fprintf(&b, "- missed noise: %s — %s\n", m.Key, m.Title)
		}
		if len(r.Category.Confusion) > 0 {
			fmt.Fprintf(&b, "\n## Category confusion (%s)\n\n| label \\ predicted | count |\n|---|---|\n", r.Provider)
			labels := make([]string, 0, len(r.Category.Confusion))
			for k := range r.Category.Confusion {
				labels = append(labels, k)
			}
			sort.Strings(labels)
			for _, l := range labels {
				preds := make([]string, 0)
				for pk, n := range r.Category.Confusion[l] {
					preds = append(preds, fmt.Sprintf("%s ×%d", pk, n))
				}
				sort.Strings(preds)
				fmt.Fprintf(&b, "| %s | %s |\n", l, strings.Join(preds, ", "))
			}
		}
		if len(r.Category.Mistakes) > 0 {
			fmt.Fprintf(&b, "\n## Category mistakes (%s)\n\n", r.Provider)
			for _, m := range r.Category.Mistakes {
				fmt.Fprintf(&b, "- %s — labeled %s, predicted %s (conf %.2f): %s\n", m.Key, m.Label, m.Predicted, m.Confidence, m.Title)
			}
		}
	}
	if ie, err := p.EvaluateImpact(ctx); err == nil && ie.N+ie.KindN > 0 {
		fmt.Fprintf(&b, "\n## Impact analyses vs PR labels\n\nlevel exact %.0f%% · within one %.0f%% · MAE %.2f (n=%d) · change-kind accuracy %.0f%% (n=%d)\n", 100*ie.Exact, 100*ie.WithinOne, ie.MAE, ie.N, 100*ie.KindAcc, ie.KindN)
		for _, m := range ie.Mistakes {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	wj, _ := json.MarshalIndent(w, "", "  ")
	fmt.Fprintf(&b, "\n## Current weights\n\n```json\n%s\n```\n", wj)
	return b.String(), nil
}

// WriteReport writes the Markdown report to dir/report-<timestamp>.md.
func (p *Pipeline) WriteReport(ctx context.Context, dir string) (string, error) {
	md, err := p.ReportMarkdown(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report-"+time.Now().Format("20060102-1504")+".md")
	return path, os.WriteFile(path, []byte(md), 0o644)
}
