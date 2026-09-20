package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"gitinbox/internal/judge"
	"gitinbox/internal/store"
)

func TestEvalFlow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fs := &fakeSource{threads: []store.Thread{
		{ThreadID: "a", Repo: "o/r", SubjectType: "Issue", SubjectNumber: 1, Title: "A needs reply", Reason: "mention", Unread: true, UpdatedAt: now, ActivityKind: "mention", RelationTags: []string{"mentioned"}},
		{ThreadID: "b", Repo: "o/r", SubjectType: "PullRequest", SubjectNumber: 2, Title: "B fyi", Reason: "subscribed", Unread: true, UpdatedAt: now.Add(-2 * time.Hour), ActivityKind: "new_pr", RelationTags: []string{"subscriber"}},
		{ThreadID: "c", Repo: "o/r", SubjectType: "PullRequest", SubjectNumber: 3, Title: "Bump dep from 1 to 2", Reason: "subscribed", Unread: true, UpdatedAt: now.Add(-3 * time.Hour), ActivityKind: "new_pr", RelationTags: []string{"subscriber"}},
	}}
	p, acct := newTestPipeline(t, fs)
	ctx := context.Background()
	if _, err := p.SyncAccount(ctx, acct, true); err != nil {
		t.Fatal(err)
	}
	// Primary judge: "a" needs a reply, "b" is fyi.
	primary := &judge.Fake{Answers: map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: 0.8},
		"category":                {Kind: judge.Choice, Choice: "needs_my_reply", Probabilities: map[string]float64{"needs_my_reply": 0.7, "fyi_progress": 0.3}, Confidence: 0.7},
	}}
	p.judgeOverride = primary
	if _, err := p.JudgeAccount(ctx, acct, 0); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	// The queue includes noise-filtered threads (they need labels too), judged ones first.
	queue, err := p.LabelQueue(ctx, acct.ID, 10, false)
	if err != nil || len(queue) != 3 || !queue[0].Score.Judged || queue[0].Answers == nil || queue[2].Thread.ThreadID != "c" {
		t.Fatalf("queue: %d %v", len(queue), err)
	}
	if err := p.SetLabel(ctx, store.Label{AccountID: acct.ID, ThreadID: "a", Category: "needs_my_reply", RequiresAction: &yes, Urgency: 2, Relevance: 2, Priority: 3, Resolved: &no, Noise: &no}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLabel(ctx, store.Label{AccountID: acct.ID, ThreadID: "b", Category: "fyi_progress", RequiresAction: &no, Urgency: 0, Relevance: -1, Priority: 1, Resolved: &no, Noise: &no}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLabel(ctx, store.Label{AccountID: acct.ID, ThreadID: "c", Urgency: -1, Relevance: -1, Priority: 0, Noise: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetLabel(ctx, store.Label{AccountID: acct.ID, ThreadID: "a", Category: "nope", Urgency: -1, Relevance: -1, Priority: -1}); err == nil {
		t.Fatal("invalid category must be rejected")
	}
	queue, _ = p.LabelQueue(ctx, acct.ID, 10, false)
	if len(queue) != 0 {
		t.Fatalf("all labeled; queue should be empty, got %d", len(queue))
	}
	ov, _ := p.EvalOverview(ctx)
	if ov.Labels != 3 || ov.Primary != 2 {
		t.Fatalf("overview: %+v", ov)
	}
	r, err := p.Evaluate(ctx, PrimaryProvider)
	if err != nil || r.Samples != 3 || r.Judged != 2 {
		t.Fatalf("evaluate: %+v %v", r, err)
	}
	// "b" is labeled fyi but judged needs_my_reply → 50% category accuracy; "c" is noise the filter caught.
	if r.Category.N != 2 || r.Category.Accuracy != 0.5 || r.Filter.TP != 1 || len(r.FilterMissed) != 0 || r.Ranking.N != 3 {
		t.Fatalf("metrics: cat=%+v filter=%+v rank=%+v", r.Category, r.Filter, r.Ranking)
	}
	// Eval-judge with another provider (override path) then evaluate it.
	p.judgeOverride = &judge.Fake{Answers: map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: 0.2},
		"category":                {Kind: judge.Choice, Choice: "fyi_progress", Probabilities: map[string]float64{"fyi_progress": 0.9}, Confidence: 0.9},
	}}
	rep, err := p.EvalJudge(ctx, "fake", "", false)
	if err != nil || rep.Judged != 3 || rep.Skipped != 0 {
		t.Fatalf("eval judge: %+v %v", rep, err)
	}
	rep, _ = p.EvalJudge(ctx, "fake", "", false)
	if rep.Skipped != 3 {
		t.Fatalf("second eval judge must skip: %+v", rep)
	}
	r2, err := p.Evaluate(ctx, "fake")
	if err != nil || r2.Judged != 3 || r2.Category.Accuracy != 0.5 || r2.RequiresAction.FN != 1 {
		t.Fatalf("evaluate fake: %+v %v", r2, err)
	}
	// Tune runs and never makes things worse.
	res, err := p.Tune(ctx, 50, false)
	if err != nil || res.Labeled != 3 || res.NDCGTo < res.NDCGFrom {
		t.Fatalf("tune: %+v %v", res, err)
	}
	// Export / import round trip.
	var buf bytes.Buffer
	n, err := p.ExportLabels(ctx, &buf)
	if err != nil || n != 3 || !strings.Contains(buf.String(), `"thread_id":"a"`) {
		t.Fatalf("export: %d %v %s", n, err, buf.String())
	}
	_ = p.deps.DB.DeleteLabel(ctx, acct.ID, "a")
	if m, err := p.ImportLabels(ctx, strings.NewReader(buf.String())); err != nil || m != 3 {
		t.Fatalf("import: %d %v", m, err)
	}
	if l, err := p.deps.DB.GetLabel(ctx, acct.ID, "a"); err != nil || l.Priority != 3 || l.RequiresAction == nil || !*l.RequiresAction {
		t.Fatalf("round trip: %+v %v", l, err)
	}
	if err := p.SetPRLabel(ctx, store.PRLabel{AccountID: acct.ID, Repo: "o/r", Number: 2, ImpactLevel: 1, ChangeKind: "bugfix"}); err != nil {
		t.Fatal(err)
	}
	md, err := p.ReportMarkdown(ctx)
	if err != nil || !strings.Contains(md, "| primary |") || !strings.Contains(md, "| fake |") || !strings.Contains(md, "Category confusion") || !strings.Contains(md, "Current weights") {
		t.Fatalf("report: %v\n%s", err, md)
	}
}
