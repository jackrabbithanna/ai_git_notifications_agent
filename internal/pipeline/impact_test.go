package pipeline

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ghinbox/internal/judge"
	"ghinbox/internal/llm"
	"ghinbox/internal/profiles"
	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// changerSource adds Changer + LandedLister to fakeSource.
type changerSource struct {
	fakeSource
	sha     string
	changes int
	merged  []source.ChangeRef
}

func (c *changerSource) Changes(_ context.Context, repo string, number int) (source.ChangeSet, error) {
	c.changes++
	return source.ChangeSet{Forge: "github", Repo: repo, Number: number, Kind: "pr", Title: "APIv4 - change getFields signature", Body: "BREAKING", State: "open",
		HeadSHA: c.sha, HTMLURL: "https://github.com/civicrm/civicrm-core/pull/7", Author: "dev", UpdatedAt: time.Now(), FilesTotal: 1, Additions: 3, Deletions: 1,
		Files: []profiles.FileChange{{Path: "Civi/Api4/Contact.php", Status: "modified", Additions: 3, Deletions: 1, Patch: "-  public function getFields() {\n+  public function getFields(bool $x) {\n"}}}, nil
}

func (c *changerSource) RecentlyMerged(_ context.Context, repo string, since time.Time, limit int) ([]source.ChangeRef, error) {
	var out []source.ChangeRef
	for _, r := range c.merged {
		if r.Repo == repo {
			out = append(out, r)
		}
	}
	return out, nil
}

type fakeGen struct{ calls int }

func (g *fakeGen) Name() string { return "fake" }
func (g *fakeGen) Generate(context.Context, string, string, map[string]any) (llm.Result, error) {
	g.calls++
	return llm.Result{Provider: "fake", Model: "fake-note", JSON: json.RawMessage(`{"what_changed":"getFields signature","why_it_matters_for_downstream":"callers break","surfaces_changed":["Civi\\Api4\\Contact::getFields"],"recommended_checks":["grep getFields("],"migration_hints":"pass the new argument","confidence_note":"high"}`)}, nil
}

func TestImpactFlow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	cs := &changerSource{sha: "sha1"}
	cs.threads = []store.Thread{
		{ThreadID: "pr7", Repo: "civicrm/civicrm-core", SubjectType: "PullRequest", SubjectNumber: 7, Title: "APIv4 - change getFields signature", Reason: "subscribed", Unread: true, UpdatedAt: now, ActivityKind: "new_pr", RelationTags: []string{"subscriber"}},
		{ThreadID: "other", Repo: "someone/else", SubjectType: "PullRequest", SubjectNumber: 1, Title: "x", Reason: "subscribed", Unread: true, UpdatedAt: now, ActivityKind: "new_pr"},
	}
	p, acct := newTestPipeline(t, &cs.fakeSource)
	p.sources[acct.ID] = cs
	ctx := context.Background()
	if _, err := p.SyncAccount(ctx, acct, true); err != nil {
		t.Fatal(err)
	}
	fake := &judge.Fake{Answers: map[string]judge.Answer{
		"change_kind":       {Kind: judge.Choice, Choice: "api_change", Probabilities: map[string]float64{"api_change": 0.9, "bugfix": 0.1}, Confidence: 0.9},
		"downstream_impact": {Kind: judge.Score, Score: 2.8, Legend: []string{"none", "possible", "likely", "certain"}, Probabilities: map[string]float64{"3": 0.8, "2": 0.2}, Confidence: 0.8},
	}}
	p.judgeOverride = fake
	gen := &fakeGen{}
	p.generatorOverride = gen

	// A merged PR in a profile repo that produced no notification: found by the landed scan.
	m := now.Add(-time.Hour)
	cs.merged = []source.ChangeRef{{Repo: "civicrm/civicrm-core", Number: 8, Title: "landed", MergedAt: &m, UpdatedAt: now}}

	// Pending analysis: only the profile repo's PR is a candidate; the landed scan adds #8.
	rep, err := p.AnalyzePending(ctx, acct, 0)
	if err != nil {
		t.Fatalf("pending: %v %+v", err, rep)
	}
	if rep.Candidates != 1 || rep.Analysed != 1 || rep.Failed != 0 || rep.Landed != 1 || cs.changes != 2 || fake.Calls != 2 {
		t.Fatalf("pending report: %+v changes=%d judge=%d", rep, cs.changes, fake.Calls)
	}
	if _, err := p.deps.DB.GetAnalysis(ctx, acct.ID, "civicrm/civicrm-core", 8); err != nil {
		t.Fatalf("landed analysis missing: %v", err)
	}
	a, err := p.deps.DB.GetAnalysis(ctx, acct.ID, "civicrm/civicrm-core", 7)
	if err != nil || a.ImpactLevel != 3 || a.ChangeKind != "api_change" || a.ProfileID != "civicrm" || a.HeadSHA != "sha1" || a.ThreadVersion == "" {
		t.Fatalf("analysis: %+v %v", a, err)
	}
	var report profiles.Report
	_ = json.Unmarshal(a.ReportJSON, &report)
	if len(report.Layers) != 1 || report.Layers[0].ID != "data_api" || len(report.SurfaceHits) == 0 || len(report.Signals) == 0 {
		t.Fatalf("report: %+v", report)
	}
	// Second run: same thread version → nothing to do; landed scan throttled; same sha → judge not re-run even when asked directly.
	rep, _ = p.AnalyzePending(ctx, acct, 0)
	if rep.Candidates != 0 || rep.Landed != 0 || fake.Calls != 2 {
		t.Fatalf("idle: %+v calls=%d", rep, fake.Calls)
	}
	if _, ran, err := p.AnalyzePR(ctx, acct, "civicrm/civicrm-core", 7, "", false); err != nil || ran {
		t.Fatalf("same sha must reuse: ran=%v err=%v", ran, err)
	}
	if _, ran, err := p.AnalyzePR(ctx, acct, "civicrm/civicrm-core", 7, "", true); err != nil || !ran || fake.Calls != 3 {
		t.Fatalf("force must re-run: ran=%v calls=%d err=%v", ran, fake.Calls, err)
	}
	// New code (sha) → re-analysed.
	cs.sha = "sha2"
	if _, ran, _ := p.AnalyzePR(ctx, acct, "civicrm/civicrm-core", 7, "", false); !ran || fake.Calls != 4 {
		t.Fatalf("new sha must re-run: ran=%v calls=%d", ran, fake.Calls)
	}
	// Generic fallback for a repo without a profile.
	if a2, _, err := p.AnalyzePR(ctx, acct, "someone/else", 1, "", false); err != nil || a2.ProfileID != profiles.GenericID {
		t.Fatalf("generic fallback: %+v %v", a2, err)
	}
	// Landed scan is throttled between runs.
	settings, _ := p.ImpactSettings(ctx)
	if n, _ := p.ScanLanded(ctx, acct, settings); n != 0 {
		t.Fatal("landed scan must be throttled")
	}
	// Note generation stores the structured note.
	note, err := p.ImpactNote(ctx, acct, "civicrm/civicrm-core", 7)
	if err != nil || note.WhatChanged == "" || gen.calls != 1 {
		t.Fatalf("note: %+v %v calls=%d", note, err, gen.calls)
	}
	views, _ := p.ListImpact(ctx, store.AnalysisQuery{MinLevel: 3, Any: true})
	var pr7 *ImpactView
	for i := range views {
		if views[i].Analysis.Repo == "civicrm/civicrm-core" && views[i].Analysis.Number == 7 {
			pr7 = &views[i]
		}
	}
	if len(views) != 3 || pr7 == nil || pr7.Note == nil || pr7.Report.Layers[0].ID != "data_api" {
		t.Fatalf("impact view: n=%d pr7=%v", len(views), pr7)
	}
	// Scored inbox reflects the impact level.
	threads, _ := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: acct.ID})
	scored, _ := p.ScoreThreads(ctx, threads)
	var found bool
	for _, sc := range scored {
		if sc.Thread.ThreadID == "pr7" {
			found = true
			if sc.Score.ImpactLevel != 3 || sc.Score.Bucket != "needs_me" {
				t.Fatalf("scored impact: %+v", sc.Score)
			}
		}
	}
	if !found {
		t.Fatal("pr7 not scored")
	}
	// Profiles: user override + flags.
	if _, err := p.SaveProfile(ctx, "id: civicrm\nname: Mine\nrepos: ['civicrm/civicrm-core']\nlayers:\n  - id: all\n    paths: ['**']\n", true); err != nil {
		t.Fatal(err)
	}
	all, problems, _ := p.Profiles(ctx)
	var mine bool
	for _, pr := range all {
		if pr.ID == "civicrm" && pr.Source == "user" && pr.Name == "Mine" {
			mine = true
		}
	}
	if !mine || len(problems) != 0 {
		t.Fatalf("profiles override: %+v %v", all, problems)
	}
}
