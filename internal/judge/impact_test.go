package judge

import "testing"

func TestImpactSetValid(t *testing.T) {
	s := Impact()
	if s.Version != ImpactVersion || len(s.Questions) != 5 {
		t.Fatalf("set: %s %d", s.Version, len(s.Questions))
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	q := s.Questions[2]
	if q.ID != "downstream_impact" || q.Kind != Score || len(q.Levels) != 4 {
		t.Fatalf("downstream_impact rubric: %+v", q)
	}
}
