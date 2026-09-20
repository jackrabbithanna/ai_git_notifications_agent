package eval

import (
	"math/rand"
	"time"

	"gitinbox/internal/scoring"
)

// nowRef anchors recency computations so evaluations are reproducible.
var nowRef = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hours(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

// TuneResult is the outcome of a weight search.
type TuneResult struct {
	Before   scoring.Weights `json:"before"`
	After    scoring.Weights `json:"after"`
	NDCGFrom float64         `json:"ndcgFrom"`
	NDCGTo   float64         `json:"ndcgTo"`
	Iters    int             `json:"iters"`
	Labeled  int             `json:"labeled"` // samples with a priority label
}

// Objective is what the tuner maximises: NDCG@25 of the ranking, with a small
// bonus for the needs-me bucket agreeing with requires-action labels.
func Objective(samples []Sample, w scoring.Weights) float64 {
	r := Evaluate(samples, w, "", "")
	return r.Ranking.NDCG25 + 0.25*r.NeedsMeBucket.F1
}

// Tune runs a seeded coordinate search over the scalar weights and thresholds.
// Kind/relation priors are kept; they are cheap to hand-edit and easy to overfit.
func Tune(samples []Sample, start scoring.Weights, iters int, seed int64) TuneResult {
	res := TuneResult{Before: start, After: start, Iters: iters}
	for _, s := range samples {
		if s.Priority >= 0 {
			res.Labeled++
		}
	}
	res.NDCGFrom = Objective(samples, start)
	if res.Labeled < 5 || iters <= 0 {
		res.NDCGTo = res.NDCGFrom
		return res
	}
	rng := rand.New(rand.NewSource(seed))
	best, bestV := start, res.NDCGFrom
	type dim struct {
		get func(*scoring.Weights) *float64
		lo  float64
		hi  float64
	}
	dims := []dim{
		{func(w *scoring.Weights) *float64 { return &w.Action }, 0, 2},
		{func(w *scoring.Weights) *float64 { return &w.Urgency }, 0, 2},
		{func(w *scoring.Weights) *float64 { return &w.Relevance }, 0, 2},
		{func(w *scoring.Weights) *float64 { return &w.Recency }, 0, 1},
		{func(w *scoring.Weights) *float64 { return &w.Resolved }, 0, 2},
		{func(w *scoring.Weights) *float64 { return &w.Impact }, 0, 2},
		{func(w *scoring.Weights) *float64 { return &w.RecencyHalfLifeH }, 6, 240},
		{func(w *scoring.Weights) *float64 { return &w.ResolvedThreshold }, 0.5, 0.95},
		{func(w *scoring.Weights) *float64 { return &w.UncalibratedDiscount }, 0.3, 1},
	}
	step := 0.25
	for it := 0; it < iters; it++ {
		d := dims[rng.Intn(len(dims))]
		cand := cloneWeights(best)
		v := d.get(&cand)
		span := (d.hi - d.lo) * step
		*v += (rng.Float64()*2 - 1) * span
		if *v < d.lo {
			*v = d.lo
		}
		if *v > d.hi {
			*v = d.hi
		}
		if val := Objective(samples, cand); val > bestV+1e-9 {
			best, bestV = cand, val
		} else if it%50 == 49 && step > 0.05 {
			step *= 0.7 // anneal
		}
	}
	res.After, res.NDCGTo = best, bestV
	return res
}

func cloneWeights(w scoring.Weights) scoring.Weights {
	c := w
	c.Kind = map[string]float64{}
	for k, v := range w.Kind {
		c.Kind[k] = v
	}
	c.Relation = map[string]float64{}
	for k, v := range w.Relation {
		c.Relation[k] = v
	}
	return c
}
