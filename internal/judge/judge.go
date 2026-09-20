// Package judge defines typed judgments (PLAN.md §4.3/§4.8): a Judge answers a
// set of Questions about a JSON state and returns Answers whose shapes mirror
// the TypeSafe System One contract, so calibrated (Jev) and uncalibrated
// (Ollama) providers are interchangeable to the scoring code.
package judge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Kind is a question/answer type.
type Kind string

const (
	Noul   Kind = "noul"   // yes/no → probability of yes
	Choice Kind = "choice" // one of a defined set → distribution
	Score  Kind = "score"  // position on ordered levels → weighted index
)

// Option is one Choice alternative.
type Option struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Question is one typed judgment. IDs are for code; instructions/criteria carry
// the full meaning because the model never sees the ID.
type Question struct {
	ID           string   `json:"id"`
	Kind         Kind     `json:"kind"`
	Instructions string   `json:"instructions"`
	NoulTrue     string   `json:"noulTrue,omitempty"`  // Noul: what yes means
	NoulFalse    string   `json:"noulFalse,omitempty"` // Noul: what no means
	Options      []Option `json:"options,omitempty"`   // Choice: ordered options
	Levels       []string `json:"levels,omitempty"`    // Score: ordered level descriptions (≥2)
}

// Validate checks the question is well-formed for its kind.
func (q Question) Validate() error {
	if q.ID == "" || q.Instructions == "" {
		return fmt.Errorf("judge: question %q needs an id and instructions", q.ID)
	}
	switch q.Kind {
	case Noul:
	case Choice:
		if len(q.Options) < 2 {
			return fmt.Errorf("judge: choice %q needs ≥2 options", q.ID)
		}
		for _, o := range q.Options {
			if o.ID == "" || o.Description == "" {
				return fmt.Errorf("judge: choice %q has an option without id/description", q.ID)
			}
		}
	case Score:
		if len(q.Levels) < 2 {
			return fmt.Errorf("judge: score %q needs ≥2 levels", q.ID)
		}
	default:
		return fmt.Errorf("judge: question %q has unknown kind %q", q.ID, q.Kind)
	}
	return nil
}

// Answer is one typed result. Probabilities sum to 1 (Choice: by option id;
// Score: by level index "0".."n-1"). Confidence is the provider's certainty
// summary in 0..1 (for uncalibrated providers: the top probability).
type Answer struct {
	Kind          Kind               `json:"kind"`
	Noul          float64            `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Legend        []string           `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// Normalized returns a Score answer's position as 0..1 (§4.8: score is a level index).
func (a Answer) Normalized() float64 {
	if a.Kind != Score || len(a.Legend) < 2 {
		return 0
	}
	return clamp01(a.Score / float64(len(a.Legend)-1))
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

// Usage is token accounting for one request.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Response is the outcome of one Ask.
type Response struct {
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Calibrated bool              `json:"calibrated"`
	Answers    map[string]Answer `json:"answers"`
	Usage      Usage             `json:"usage"`
	Latency    time.Duration     `json:"latency"`
}

// Judge evaluates questions over a state. Implementations must be safe for
// concurrent use.
type Judge interface {
	Name() string
	Calibrated() bool
	Ask(ctx context.Context, state any, questions []Question) (Response, error)
}

// Errors that should stop a run rather than be retried per thread.
var (
	ErrUnauthorized   = errors.New("judge: provider rejected the credentials")
	ErrInvalidRequest = errors.New("judge: provider rejected the request as malformed")
	ErrUnavailable    = errors.New("judge: provider unavailable")
)

// Set is a versioned question set.
type Set struct {
	Version   string
	Questions []Question
}

// Validate checks every question and ID uniqueness.
func (s Set) Validate() error {
	seen := map[string]bool{}
	for _, q := range s.Questions {
		if err := q.Validate(); err != nil {
			return err
		}
		if seen[q.ID] {
			return fmt.Errorf("judge: duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
	}
	return nil
}

// IDs returns the question ids in order.
func (s Set) IDs() []string {
	out := make([]string, 0, len(s.Questions))
	for _, q := range s.Questions {
		out = append(out, q.ID)
	}
	return out
}

// NormalizeProbabilities clamps negatives, renormalises to sum 1, and fills
// missing keys with 0. Returns the key with the highest probability.
func NormalizeProbabilities(p map[string]float64, keys []string) (map[string]float64, string) {
	out := make(map[string]float64, len(keys))
	sum := 0.0
	for _, k := range keys {
		v := p[k]
		if v < 0 {
			v = 0
		}
		out[k] = v
		sum += v
	}
	if sum <= 0 {
		// Nothing usable: uniform.
		for _, k := range keys {
			out[k] = 1 / float64(len(keys))
		}
		sum = 1
	} else {
		for k := range out {
			out[k] /= sum
		}
	}
	best, bestV := "", -1.0
	ordered := append([]string(nil), keys...)
	sort.Strings(ordered)    // deterministic tie-break
	for _, k := range keys { // prefer declaration order on exact ties
		if out[k] > bestV {
			best, bestV = k, out[k]
		}
	}
	_ = ordered
	return out, best
}

// TopProbability is the uncalibrated confidence summary.
func TopProbability(p map[string]float64) float64 {
	m := 0.0
	for _, v := range p {
		if v > m {
			m = v
		}
	}
	return m
}
