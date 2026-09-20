package eval

import (
	"math"
	"testing"

	"gitinbox/internal/judge"
	"gitinbox/internal/scoring"
)

func bp(b bool) *bool { return &b }

func answers(action, resolved float64, cat string, conf, urg, rel float64) map[string]judge.Answer {
	return map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: action},
		"resolved":                {Kind: judge.Noul, Noul: resolved},
		"category":                {Kind: judge.Choice, Choice: cat, Confidence: conf},
		"urgency":                 {Kind: judge.Score, Score: urg, Legend: []string{"a", "b", "c", "d"}},
		"relevance":               {Kind: judge.Score, Score: rel, Legend: []string{"a", "b", "c"}},
	}
}

func TestNDCGAndSpearman(t *testing.T) {
	if v := NDCG([]int{3, 2, 1, 0}, 0); math.Abs(v-1) > 1e-9 {
		t.Fatalf("perfect order NDCG = %v", v)
	}
	if v := NDCG([]int{0, 1, 2, 3}, 0); v >= 1 || v <= 0 {
		t.Fatalf("reversed NDCG = %v", v)
	}
	if NDCG([]int{0, 0}, 0) != 0 || NDCG(nil, 5) != 0 {
		t.Fatal("degenerate NDCG")
	}
	if s := spearman([]float64{3, 2, 1}, []int{3, 2, 1}); math.Abs(s-1) > 1e-9 {
		t.Fatalf("spearman perfect = %v", s)
	}
	if s := spearman([]float64{1, 2, 3}, []int{3, 2, 1}); math.Abs(s+1) > 1e-9 {
		t.Fatalf("spearman reversed = %v", s)
	}
}

func TestEvaluateMetrics(t *testing.T) {
	samples := []Sample{
		{Key: "1", Title: "A", Kind: "mention", Calibrated: true, Answers: answers(0.9, 0.1, "needs_my_reply", 0.9, 2.1, 2), Category: "needs_my_reply", RequiresAction: bp(true), Urgency: 2, Relevance: 2, Priority: 3, Resolved: bp(false), Noise: bp(false), Filter: "keep"},
		{Key: "2", Title: "B", Kind: "comment", Calibrated: true, Answers: answers(0.2, 0.2, "fyi_progress", 0.8, 0.2, 1), Category: "fyi_progress", RequiresAction: bp(false), Urgency: 0, Relevance: 1, Priority: 1, Resolved: bp(false), Noise: bp(false), Filter: "keep"},
		{Key: "3", Title: "C", Kind: "state_change", Calibrated: true, Answers: answers(0.1, 0.95, "resolved_no_action", 0.9, 0, 0), Category: "resolved_no_action", RequiresAction: bp(false), Urgency: 0, Relevance: 0, Priority: 0, Resolved: bp(true), Noise: bp(false), Filter: "keep"},
		{Key: "4", Title: "D bot bump", Kind: "new_pr", Calibrated: true, Answers: answers(0.6, 0.1, "needs_my_review", 0.4, 1, 1), Category: "fyi_progress", RequiresAction: bp(false), Urgency: 1, Relevance: 1, Priority: 0, Resolved: bp(false), Noise: bp(true), Filter: "keep"},
		{Key: "5", Title: "E unjudged", Kind: "comment", Priority: 2, Noise: bp(false), Filter: "keep"},
	}
	r := Evaluate(samples, scoring.Defaults(), "fake", "m")
	if r.Samples != 5 || r.Judged != 4 {
		t.Fatalf("counts: %+v", r)
	}
	if r.Category.N != 4 || math.Abs(r.Category.Accuracy-0.75) > 1e-9 || r.Category.Unsure != 1 || len(r.Category.Mistakes) != 1 || r.Category.Mistakes[0].Predicted != "needs_my_review" {
		t.Fatalf("category: %+v", r.Category)
	}
	if r.RequiresAction.N != 4 || r.RequiresAction.TP != 1 || r.RequiresAction.FP != 1 || r.RequiresAction.Recall != 1 || r.RequiresAction.Brier <= 0 {
		t.Fatalf("requires action: %+v", r.RequiresAction)
	}
	if r.Resolved.TP != 1 || r.Resolved.FP != 0 || r.Resolved.F1 != 1 {
		t.Fatalf("resolved: %+v", r.Resolved)
	}
	if r.Urgency.N != 4 || r.Urgency.Exact < 0.99 || r.Relevance.Exact < 0.99 {
		t.Fatalf("ordinal: %+v %+v", r.Urgency, r.Relevance)
	}
	if r.Filter.N != 5 || r.Filter.FN != 1 || len(r.FilterMissed) != 1 || r.FilterMissed[0].Title != "D bot bump" {
		t.Fatalf("filter: %+v missed=%+v", r.Filter, r.FilterMissed)
	}
	if r.Ranking.N != 5 || r.Ranking.NDCG25 <= 0.5 || r.NeedsMeBucket.N != 4 {
		t.Fatalf("ranking: %+v bucket=%+v", r.Ranking, r.NeedsMeBucket)
	}
}

func TestTuneImprovesOrKeeps(t *testing.T) {
	var samples []Sample
	for i := 0; i < 12; i++ {
		action := 0.1
		prio := 0
		cat := "fyi_progress"
		if i%3 == 0 {
			action, prio, cat = 0.9, 3, "needs_my_reply"
		}
		// Urgency/relevance are deliberately uninformative so only the action weight can fix the ranking.
		samples = append(samples, Sample{Key: string(rune('a' + i)), Kind: "comment", Calibrated: true, Answers: answers(action, 0.1, cat, 0.9, 1, 1), Category: cat, RequiresAction: bp(prio == 3), Priority: prio, UpdatedAgeH: float64(i)})
	}
	start := scoring.Defaults()
	start.Action = 0 // deliberately bad
	res := Tune(samples, start, 300, 1)
	if res.Labeled != 12 || res.NDCGTo <= res.NDCGFrom || res.After.Action <= 0 {
		t.Fatalf("tune: from=%.3f to=%.3f action=%.2f", res.NDCGFrom, res.NDCGTo, res.After.Action)
	}
	// Tuning never changes the untouched priors.
	if len(res.After.Kind) != len(start.Kind) {
		t.Fatal("kind priors must be preserved")
	}
}
