package store

import (
	"context"
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
	a, err := db.InsertAccount(ctx, "octocat", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Host != "github.com" || a.WriteMode != "readonly" {
		t.Fatalf("defaults: %+v", a)
	}
	if _, err := db.InsertAccount(ctx, "octocat", ""); err == nil {
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
	a, _ := db.InsertAccount(ctx, "octocat", "")
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
	a, _ := db.InsertAccount(ctx, "octocat", "")
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
