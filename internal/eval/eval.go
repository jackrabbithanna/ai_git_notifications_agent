// Package eval measures judgments and the app's ranking against user labels
// (PLAN.md M5) and tunes scoring weights from the same data. Pure functions;
// the pipeline assembles Samples from the store.
package eval

import (
	"math"
	"sort"

	"gitinbox/internal/judge"
	"gitinbox/internal/scoring"
)

// Sample is one labeled thread together with a provider's answers and the score
// the app would give under the weights being evaluated.
type Sample struct {
	Key         string // account:thread
	Title       string
	Repo        string
	Kind        string
	Relations   []string
	IsAuthor    bool
	Calibrated  bool
	Answers     map[string]judge.Answer // nil when this provider has no judgment
	Filter      string                  // keep | noise (the app's verdict)
	ImpactLevel int                     // -1 none

	// Labels (−1 / nil = unset).
	Category       string
	RequiresAction *bool
	Urgency        int
	Relevance      int
	Priority       int
	Resolved       *bool
	Noise          *bool

	UpdatedAgeH float64 // hours since the thread's last activity (for recency)
}

// Binary summarises a yes/no judgment against labels.
type Binary struct {
	N         int     `json:"n"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
	TN        int     `json:"tn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Accuracy  float64 `json:"accuracy"`
	Brier     float64 `json:"brier"` // mean squared error of the probability; lower is better, 0.25 = coin flip
}

func binary(pairs []probLabel, threshold float64) Binary {
	var b Binary
	var brier float64
	for _, p := range pairs {
		b.N++
		pred := p.p >= threshold
		switch {
		case pred && p.y:
			b.TP++
		case pred && !p.y:
			b.FP++
		case !pred && p.y:
			b.FN++
		default:
			b.TN++
		}
		y := 0.0
		if p.y {
			y = 1
		}
		brier += (p.p - y) * (p.p - y)
	}
	if b.N == 0 {
		return b
	}
	if b.TP+b.FP > 0 {
		b.Precision = float64(b.TP) / float64(b.TP+b.FP)
	}
	if b.TP+b.FN > 0 {
		b.Recall = float64(b.TP) / float64(b.TP+b.FN)
	}
	if b.Precision+b.Recall > 0 {
		b.F1 = 2 * b.Precision * b.Recall / (b.Precision + b.Recall)
	}
	b.Accuracy = float64(b.TP+b.TN) / float64(b.N)
	b.Brier = brier / float64(b.N)
	return b
}

type probLabel struct {
	p float64
	y bool
}

// Ordinal summarises a Score-type judgment against level labels.
type Ordinal struct {
	N         int     `json:"n"`
	Exact     float64 `json:"exact"`     // share where round(score) == label
	WithinOne float64 `json:"withinOne"` // share where |round(score) − label| ≤ 1
	MAE       float64 `json:"mae"`       // mean |score − label| on the level index
}

func ordinal(pairs [][2]float64) Ordinal {
	var o Ordinal
	var exact, within, mae float64
	for _, p := range pairs {
		o.N++
		d := math.Abs(p[0] - p[1])
		mae += d
		if math.Round(p[0]) == p[1] {
			exact++
		}
		if math.Abs(math.Round(p[0])-p[1]) <= 1 {
			within++
		}
	}
	if o.N > 0 {
		o.Exact, o.WithinOne, o.MAE = exact/float64(o.N), within/float64(o.N), mae/float64(o.N)
	}
	return o
}

// Categorical summarises a Choice judgment.
type Categorical struct {
	N         int                       `json:"n"`
	Accuracy  float64                   `json:"accuracy"`
	Confusion map[string]map[string]int `json:"confusion"` // label → predicted → count
	Unsure    int                       `json:"unsure"`    // predictions below the confidence threshold
	AccSure   float64                   `json:"accSure"`   // accuracy among confident predictions
	AccUnsure float64                   `json:"accUnsure"` // accuracy among unsure predictions
	Mistakes  []Mistake                 `json:"mistakes"`
}

// Mistake is one wrong categorical prediction, for inspection.
type Mistake struct {
	Key        string  `json:"key"`
	Title      string  `json:"title"`
	Label      string  `json:"label"`
	Predicted  string  `json:"predicted"`
	Confidence float64 `json:"confidence"`
}

// Ranking compares the app's priority order with the labeled priorities.
type Ranking struct {
	N        int     `json:"n"`
	NDCG10   float64 `json:"ndcg10"`
	NDCG25   float64 `json:"ndcg25"`
	NDCG     float64 `json:"ndcg"`     // full list
	Spearman float64 `json:"spearman"` // rank correlation between priority and label
}

// Report is the evaluation of one provider under one weight set.
type Report struct {
	Provider       string      `json:"provider"`
	Model          string      `json:"model"`
	Samples        int         `json:"samples"`
	Judged         int         `json:"judged"`
	Category       Categorical `json:"category"`
	RequiresAction Binary      `json:"requiresAction"`
	Resolved       Binary      `json:"resolved"`
	Urgency        Ordinal     `json:"urgency"`
	Relevance      Ordinal     `json:"relevance"`
	Ranking        Ranking     `json:"ranking"`
	Filter         Binary      `json:"filter"` // app verdict "noise" vs noise labels
	FilterMissed   []Mistake   `json:"filterMissed"`
	NeedsMeBucket  Binary      `json:"needsMeBucket"` // bucket == needs_me vs requires_action label
}

// Evaluate scores every sample with w and computes the metrics.
func Evaluate(samples []Sample, w scoring.Weights, provider, model string) Report {
	r := Report{Provider: provider, Model: model, Samples: len(samples)}
	var actionPairs, resolvedPairs, filterPairs, bucketPairs []probLabel
	var urg, rel [][2]float64
	cat := Categorical{Confusion: map[string]map[string]int{}}
	var catCorrect, sureN, sureCorrect, unsureCorrect float64
	type ranked struct {
		priority float64
		label    int
	}
	var rank []ranked
	for _, s := range samples {
		res := score(s, w)
		if s.Priority >= 0 {
			rank = append(rank, ranked{res.Priority, s.Priority})
		}
		if s.Noise != nil {
			filterPairs = append(filterPairs, probLabel{p: b2f(s.Filter == "noise"), y: *s.Noise})
		}
		if s.RequiresAction != nil {
			bucketPairs = append(bucketPairs, probLabel{p: b2f(res.Bucket == scoring.BucketNeedsMe || res.Pinned), y: *s.RequiresAction})
		}
		if s.Answers == nil {
			continue
		}
		r.Judged++
		if a, ok := s.Answers["requires_action_from_me"]; ok && s.RequiresAction != nil {
			actionPairs = append(actionPairs, probLabel{p: a.Noul, y: *s.RequiresAction})
		}
		if a, ok := s.Answers["resolved"]; ok && s.Resolved != nil {
			resolvedPairs = append(resolvedPairs, probLabel{p: a.Noul, y: *s.Resolved})
		}
		if a, ok := s.Answers["urgency"]; ok && s.Urgency >= 0 {
			urg = append(urg, [2]float64{a.Score, float64(s.Urgency)})
		}
		if a, ok := s.Answers["relevance"]; ok && s.Relevance >= 0 {
			rel = append(rel, [2]float64{a.Score, float64(s.Relevance)})
		}
		if a, ok := s.Answers["category"]; ok && s.Category != "" {
			cat.N++
			if cat.Confusion[s.Category] == nil {
				cat.Confusion[s.Category] = map[string]int{}
			}
			cat.Confusion[s.Category][a.Choice]++
			correct := a.Choice == s.Category
			sure := a.Confidence >= w.UnsureBelow
			if correct {
				catCorrect++
			}
			if sure {
				sureN++
				if correct {
					sureCorrect++
				}
			} else {
				cat.Unsure++
				if correct {
					unsureCorrect++
				}
			}
			if !correct && len(cat.Mistakes) < 40 {
				cat.Mistakes = append(cat.Mistakes, Mistake{Key: s.Key, Title: s.Title, Label: s.Category, Predicted: a.Choice, Confidence: a.Confidence})
			}
		}
	}
	if cat.N > 0 {
		cat.Accuracy = catCorrect / float64(cat.N)
	}
	if sureN > 0 {
		cat.AccSure = sureCorrect / sureN
	}
	if cat.Unsure > 0 {
		cat.AccUnsure = unsureCorrect / float64(cat.Unsure)
	}
	r.Category = cat
	r.RequiresAction = binary(actionPairs, 0.5)
	r.Resolved = binary(resolvedPairs, w.ResolvedThreshold)
	r.Urgency = ordinal(urg)
	r.Relevance = ordinal(rel)
	r.Filter = binary(filterPairs, 0.5)
	r.NeedsMeBucket = binary(bucketPairs, 0.5)
	for _, s := range samples {
		if s.Noise != nil && *s.Noise && s.Filter != "noise" && len(r.FilterMissed) < 40 {
			r.FilterMissed = append(r.FilterMissed, Mistake{Key: s.Key, Title: s.Title, Label: "noise", Predicted: s.Filter})
		}
	}
	// Ranking.
	sort.SliceStable(rank, func(i, j int) bool { return rank[i].priority > rank[j].priority })
	labels := make([]int, len(rank))
	prios := make([]float64, len(rank))
	for i, x := range rank {
		labels[i] = x.label
		prios[i] = x.priority
	}
	r.Ranking = Ranking{N: len(rank), NDCG10: NDCG(labels, 10), NDCG25: NDCG(labels, 25), NDCG: NDCG(labels, 0), Spearman: spearman(prios, labels)}
	return r
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// score applies the weights to a sample.
func score(s Sample, w scoring.Weights) scoring.Result {
	in := scoring.Inputs{Kind: s.Kind, Relations: s.Relations, Answers: s.Answers, Calibrated: s.Calibrated, IsAuthor: s.IsAuthor, ImpactLevel: s.ImpactLevel}
	if s.UpdatedAgeH > 0 {
		in.UpdatedAt = nowRef.Add(-hours(s.UpdatedAgeH))
	}
	return scoring.Score(w, in, nowRef)
}

// NDCG computes normalised discounted cumulative gain of a ranked list of
// relevance labels (gain = label), truncated at k (0 = whole list).
func NDCG(rankedLabels []int, k int) float64 {
	if len(rankedLabels) == 0 {
		return 0
	}
	if k <= 0 || k > len(rankedLabels) {
		k = len(rankedLabels)
	}
	dcg := 0.0
	for i := 0; i < k; i++ {
		dcg += float64(rankedLabels[i]) / math.Log2(float64(i)+2)
	}
	ideal := append([]int(nil), rankedLabels...)
	sort.Sort(sort.Reverse(sort.IntSlice(ideal)))
	idcg := 0.0
	for i := 0; i < k; i++ {
		idcg += float64(ideal[i]) / math.Log2(float64(i)+2)
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// spearman is the rank correlation between priorities and labels (ties averaged).
func spearman(x []float64, y []int) float64 {
	n := len(x)
	if n < 3 {
		return 0
	}
	yf := make([]float64, n)
	for i, v := range y {
		yf[i] = float64(v)
	}
	rx, ry := ranks(x), ranks(yf)
	var mx, my float64
	for i := 0; i < n; i++ {
		mx += rx[i]
		my += ry[i]
	}
	mx /= float64(n)
	my /= float64(n)
	var num, dx, dy float64
	for i := 0; i < n; i++ {
		num += (rx[i] - mx) * (ry[i] - my)
		dx += (rx[i] - mx) * (rx[i] - mx)
		dy += (ry[i] - my) * (ry[i] - my)
	}
	if dx == 0 || dy == 0 {
		return 0
	}
	return num / math.Sqrt(dx*dy)
}

func ranks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] > v[idx[b]] })
	out := make([]float64, len(v))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}
