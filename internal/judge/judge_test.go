package judge

import "testing"

func TestTriageSetValid(t *testing.T) {
	s := Triage()
	if s.Version != TriageVersion || len(s.Questions) != 6 {
		t.Fatalf("set: %s %d", s.Version, len(s.Questions))
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, q := range s.Questions {
		if q.Kind == Score && len(q.Levels) < 3 {
			t.Fatalf("%s: score rubric too short", q.ID)
		}
	}
}

func TestNormalizeProbabilities(t *testing.T) {
	p, best := NormalizeProbabilities(map[string]float64{"a": 2, "b": 1, "zzz": 5}, []string{"a", "b", "c"})
	if p["a"] < 0.66 || p["a"] > 0.67 || p["c"] != 0 || best != "a" {
		t.Fatalf("got %v best=%s", p, best)
	}
	p, best = NormalizeProbabilities(nil, []string{"x", "y"})
	if p["x"] != 0.5 || best != "x" {
		t.Fatalf("uniform fallback: %v %s", p, best)
	}
	a := Answer{Kind: Score, Score: 2.4, Legend: []string{"a", "b", "c", "d"}}
	if n := a.Normalized(); n < 0.79 || n > 0.81 {
		t.Fatalf("normalized = %v", n)
	}
}
