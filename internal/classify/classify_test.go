package classify

import (
	"testing"

	"gitinbox/internal/ghmcp"
)

func n(reason, subjectType, subjectURL, latest string) ghmcp.Notification {
	return ghmcp.Notification{Reason: reason, SubjectType: subjectType, SubjectURL: subjectURL, LatestCommentURL: latest, Repo: "o/r"}
}

func TestClassify(t *testing.T) {
	const pr = "https://api.github.com/repos/o/r/pulls/7"
	const issue = "https://api.github.com/repos/o/r/issues/8"
	cases := []struct {
		name string
		in   ghmcp.Notification
		kind Kind
		rel  string
		new  bool
	}{
		{"new pr, subscribed", n("subscribed", "PullRequest", pr, pr), KindNewPR, RelSubscriber, true},
		{"new pr, no latest", n("subscribed", "PullRequest", pr, ""), KindNewPR, RelSubscriber, true},
		{"new issue", n("subscribed", "Issue", issue, issue), KindNewIssue, RelSubscriber, true},
		{"issue comment", n("comment", "Issue", issue, "https://api.github.com/repos/o/r/issues/comments/1"), KindComment, RelParticipant, false},
		{"pr review comment", n("subscribed", "PullRequest", pr, "https://api.github.com/repos/o/r/pulls/comments/2"), KindReview, RelSubscriber, false},
		{"pr conversation comment", n("author", "PullRequest", pr, "https://api.github.com/repos/o/r/issues/comments/3"), KindComment, RelAuthor, false},
		{"review requested on new pr", n("review_requested", "PullRequest", pr, pr), KindReviewRequested, RelReviewer, true},
		{"mention", n("mention", "Issue", issue, "https://api.github.com/repos/o/r/issues/comments/4"), KindMention, RelMentioned, false},
		{"assigned", n("assign", "Issue", issue, issue), KindAssignment, RelAssignee, true},
		{"merged", n("state_change", "PullRequest", pr, "https://api.github.com/repos/o/r/issues/comments/5"), KindStateChange, RelSubscriber, false},
		{"ci", n("ci_activity", "CheckSuite", "", ""), KindCI, RelSubscriber, true},
		{"release", n("subscribed", "Release", "https://api.github.com/repos/o/r/releases/9", ""), KindRelease, RelSubscriber, true},
		{"security", n("security_alert", "RepositoryVulnerabilityAlert", "", ""), KindSecurity, RelSubscriber, true},
		{"discussion", n("subscribed", "Discussion", "https://api.github.com/repos/o/r/discussions/3", ""), KindDiscussion, RelSubscriber, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in)
			if got.Kind != c.kind {
				t.Errorf("kind = %s, want %s", got.Kind, c.kind)
			}
			if len(got.RelationTags) != 1 || got.RelationTags[0] != c.rel {
				t.Errorf("relations = %v, want [%s]", got.RelationTags, c.rel)
			}
			if got.IsNewItem != c.new {
				t.Errorf("IsNewItem = %v, want %v", got.IsNewItem, c.new)
			}
		})
	}
}

func TestHTMLURL(t *testing.T) {
	cases := map[ghmcp.Notification]string{
		n("subscribed", "PullRequest", "https://api.github.com/repos/o/r/pulls/7", ""):      "https://github.com/o/r/pull/7",
		n("subscribed", "Issue", "https://api.github.com/repos/o/r/issues/8", ""):           "https://github.com/o/r/issues/8",
		n("subscribed", "Release", "https://api.github.com/repos/o/r/releases/123", ""):     "https://github.com/o/r/releases",
		n("subscribed", "Commit", "https://api.github.com/repos/o/r/commits/abc", ""):       "https://github.com/o/r/commit/abc",
		n("subscribed", "Discussion", "https://api.github.com/repos/o/r/discussions/3", ""): "https://github.com/o/r/discussions/3",
		n("ci_activity", "CheckSuite", "", ""):                                              "https://github.com/o/r/actions",
	}
	for in, want := range cases {
		if got := HTMLURL("", in); got != want {
			t.Errorf("%s %s: got %s, want %s", in.SubjectType, in.SubjectURL, got, want)
		}
	}
	if got := HTMLURL("ghe.example.com", n("subscribed", "Issue", "https://ghe.example.com/api/v3/repos/o/r/issues/1", "")); got != "https://ghe.example.com/o/r/issues/1" {
		t.Errorf("GHE: got %s", got)
	}
}
