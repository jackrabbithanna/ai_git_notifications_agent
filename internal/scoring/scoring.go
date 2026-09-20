// Package scoring turns stored judgments into a priority (PLAN.md §4.6). It is
// pure code over cached answers, so changing weights re-ranks instantly.
package scoring

import (
	"math"
	"time"

	"ghinbox/internal/judge"
)

// Weights are user-tunable (Settings sliders). Kind and Relation maps add a
// per-activity-kind / per-relation prior so threads without judgments still order sensibly.
type Weights struct {
	Action    float64            `json:"action"`    // × P(requires_action_from_me)
	Urgency   float64            `json:"urgency"`   // × urgency (0..1)
	Relevance float64            `json:"relevance"` // × relevance (0..1)
	Recency   float64            `json:"recency"`   // × exp(-age / RecencyHalfLifeH)
	Resolved  float64            `json:"resolved"`  // − × P(resolved)
	Kind      map[string]float64 `json:"kind"`
	Relation  map[string]float64 `json:"relation"`
	// RecencyHalfLifeH is the age in hours at which the recency term halves.
	RecencyHalfLifeH float64 `json:"recencyHalfLifeH"`
	// ResolvedThreshold: P(resolved) above this moves the thread to the Resolved bucket.
	ResolvedThreshold float64 `json:"resolvedThreshold"`
	// UnsureBelow: category confidence below this shows an "unsure" badge.
	UnsureBelow float64 `json:"unsureBelow"`
	// UncalibratedDiscount scales judgment terms from uncalibrated providers (Ollama).
	UncalibratedDiscount float64 `json:"uncalibratedDiscount"`
}

// Defaults are a reasonable starting point; M5 tunes them on labeled data.
func Defaults() Weights {
	return Weights{
		Action: 1.0, Urgency: 0.8, Relevance: 0.6, Recency: 0.3, Resolved: 0.8,
		Kind: map[string]float64{
			"review_requested": 0.6, "mention": 0.5, "assignment": 0.5, "blocking_or_failing": 0.6,
			"review": 0.3, "comment": 0.2, "new_pr": 0.15, "new_issue": 0.15, "state_change": 0.1,
			"ci": 0.2, "security": 0.5, "release": 0.05, "discussion": 0.1, "commit": 0.05, "other": 0,
		},
		Relation:             map[string]float64{"author": 0.3, "assignee": 0.3, "reviewer": 0.4, "mentioned": 0.3, "participant": 0.1, "subscriber": 0},
		RecencyHalfLifeH:     48,
		ResolvedThreshold:    0.8,
		UnsureBelow:          0.5,
		UncalibratedDiscount: 0.7,
	}
}

// Inputs are the facts scoring looks at for one thread.
type Inputs struct {
	Kind       string
	Relations  []string
	UpdatedAt  time.Time
	Answers    map[string]judge.Answer // nil when not judged
	Calibrated bool
	IsAuthor   bool
}

// Bucket names.
const (
	BucketNeedsMe  = "needs_me"
	BucketNormal   = "normal"
	BucketResolved = "resolved"
)

// Result is the scored view of a thread.
type Result struct {
	Priority   float64 `json:"priority"`   // raw weighted sum
	Percent    int     `json:"percent"`    // priority normalised to 0..100 for display
	Bucket     string  `json:"bucket"`     // needs_me | normal | resolved
	Pinned     bool    `json:"pinned"`     // hard rule: blocking_or_failing on my own item
	Unsure     bool    `json:"unsure"`     // category confidence below threshold
	Category   string  `json:"category"`   // judged category, "" when not judged
	NextAction string  `json:"nextAction"` // judged next action
	Judged     bool    `json:"judged"`
}

// Score applies the weights to one thread.
func Score(w Weights, in Inputs, now time.Time) Result {
	r := Result{Bucket: BucketNormal}
	p := w.Kind[in.Kind]
	for _, rel := range in.Relations {
		p += w.Relation[rel]
	}
	if !in.UpdatedAt.IsZero() && w.RecencyHalfLifeH > 0 {
		ageH := now.Sub(in.UpdatedAt).Hours()
		if ageH < 0 {
			ageH = 0
		}
		p += w.Recency * math.Exp(-ageH*math.Ln2/w.RecencyHalfLifeH)
	}
	maxP := maxKind(w) + maxRelation(w) + w.Recency

	if in.Answers != nil {
		r.Judged = true
		disc := 1.0
		if !in.Calibrated {
			disc = w.UncalibratedDiscount
		}
		act := in.Answers["requires_action_from_me"].Noul
		urg := in.Answers["urgency"].Normalized()
		rel := in.Answers["relevance"].Normalized()
		res := in.Answers["resolved"].Noul
		p += disc * (w.Action*act + w.Urgency*urg + w.Relevance*rel - w.Resolved*res)
		maxP += w.Action + w.Urgency + w.Relevance
		if cat, ok := in.Answers["category"]; ok {
			r.Category = cat.Choice
			r.Unsure = cat.Confidence < w.UnsureBelow
			if cat.Choice == "blocking_or_failing" && in.IsAuthor {
				r.Pinned = true
			}
			if act >= 0.5 || cat.Choice == "needs_my_review" || cat.Choice == "needs_my_reply" || cat.Choice == "blocking_or_failing" {
				r.Bucket = BucketNeedsMe
			}
		}
		if na, ok := in.Answers["next_action"]; ok {
			r.NextAction = na.Choice
		}
		if res > w.ResolvedThreshold && !r.Pinned {
			r.Bucket = BucketResolved
		}
	}
	r.Priority = p
	if maxP > 0 {
		r.Percent = int(math.Round(100 * clamp01(p/maxP)))
	}
	if r.Pinned {
		r.Percent = 100
	}
	return r
}

func maxKind(w Weights) float64 {
	m := 0.0
	for _, v := range w.Kind {
		if v > m {
			m = v
		}
	}
	return m
}

func maxRelation(w Weights) float64 {
	m := 0.0
	for _, v := range w.Relation {
		if v > m {
			m = v
		}
	}
	return m
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
