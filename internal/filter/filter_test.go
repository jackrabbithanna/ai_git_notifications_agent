package filter

import (
	"testing"

	"ghinbox/internal/classify"
)

func TestApply(t *testing.T) {
	r := Defaults()
	r.MutedRepos = []string{"noisy/repo", "spam-org/*"}
	r.MutedTitleKeywords = []string{"[skip-me]"}
	r.DropReleases = true
	r.KeepReleaseRepos = []string{"civicrm/civicrm-core"}

	cases := []struct {
		name   string
		in     Input
		keep   bool
		reason string
	}{
		{"plain pr", Input{Repo: "o/r", Title: "Fix the thing", Kind: classify.KindNewPR}, true, ""},
		{"dependabot", Input{Repo: "o/r", Title: "Bump lodash from 4.17.20 to 4.17.21", Kind: classify.KindNewPR}, false, "bot"},
		{"renovate", Input{Repo: "o/r", Title: "chore(deps): update dependency vite to v8", Kind: classify.KindNewPR}, false, "bot"},
		{"ci ok", Input{Repo: "o/r", Title: "CI workflow run succeeded for main branch", Kind: classify.KindCI}, false, "ci_success"},
		{"ci failed", Input{Repo: "o/r", Title: "CI workflow run failed for main branch", Kind: classify.KindCI}, true, ""},
		{"muted repo", Input{Repo: "noisy/repo", Title: "anything", Kind: classify.KindComment}, false, "muted_repo"},
		{"muted org", Input{Repo: "spam-org/x", Title: "anything", Kind: classify.KindComment}, false, "muted_repo"},
		{"keyword", Input{Repo: "o/r", Title: "Please [skip-me] now", Kind: classify.KindComment}, false, "keyword"},
		{"release dropped", Input{Repo: "o/r", Title: "v1.2.3", Kind: classify.KindRelease}, false, "release"},
		{"release kept", Input{Repo: "civicrm/civicrm-core", Title: "6.10.0", Kind: classify.KindRelease}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Apply(r, c.in)
			if v.Keep != c.keep || v.Reason != c.reason {
				t.Errorf("got keep=%v reason=%q, want keep=%v reason=%q", v.Keep, v.Reason, c.keep, c.reason)
			}
		})
	}
}
