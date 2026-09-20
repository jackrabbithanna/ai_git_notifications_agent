package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ghinbox/internal/judge"
	"ghinbox/internal/secrets"
	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// fakeSource is a Source+Enricher with canned threads.
type fakeSource struct {
	threads   []store.Thread
	enriched  int
	enrichErr error
}

func (f *fakeSource) Forge() string                              { return store.ForgeGitHub }
func (f *fakeSource) Login(context.Context) (string, error)      { return "me", nil }
func (f *fakeSource) Mine(context.Context) ([]store.Item, error) { return nil, nil }
func (f *fakeSource) MarkRead(context.Context, string) error     { return nil }
func (f *fakeSource) MarkDone(context.Context, string) error     { return nil }
func (f *fakeSource) Unsubscribe(context.Context, string) error  { return nil }
func (f *fakeSource) Close() error                               { return nil }
func (f *fakeSource) WriteMode() string                          { return "readonly" }
func (f *fakeSource) Notifications(_ context.Context, o source.Opts) (source.Result, error) {
	var out []store.Thread
	for _, t := range f.threads {
		t.AccountID = o.AccountID
		out = append(out, t)
	}
	return source.Result{Threads: out}, nil
}
func (f *fakeSource) Enrich(_ context.Context, t store.Thread) (store.Enrichment, error) {
	if f.enrichErr != nil {
		return store.Enrichment{}, f.enrichErr
	}
	f.enriched++
	at := t.UpdatedAt
	return store.Enrichment{ItemAuthor: "alice", ItemState: "open", ItemBody: "body of " + t.ThreadID, LatestAuthor: "bob", LatestBody: "hey @me can you look?", LatestAt: &at}, nil
}

func newTestPipeline(t *testing.T, fs *fakeSource) (*Pipeline, store.Account) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec := secrets.OpenFile(filepath.Join(t.TempDir(), "s.json"))
	p := New(Deps{DB: db, Secrets: sec, Logger: slog.New(slog.NewTextHandler(nil, nil))})
	p.deps.Logger = slog.Default()
	acct, err := db.InsertAccount(context.Background(), store.ForgeGitHub, "me", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sec.Set(secrets.AccountKey(acct.ID), "tok"); err != nil {
		t.Fatal(err)
	}
	p.sources[acct.ID] = fs
	return p, acct
}

func TestJudgeAccountFlow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fs := &fakeSource{threads: []store.Thread{
		{ThreadID: "a", Repo: "o/r", SubjectType: "Issue", SubjectNumber: 1, Title: "A", Reason: "mention", Unread: true, UpdatedAt: now, ActivityKind: "mention", RelationTags: []string{"mentioned"}, LatestCommentURL: "https://api.github.com/repos/o/r/issues/comments/9"},
		{ThreadID: "b", Repo: "o/r", SubjectType: "PullRequest", SubjectNumber: 2, Title: "Bump lodash from 1 to 2", Reason: "subscribed", Unread: true, UpdatedAt: now, ActivityKind: "new_pr", RelationTags: []string{"subscriber"}},
	}}
	p, acct := newTestPipeline(t, fs)
	ctx := context.Background()
	if _, err := p.SyncAccount(ctx, acct, true); err != nil {
		t.Fatal(err)
	}
	// No provider configured → ErrJudgeOff, nothing judged.
	if _, err := p.JudgeAccount(ctx, acct, 0); !errors.Is(err, ErrJudgeOff) {
		t.Fatalf("expected ErrJudgeOff, got %v", err)
	}
	// Inject a fake judge by overriding settings to ollama with a model, then swapping the constructor.
	fake := &judge.Fake{Answers: map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: 0.9},
		"category":                {Kind: judge.Choice, Choice: "needs_my_reply", Probabilities: map[string]float64{"needs_my_reply": 0.8, "fyi_progress": 0.2}, Confidence: 0.8},
	}}
	p.judgeOverride = fake
	rep, err := p.JudgeAccount(ctx, acct, 0)
	if err != nil {
		t.Fatalf("judge: %v (%+v)", err, rep)
	}
	// "b" is bot noise → filtered out of candidates; only "a" is judged and enriched.
	if rep.Candidates != 1 || rep.Judged != 1 || rep.Enriched != 1 || rep.Failed != 0 || fake.Calls != 1 || fs.enriched != 1 {
		t.Fatalf("report: %+v calls=%d enriched=%d", rep, fake.Calls, fs.enriched)
	}
	th, _ := p.deps.DB.GetThread(ctx, acct.ID, "a")
	if th.ItemAuthor != "alice" || th.LatestBody == "" || th.EnrichedVersion != th.Version() {
		t.Fatalf("enrichment not stored: %+v", th.Enrichment)
	}
	ex, err := p.Explain(ctx, acct, "a", false)
	if err != nil || ex.Judgment == nil || ex.Stale || ex.Answers["category"].Choice != "needs_my_reply" {
		t.Fatalf("explain: %+v %v", ex, err)
	}
	if ex.Score.Bucket != "needs_me" || !ex.Score.Judged || ex.State.LatestActivity == nil || ex.State.LatestActivity.Author != "bob" || ex.State.Me.Login != "me" {
		t.Fatalf("score/state: %+v %+v", ex.Score, ex.State)
	}
	// Second run: nothing pending.
	rep, _ = p.JudgeAccount(ctx, acct, 0)
	if rep.Candidates != 0 || fake.Calls != 1 {
		t.Fatalf("second run should be idle: %+v calls=%d", rep, fake.Calls)
	}
	// New activity on "a" → stale → re-enriched and re-judged.
	fs.threads[0].UpdatedAt = now.Add(time.Hour)
	if _, err := p.SyncAccount(ctx, acct, false); err != nil {
		t.Fatal(err)
	}
	rep, _ = p.JudgeAccount(ctx, acct, 0)
	if rep.Judged != 1 || fake.Calls != 2 || fs.enriched != 2 {
		t.Fatalf("re-judge after activity: %+v calls=%d enriched=%d", rep, fake.Calls, fs.enriched)
	}
	// Scored inbox: judged thread carries a category and percent.
	threads, _ := p.deps.DB.ListThreads(ctx, store.ThreadQuery{AccountID: acct.ID})
	scored, err := p.ScoreThreads(ctx, threads)
	if err != nil || len(scored) != 1 || scored[0].Score.Category != "needs_my_reply" || scored[0].Score.Percent == 0 {
		t.Fatalf("scored: %+v %v", scored, err)
	}
	// Fatal provider errors abort the run and surface.
	fs.threads[0].UpdatedAt = now.Add(2 * time.Hour)
	_, _ = p.SyncAccount(ctx, acct, false)
	fake.Err = judge.ErrUnauthorized
	rep, err = p.JudgeAccount(ctx, acct, 0)
	if !errors.Is(err, judge.ErrUnauthorized) || rep.Failed != 1 {
		t.Fatalf("fatal: %v %+v", err, rep)
	}
}

func TestBuildTriageState(t *testing.T) {
	acct := store.Account{Login: "me", Forge: "github"}
	at := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	th := store.Thread{Repo: "o/r", SubjectType: "PullRequest", SubjectNumber: 7, Title: "T", ActivityKind: "review", Reason: "review_requested", RelationTags: []string{"reviewer"}, UpdatedAt: at,
		Enrichment: store.Enrichment{ItemAuthor: "ME", ItemState: "open", ItemLabels: []string{"x"}, ItemBody: "b", LatestAuthor: "bob", LatestBody: "please", LatestAt: &at}}
	st := BuildTriageState(acct, th, "")
	if !st.Me.IsAuthor || st.Thread.Number != 7 || st.LatestActivity.Body != "please" || st.Profile.Interests == "" || st.Thread.UpdatedAt != "2026-09-19T10:00:00Z" {
		t.Fatalf("state: %+v", st)
	}
}
