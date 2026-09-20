package judge

import (
	"context"
	"strconv"
	"time"
)

// Fake is a deterministic Judge for tests and dry runs: it answers every
// question with a fixed shape (first option / level 1 / noul 0.5) unless
// Answers overrides a question id.
type Fake struct {
	Answers map[string]Answer
	Calls   int
	Err     error
	Delay   time.Duration
}

func (f *Fake) Name() string     { return "fake" }
func (f *Fake) Calibrated() bool { return false }

func (f *Fake) Ask(ctx context.Context, state any, questions []Question) (Response, error) {
	f.Calls++
	if f.Err != nil {
		return Response{}, f.Err
	}
	if f.Delay > 0 {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(f.Delay):
		}
	}
	out := Response{Provider: "fake", Model: "fake", Answers: map[string]Answer{}}
	for _, q := range questions {
		if a, ok := f.Answers[q.ID]; ok {
			out.Answers[q.ID] = a
			continue
		}
		a := Answer{Kind: q.Kind}
		switch q.Kind {
		case Noul:
			a.Noul = 0.5
		case Choice:
			keys := make([]string, 0, len(q.Options))
			for _, o := range q.Options {
				keys = append(keys, o.ID)
			}
			a.Probabilities, a.Choice = NormalizeProbabilities(map[string]float64{keys[0]: 1}, keys)
			a.Confidence = 1
		case Score:
			keys := make([]string, len(q.Levels))
			for i := range q.Levels {
				keys[i] = strconv.Itoa(i)
			}
			a.Probabilities, _ = NormalizeProbabilities(map[string]float64{"1": 1}, keys)
			a.Score = 1
			a.Legend = append([]string(nil), q.Levels...)
			a.Confidence = 1
		}
		out.Answers[q.ID] = a
	}
	return out, nil
}

var _ Judge = (*Fake)(nil)
