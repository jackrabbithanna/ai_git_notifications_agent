package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateAndAccounts(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, err := db.InsertAccount(ctx, ForgeGitHub, "octocat", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Host != "github.com" || a.WriteMode != "readonly" || a.Forge != ForgeGitHub {
		t.Fatalf("defaults: %+v", a)
	}
	gl, err := db.InsertAccount(ctx, ForgeGitLab, "octocat", "")
	if err != nil || gl.Host != "gitlab.com" || gl.Forge != ForgeGitLab {
		t.Fatalf("gitlab defaults: %+v %v", gl, err)
	}
	if err := db.SetAccountTokenScopes(ctx, gl.ID, "read_api"); err != nil {
		t.Fatal(err)
	}
	if g, _ := db.GetAccount(ctx, gl.ID); g.TokenScopes != "read_api" {
		t.Fatalf("scopes: %+v", g)
	}
	if _, err := db.InsertAccount(ctx, ForgeGitHub, "octocat", ""); err == nil {
		t.Fatal("duplicate login/host should fail")
	}
	if err := db.SetAccountWriteMode(ctx, a.ID, "notifications"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAccount(ctx, a.ID)
	if err != nil || got.WriteMode != "notifications" {
		t.Fatalf("got %+v, %v", got, err)
	}
	st, err := db.GetSyncState(ctx, a.ID)
	if err != nil || st.PollIntervalSec != 180 {
		t.Fatalf("sync state: %+v %v", st, err)
	}
	if err := db.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAccount(ctx, a.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpsertThreadPreservesAndResurfaces(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "octocat", "")
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	th := Thread{AccountID: a.ID, ThreadID: "t1", Repo: "o/r", SubjectType: "PullRequest", Title: "PR", Reason: "subscribed",
		Unread: true, UpdatedAt: base, ActivityKind: "new_pr", RelationTags: []string{"subscriber"}, FilterVerdict: "keep"}
	if err := db.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkThreadDone(ctx, a.ID, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SnoozeThread(ctx, a.ID, "t1", base.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Same updated_at: local state must survive a re-sync.
	if err := db.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetThread(ctx, a.ID, "t1")
	if got.DoneAt == nil || got.SnoozedUntil == nil || got.LocalReadAt == nil {
		t.Fatalf("local state lost on unchanged re-sync: %+v", got)
	}
	if got.FirstSeenAt.IsZero() || got.LastSyncedAt.IsZero() {
		t.Fatalf("timestamps missing: %+v", got)
	}
	list, _ := db.ListThreads(ctx, ThreadQuery{AccountID: a.ID})
	if len(list) != 0 {
		t.Fatalf("done thread should be hidden by default, got %d", len(list))
	}
	// New activity: re-surfaces.
	th.UpdatedAt = base.Add(time.Hour)
	th.Title = "PR (updated)"
	if err := db.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetThread(ctx, a.ID, "t1")
	if got.DoneAt != nil || got.SnoozedUntil != nil || got.LocalReadAt != nil {
		t.Fatalf("new activity should clear local state: %+v", got)
	}
	if got.Title != "PR (updated)" || !got.UpdatedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("github fields not refreshed: %+v", got)
	}
	list, _ = db.ListThreads(ctx, ThreadQuery{AccountID: a.ID})
	if len(list) != 1 {
		t.Fatalf("expected 1 visible thread, got %d", len(list))
	}
	c, err := db.Counts(ctx, 0)
	if err != nil || c.Unread != 1 {
		t.Fatalf("counts: %+v %v", c, err)
	}
}

func TestItemsPrune(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "octocat", "")
	now := time.Now()
	for i, n := range []int{1, 2} {
		it := Item{AccountID: a.ID, Repo: "o/r", Number: n, Kind: "issue", Title: "x", State: "open", CreatedAt: now, UpdatedAt: now, Relations: []string{"assigned"}}
		if err := db.UpsertItem(ctx, it, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := db.PruneItems(ctx, a.ID, 2)
	if err != nil || pruned != 1 {
		t.Fatalf("pruned=%d err=%v", pruned, err)
	}
	items, _ := db.ListItems(ctx, a.ID, false)
	if len(items) != 1 || items[0].Number != 2 || items[0].Relations[0] != "assigned" {
		t.Fatalf("items: %+v", items)
	}
}

func TestSettings(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	type cfg struct {
		A int `json:"a"`
	}
	var c cfg
	ok, err := db.GetSetting(ctx, "x", &c)
	if err != nil || ok {
		t.Fatalf("missing setting: ok=%v err=%v", ok, err)
	}
	if err := db.SetSetting(ctx, "x", cfg{A: 7}); err != nil {
		t.Fatal(err)
	}
	ok, err = db.GetSetting(ctx, "x", &c)
	if err != nil || !ok || c.A != 7 {
		t.Fatalf("got %+v ok=%v err=%v", c, ok, err)
	}
}

func TestMigrateFromM1Schema(t *testing.T) {
	// Build a database as M1 left it (only 0001 applied), then Open must apply 0002.
	path := filepath.Join(t.TempDir(), "m1.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations VALUES (1, 'x');
		INSERT INTO accounts (login, host, write_mode, created_at) VALUES ('old', 'github.com', 'readonly', 'x');
		INSERT INTO threads (account_id, thread_id, repo, subject_type, title, reason, updated_at, first_seen_at, last_synced_at) VALUES (1, 't', 'o/r', 'Issue', 'T', 'subscribed', 'x', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("migrate 0001->0002: %v", err)
	}
	defer db.Close()
	a, err := db.GetAccount(context.Background(), 1)
	if err != nil || a.Forge != ForgeGitHub || a.Login != "old" {
		t.Fatalf("old account after migration: %+v %v", a, err)
	}
	th, err := db.GetThread(context.Background(), 1, "t")
	if err != nil || th.Actor != "" {
		t.Fatalf("old thread after migration: %+v %v", th, err)
	}
	if err := db.AddWatched(context.Background(), 1, "/dev/core/"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchedAndReadExcept(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitLab, "me", "lab.example.org")
	if err := db.AddWatched(ctx, a.ID, "dev/core"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddWatched(ctx, a.ID, "dev/core"); err != nil { // idempotent
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.UpdateWatched(ctx, a.ID, "dev/core", 42, &now, ""); err != nil {
		t.Fatal(err)
	}
	ws, _ := db.ListWatched(ctx, a.ID)
	if len(ws) != 1 || ws[0].ProjectID != 42 || ws[0].LastEventAt == nil || !ws[0].LastEventAt.Equal(now) {
		t.Fatalf("watched: %+v", ws)
	}
	if err := db.UpdateWatched(ctx, a.ID, "dev/core", 42, nil, "boom"); err != nil {
		t.Fatal(err)
	}
	ws, _ = db.ListWatched(ctx, a.ID)
	if ws[0].LastError != "boom" || ws[0].LastEventAt == nil {
		t.Fatalf("nil lastEventAt must keep previous value: %+v", ws)
	}
	if err := db.RemoveWatched(ctx, a.ID, "nope"); err != ErrNotFound {
		t.Fatalf("remove unknown: %v", err)
	}

	for _, id := range []string{"todo:1", "todo:2", "gl:1:Issue:5"} {
		th := Thread{AccountID: a.ID, ThreadID: id, Repo: "dev/core", SubjectType: "Issue", Title: id, Reason: "assigned", Actor: "bob", Unread: true, UpdatedAt: now, ActivityKind: "assignment", FilterVerdict: "keep"}
		if err := db.UpsertThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.MarkThreadsReadExcept(ctx, a.ID, "todo:", []string{"todo:2"})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	t1, _ := db.GetThread(ctx, a.ID, "todo:1")
	t2, _ := db.GetThread(ctx, a.ID, "todo:2")
	ev, _ := db.GetThread(ctx, a.ID, "gl:1:Issue:5")
	if t1.Unread || !t2.Unread || !ev.Unread || t1.Actor != "bob" {
		t.Fatalf("read-except: %v %v %v actor=%q", t1.Unread, t2.Unread, ev.Unread, t1.Actor)
	}
}

func TestJudgmentsAndEnrichment(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "me", "")
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	th := Thread{AccountID: a.ID, ThreadID: "t1", Repo: "o/r", SubjectType: "Issue", Title: "T", Reason: "mention", Unread: true, UpdatedAt: base, ActivityKind: "mention", FilterVerdict: "keep"}
	if err := db.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetThread(ctx, a.ID, "t1")
	v1 := got.Version()
	if v1 == "" || len(v1) != 16 {
		t.Fatalf("version: %q", v1)
	}
	cands, err := db.JudgeCandidates(ctx, a.ID, "triage.v1", 10)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates before judging: %d %v", len(cands), err)
	}
	when := base.Add(-time.Hour)
	if err := db.SetThreadEnrichment(ctx, a.ID, "t1", Enrichment{ItemAuthor: "alice", ItemState: "open", ItemLabels: []string{"bug"}, ItemBody: "desc", LatestAuthor: "bob", LatestBody: "ping @me", LatestAt: &when, EnrichedVersion: v1}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutJudgment(ctx, Judgment{AccountID: a.ID, ThreadID: "t1", ThreadVersion: v1, QuestionsVersion: "triage.v1", Provider: "fake", Calibrated: false,
		AnswersJSON: json.RawMessage(`{"resolved":{"kind":"noul","noul":0.1}}`), UsageJSON: json.RawMessage(`{"inputTokens":10,"outputTokens":2}`), LatencyMs: 5}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetThread(ctx, a.ID, "t1")
	if got.ItemAuthor != "alice" || got.ItemLabels[0] != "bug" || got.LatestBody != "ping @me" || got.LatestAt == nil || got.EnrichedVersion != v1 {
		t.Fatalf("enrichment: %+v", got.Enrichment)
	}
	cands, _ = db.JudgeCandidates(ctx, a.ID, "triage.v1", 10)
	if len(cands) != 0 {
		t.Fatalf("judged thread must not be a candidate: %d", len(cands))
	}
	js, _ := db.ListJudgments(ctx, 0, "triage.v1")
	if j := js[JudgmentKey(a.ID, "t1")]; j.Provider != "fake" || j.ThreadVersion != v1 {
		t.Fatalf("list: %+v", js)
	}
	st, _ := db.JudgmentStatsFor(ctx, "triage.v1")
	if st.Total != 1 || st.ByProvider["fake"] != 1 || st.InputTokens != 10 {
		t.Fatalf("stats: %+v", st)
	}
	// New activity → new version → candidate again; enrichment survives but is stale by version.
	th.UpdatedAt = base.Add(time.Hour)
	if err := db.UpsertThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetThread(ctx, a.ID, "t1")
	if got.Version() == v1 || got.EnrichedVersion != v1 || got.ItemAuthor != "alice" {
		t.Fatalf("after activity: version=%s enriched=%s", got.Version(), got.EnrichedVersion)
	}
	cands, _ = db.JudgeCandidates(ctx, a.ID, "triage.v1", 10)
	if len(cands) != 1 {
		t.Fatalf("stale judgment must re-candidate: %d", len(cands))
	}
}

func TestProfilesAndAnalyses(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "me", "")
	if err := db.PutUserProfile(ctx, "mine", "id: mine\nlayers: []\n", true); err != nil {
		t.Fatal(err)
	}
	ps, _ := db.ListUserProfiles(ctx)
	if len(ps) != 1 || ps[0].ID != "mine" || !ps[0].Enabled {
		t.Fatalf("profiles: %+v", ps)
	}
	if err := db.SetProfileEnabled(ctx, "mine", false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetProfileEnabled(ctx, "civicrm", false); err != nil {
		t.Fatal(err)
	}
	ps, _ = db.ListUserProfiles(ctx)
	flags, _ := db.ProfileFlags(ctx)
	if ps[0].Enabled || flags["civicrm"] != false {
		t.Fatalf("enable flags: %+v %v", ps, flags)
	}
	now := time.Now().UTC().Truncate(time.Second)
	an := Analysis{AccountID: a.ID, Forge: ForgeGitHub, Repo: "civicrm/civicrm-core", Number: 7, Kind: "pr", HeadSHA: "abc", ProfileID: "civicrm", Title: "T", State: "open", UpdatedAt: &now,
		ReportJSON: json.RawMessage(`{"layers":[]}`), QuestionsVersion: "impact.v1", Provider: "fake", AnswersJSON: json.RawMessage(`{}`), ImpactLevel: 2, ImpactScore: 2.2, ChangeKind: "api_change", AnalysedAt: &now}
	if err := db.PutAnalysis(ctx, an); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAnalysis(ctx, a.ID, "civicrm/civicrm-core", 7)
	if err != nil || got.ImpactLevel != 2 || got.HeadSHA != "abc" || got.NoteJSON != nil || got.AnalysedAt == nil {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := db.SetAnalysisNote(ctx, a.ID, "civicrm/civicrm-core", 7, json.RawMessage(`{"what_changed":"x"}`), "qwen"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetAnalysis(ctx, a.ID, "civicrm/civicrm-core", 7)
	if string(got.NoteJSON) != `{"what_changed":"x"}` || got.NoteModel != "qwen" {
		t.Fatalf("note: %s %s", got.NoteJSON, got.NoteModel)
	}
	an.Number = 8
	an.State = "merged"
	an.MergedAt = &now
	an.ImpactLevel = 3
	_ = db.PutAnalysis(ctx, an)
	an.Number = 9
	an.State = "open"
	an.ImpactLevel = -1
	_ = db.PutAnalysis(ctx, an)
	open, _ := db.ListAnalyses(ctx, AnalysisQuery{MinLevel: 0})
	landed, _ := db.ListAnalyses(ctx, AnalysisQuery{MinLevel: 2, Landed: true})
	all, _ := db.ListAnalyses(ctx, AnalysisQuery{MinLevel: -1, Any: true})
	if len(open) != 1 || open[0].Number != 7 || len(landed) != 1 || landed[0].Number != 8 || len(all) != 3 {
		t.Fatalf("lists: open=%d landed=%d all=%d", len(open), len(landed), len(all))
	}
	byKey, _ := db.AnalysesByKey(ctx, 0)
	if byKey[AnalysisKey(a.ID, "civicrm/civicrm-core", 8)].ImpactLevel != 3 {
		t.Fatalf("by key: %v", byKey)
	}
	// Enrichment now records the item creation time.
	th := Thread{AccountID: a.ID, ThreadID: "t", Repo: "o/r", SubjectType: "PullRequest", Title: "x", Reason: "subscribed", Unread: true, UpdatedAt: now, ActivityKind: "new_pr", FilterVerdict: "keep"}
	_ = db.UpsertThread(ctx, th)
	created := now.Add(-10 * time.Minute)
	_ = db.SetThreadEnrichment(ctx, a.ID, "t", Enrichment{ItemCreatedAt: &created, EnrichedVersion: "v"})
	got2, _ := db.GetThread(ctx, a.ID, "t")
	if !got2.IsBrandNew() {
		t.Fatalf("brand new: %+v", got2.Enrichment)
	}
	old := now.Add(-48 * time.Hour)
	_ = db.SetThreadEnrichment(ctx, a.ID, "t", Enrichment{ItemCreatedAt: &old, EnrichedVersion: "v"})
	got2, _ = db.GetThread(ctx, a.ID, "t")
	if got2.IsBrandNew() {
		t.Fatal("old item with new activity is a push, not brand new")
	}
	st, _ := db.GetSyncState(ctx, a.ID)
	st.LastLandedScan = &now
	_ = db.PutSyncState(ctx, st)
	st, _ = db.GetSyncState(ctx, a.ID)
	if st.LastLandedScan == nil {
		t.Fatal("landed scan time not stored")
	}
}

func TestProseTables(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "me", "")
	if err := db.PutSummary(ctx, Summary{AccountID: a.ID, ThreadID: "t", ThreadVersion: "v1", Model: "qwen", ContentJSON: json.RawMessage(`{"summary":"s"}`), UsageJSON: json.RawMessage(`{"inputTokens":5,"outputTokens":2}`), LatencyMs: 900}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutSummary(ctx, Summary{AccountID: a.ID, ThreadID: "t", ThreadVersion: "v2", Model: "qwen", ContentJSON: json.RawMessage(`{"summary":"s2"}`), LatencyMs: 100}); err != nil {
		t.Fatal(err)
	}
	s, err := db.GetSummary(ctx, a.ID, "t")
	if err != nil || s.ThreadVersion != "v2" || string(s.ContentJSON) != `{"summary":"s2"}` {
		t.Fatalf("summary: %+v %v", s, err)
	}
	if m, _ := db.SummariesByKey(ctx, 0); len(m) != 1 {
		t.Fatalf("summaries by key: %v", m)
	}
	now := time.Now()
	id, err := db.PutDigest(ctx, Digest{PeriodStart: now.Add(-24 * time.Hour), PeriodEnd: now, Model: "qwen", ContentJSON: json.RawMessage(`{"headline":"h"}`), ThreadCount: 7, LatencyMs: 5000})
	if err != nil || id == 0 {
		t.Fatalf("digest: %d %v", id, err)
	}
	ds, _ := db.ListDigests(ctx, 5)
	if len(ds) != 1 || ds[0].ThreadCount != 7 || ds[0].PeriodEnd.IsZero() {
		t.Fatalf("digests: %+v", ds)
	}
	if ok, _ := db.MarkNotified(ctx, "thread:1:t:v2"); !ok {
		t.Fatal("first notification must be new")
	}
	if ok, _ := db.MarkNotified(ctx, "thread:1:t:v2"); ok {
		t.Fatal("second notification must be a duplicate")
	}
	_ = db.PutJudgment(ctx, Judgment{AccountID: a.ID, ThreadID: "t", ThreadVersion: "v2", QuestionsVersion: "triage.v1", Provider: "jev", Model: "jev-1", AnswersJSON: json.RawMessage(`{}`), UsageJSON: json.RawMessage(`{"inputTokens":100,"outputTokens":10}`), LatencyMs: 600})
	rows, err := db.UsageStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]UsageRow{}
	for _, r := range rows {
		byKey[r.Source+"/"+r.Provider] = r
	}
	if byKey["judgments/jev"].InputTokens != 100 || byKey["summaries/ollama"].Count != 1 || byKey["digests/ollama"].AvgLatencyMs != 5000 {
		t.Fatalf("usage: %+v", rows)
	}
}

func TestLabelsAndEval(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	a, _ := db.InsertAccount(ctx, ForgeGitHub, "me", "")
	yes, no := true, false
	if err := db.PutLabel(ctx, Label{AccountID: a.ID, ThreadID: "t", Category: "needs_my_reply", RequiresAction: &yes, Urgency: 2, Relevance: -1, Priority: 3, Resolved: &no, Note: "n"}); err != nil {
		t.Fatal(err)
	}
	l, err := db.GetLabel(ctx, a.ID, "t")
	if err != nil || l.Category != "needs_my_reply" || l.RequiresAction == nil || !*l.RequiresAction || l.Relevance != -1 || l.Priority != 3 || l.Resolved == nil || *l.Resolved || l.Noise != nil {
		t.Fatalf("label: %+v %v", l, err)
	}
	if m, _ := db.ListLabels(ctx, 0); len(m) != 1 {
		t.Fatalf("list labels: %v", m)
	}
	if err := db.PutPRLabel(ctx, PRLabel{AccountID: a.ID, Repo: "o/r", Number: 5, ImpactLevel: 3, ChangeKind: "api_change"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := db.ListPRLabels(ctx); m[AnalysisKey(a.ID, "o/r", 5)].ImpactLevel != 3 {
		t.Fatalf("pr labels: %v", m)
	}
	if err := db.PutEvalJudgment(ctx, EvalJudgment{AccountID: a.ID, ThreadID: "t", Provider: "ollama", Model: "q", ThreadVersion: "v", AnswersJSON: json.RawMessage(`{"a":1}`), LatencyMs: 7}); err != nil {
		t.Fatal(err)
	}
	m, _ := db.ListEvalJudgments(ctx, "ollama")
	if j := m[JudgmentKey(a.ID, "t")]; j.Model != "q" || string(j.AnswersJSON) != `{"a":1}` {
		t.Fatalf("eval judgments: %+v", m)
	}
	if prov, _ := db.EvalProviders(ctx); prov["ollama"] != 1 {
		t.Fatalf("providers: %v", prov)
	}
	if err := db.DeleteLabel(ctx, a.ID, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetLabel(ctx, a.ID, "t"); err != ErrNotFound {
		t.Fatalf("delete: %v", err)
	}
}
