package scoring

import (
	"testing"
	"time"

	"ghinbox/internal/judge"
)

func answers(action, resolved float64, cat string, conf float64, urg, rel float64) map[string]judge.Answer {
	return map[string]judge.Answer{
		"requires_action_from_me": {Kind: judge.Noul, Noul: action},
		"resolved":                {Kind: judge.Noul, Noul: resolved},
		"category":                {Kind: judge.Choice, Choice: cat, Confidence: conf},
		"urgency":                 {Kind: judge.Score, Score: urg * 3, Legend: []string{"a", "b", "c", "d"}},
		"relevance":               {Kind: judge.Score, Score: rel * 2, Legend: []string{"a", "b", "c"}},
		"next_action":             {Kind: judge.Choice, Choice: "reply"},
	}
}

func TestScoreOrdering(t *testing.T) {
	w := Defaults()
	now := time.Now()
	unjudged := Score(w, Inputs{Kind: "comment", Relations: []string{"subscriber"}, UpdatedAt: now}, now)
	needsMe := Score(w, Inputs{Kind: "mention", Relations: []string{"mentioned"}, UpdatedAt: now, Answers: answers(0.9, 0.05, "needs_my_reply", 0.8, 0.7, 1), Calibrated: true}, now)
	fyi := Score(w, Inputs{Kind: "comment", Relations: []string{"subscriber"}, UpdatedAt: now, Answers: answers(0.1, 0.1, "fyi_progress", 0.9, 0, 0.5), Calibrated: true}, now)
	resolved := Score(w, Inputs{Kind: "state_change", UpdatedAt: now, Answers: answers(0.1, 0.95, "resolved_no_action", 0.9, 0, 0), Calibrated: true}, now)
	if !(needsMe.Priority > fyi.Priority && fyi.Priority > resolved.Priority) {
		t.Fatalf("ordering: needsMe=%.2f fyi=%.2f resolved=%.2f", needsMe.Priority, fyi.Priority, resolved.Priority)
	}
	if needsMe.Bucket != BucketNeedsMe || fyi.Bucket != BucketNormal || resolved.Bucket != BucketResolved || unjudged.Bucket != BucketNormal {
		t.Fatalf("buckets: %s %s %s %s", needsMe.Bucket, fyi.Bucket, resolved.Bucket, unjudged.Bucket)
	}
	if needsMe.Percent <= fyi.Percent || needsMe.Percent > 100 || unjudged.Judged || !needsMe.Judged || needsMe.NextAction != "reply" {
		t.Fatalf("percent/judged: %+v %+v", needsMe, unjudged)
	}
}

func TestHardRulesAndDiscount(t *testing.T) {
	w := Defaults()
	now := time.Now()
	pinned := Score(w, Inputs{Kind: "ci", Relations: []string{"author"}, UpdatedAt: now, IsAuthor: true, Answers: answers(0.9, 0.9, "blocking_or_failing", 0.9, 1, 1), Calibrated: true}, now)
	if !pinned.Pinned || pinned.Percent != 100 || pinned.Bucket == BucketResolved {
		t.Fatalf("pinned rule: %+v", pinned)
	}
	unsure := Score(w, Inputs{Kind: "comment", UpdatedAt: now, Answers: answers(0.5, 0.1, "fyi_progress", 0.3, 0.2, 0.2), Calibrated: true}, now)
	if !unsure.Unsure {
		t.Fatalf("unsure badge: %+v", unsure)
	}
	cal := Score(w, Inputs{Kind: "comment", UpdatedAt: now, Answers: answers(0.9, 0, "needs_my_reply", 0.9, 0.5, 0.5), Calibrated: true}, now)
	uncal := Score(w, Inputs{Kind: "comment", UpdatedAt: now, Answers: answers(0.9, 0, "needs_my_reply", 0.9, 0.5, 0.5), Calibrated: false}, now)
	if uncal.Priority >= cal.Priority {
		t.Fatalf("uncalibrated judgments must be discounted: %.2f vs %.2f", uncal.Priority, cal.Priority)
	}
	// Monotonic in the action weight.
	w2 := w
	w2.Action = 2
	if Score(w2, Inputs{Kind: "comment", UpdatedAt: now, Answers: answers(0.9, 0, "x", 0.9, 0, 0), Calibrated: true}, now).Priority <= cal.Priority {
		t.Fatal("raising the action weight must raise priority")
	}
	old := Score(w, Inputs{Kind: "comment", UpdatedAt: now.Add(-30 * 24 * time.Hour)}, now)
	fresh := Score(w, Inputs{Kind: "comment", UpdatedAt: now}, now)
	if old.Priority >= fresh.Priority {
		t.Fatal("recency decay")
	}
}
