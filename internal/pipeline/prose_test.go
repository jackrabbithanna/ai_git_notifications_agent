package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ghinbox/internal/judge"
	"ghinbox/internal/llm"
	"ghinbox/internal/store"
)

// proseGen returns a canned summary or digest depending on the schema requested,
// and records whether a previous_summary was in the prompt.
type proseGen struct {
	calls        int
	sawPrevious  bool
	lastUserJSON string
}

func (g *proseGen) Name() string { return "fake" }
func (g *proseGen) Generate(_ context.Context, _ string, user string, schema map[string]any) (llm.Result, error) {
	g.calls++
	g.lastUserJSON = user
	g.sawPrevious = strings.Contains(user, "previous_summary")
	props := schema["properties"].(map[string]any)
	if _, ok := props["headline"]; ok {
		return llm.Result{Provider: "fake", Model: "fake-digest", JSON: json.RawMessage(`{"headline":"Two things need you","sections":[{"title":"Needs you","items":[{"ref":"o/r#1","title":"A","why":"mentioned you"}]}],"suggested_actions":["reply to A"]}`)}, nil
	}
	return llm.Result{Provider: "fake", Model: "fake-sum", JSON: json.RawMessage(`{"summary":"Thread about A.","key_points":["p1"],"asks_of_me":["reply"],"changed_since_last_read":"` + map[bool]string{true: "new comment", false: ""}[g.sawPrevious] + `"}`),
		Usage: llm.Usage{InputTokens: 50, OutputTokens: 20}, Latency: 10 * time.Millisecond}, nil
}

func TestProseFlow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fs := &fakeSource{threads: []store.Thread{
		{ThreadID: "a", Repo: "o/r", SubjectType: "Issue", SubjectNumber: 1, Title: "A", Reason: "mention", Unread: true, UpdatedAt: now, ActivityKind: "mention", RelationTags: []string{"mentioned"}},
		{ThreadID: "b", Repo: "o/r", SubjectType: "Issue", SubjectNumber: 2, Title: "B", Reason: "subscribed", Unread: true, UpdatedAt: now.Add(-time.Hour), ActivityKind: "comment", RelationTags: []string{"subscriber"}},
	}}
	p, acct := newTestPipeline(t, fs)
	ctx := context.Background()
	if _, err := p.SyncAccount(ctx, acct, true); err != nil {
		t.Fatal(err)
	}
	gen := &proseGen{}
	p.generatorOverride = gen
	var notified []Notification
	p.deps.Notify = func(n Notification) { notified = append(notified, n) }

	// Summarize: generate, then reuse for the same version.
	v, err := p.Summarize(ctx, acct, "a", false)
	if err != nil || v.Content.Summary == "" || v.Content.AsksOfMe[0] != "reply" || gen.calls != 1 || v.Summary.Model != "fake-sum" {
		t.Fatalf("summarize: %+v %v calls=%d", v, err, gen.calls)
	}
	if _, err := p.Summarize(ctx, acct, "a", false); err != nil || gen.calls != 1 {
		t.Fatalf("reuse: calls=%d err=%v", gen.calls, err)
	}
	sv, err := p.Summary(ctx, acct, "a")
	if err != nil || sv.Stale {
		t.Fatalf("summary view: %+v %v", sv, err)
	}
	// New activity → stale → regenerated with the previous summary in the prompt.
	fs.threads[0].UpdatedAt = now.Add(time.Hour)
	_, _ = p.SyncAccount(ctx, acct, false)
	if sv, _ := p.Summary(ctx, acct, "a"); !sv.Stale {
		t.Fatal("summary must be stale after activity")
	}
	v, err = p.Summarize(ctx, acct, "a", false)
	if err != nil || gen.calls != 2 || !gen.sawPrevious || v.Content.ChangedSinceLastRead != "new comment" {
		t.Fatalf("delta: %+v calls=%d prev=%v", v.Content, gen.calls, gen.sawPrevious)
	}
	if !strings.Contains(gen.lastUserJSON, `"login": "me"`) {
		t.Fatalf("state must carry me.login: %s", gen.lastUserJSON[:200])
	}
	// SummarizeTop: only "b" lacks a current summary.
	n, err := p.SummarizeTop(ctx, 5)
	if err != nil || n != 1 || gen.calls != 3 {
		t.Fatalf("top: n=%d calls=%d err=%v", n, gen.calls, err)
	}
	// Digest over the period.
	dv, err := p.GenerateDigest(ctx, now.Add(-24*time.Hour))
	if err != nil || dv.Content.Headline == "" || dv.Digest.ThreadCount != 2 || dv.Digest.ID == 0 || !strings.Contains(gen.lastUserJSON, `"o/r#1"`) {
		t.Fatalf("digest: %+v %v", dv, err)
	}
	if ds, _ := p.Digests(ctx, 10); len(ds) != 1 || ds[0].Content.Sections[0].Items[0].Ref != "o/r#1" {
		t.Fatalf("digests: %+v", ds)
	}
	// Notifications: judge "a" into needs_me, then notify once per version.
	p.judgeOverride = &judge.Fake{Answers: map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: 0.9},
		"category":                {Kind: judge.Choice, Choice: "needs_my_reply", Probabilities: map[string]float64{"needs_my_reply": 0.9}, Confidence: 0.9},
	}}
	if _, err := p.JudgeAccount(ctx, acct, 0); err != nil {
		t.Fatal(err)
	}
	sent, err := p.NotifyNew(ctx)
	if err != nil || sent != 2 || len(notified) != 2 || !strings.Contains(notified[0].Title, "needs my reply") {
		t.Fatalf("notify: sent=%d %+v err=%v", sent, notified, err)
	}
	if sent, _ := p.NotifyNew(ctx); sent != 0 {
		t.Fatalf("notifications must not repeat: %d", sent)
	}
	rows, _ := p.deps.DB.UsageStats(ctx)
	var sums, digs bool
	for _, r := range rows {
		if r.Source == "summaries" && r.Count == 2 && r.InputTokens == 100 {
			sums = true
		}
		if r.Source == "digests" && r.Count == 1 {
			digs = true
		}
	}
	if !sums || !digs {
		t.Fatalf("usage rows: %+v", rows)
	}
}
