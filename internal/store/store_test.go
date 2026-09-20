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
