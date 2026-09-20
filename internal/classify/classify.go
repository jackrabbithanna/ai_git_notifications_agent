// Package classify derives deterministic facts about a notification thread —
// what kind of activity it is and how the account relates to it — from the
// fields GitHub already provides. No model involved (PLAN.md §4.1).
package classify

import (
	"strings"

	"ghinbox/internal/ghmcp"
)

// Kind is the activity kind of the latest event on a thread.
type Kind string

const (
	KindNewIssue        Kind = "new_issue"
	KindNewPR           Kind = "new_pr"
	KindComment         Kind = "comment"
	KindReview          Kind = "review"
	KindReviewRequested Kind = "review_requested"
	KindStateChange     Kind = "state_change"
	KindCI              Kind = "ci"
	KindRelease         Kind = "release"
	KindMention         Kind = "mention"
	KindAssignment      Kind = "assignment"
	KindSecurity        Kind = "security"
	KindDiscussion      Kind = "discussion"
	KindCommit          Kind = "commit"
	KindOther           Kind = "other"
)

// Relation tags describe how the account is connected to the thread.
const (
	RelAuthor      = "author"
	RelAssignee    = "assignee"
	RelReviewer    = "reviewer"
	RelMentioned   = "mentioned"
	RelParticipant = "participant"
	RelSubscriber  = "subscriber"
)

// Result is the classification of one notification.
type Result struct {
	Kind         Kind
	RelationTags []string
	IsNewItem    bool // the notification is about the item itself, not later activity on it
}

// IsNewItem reports whether the thread's latest activity is not a comment:
// GitHub points latest_comment_url at the subject itself when the newest event
// is the item's creation, a push to a PR, or another non-comment update. Telling
// "brand new" from "pushed" needs the item's created_at (pull_request_read get),
// which the enrichment step adds in a later milestone.
func IsNewItem(n ghmcp.Notification) bool {
	return n.LatestCommentURL == "" || n.LatestCommentURL == n.SubjectURL
}

// Classify maps a notification to an activity kind and relation tags.
func Classify(n ghmcp.Notification) Result {
	r := Result{RelationTags: relations(n.Reason), IsNewItem: IsNewItem(n)}
	switch n.Reason {
	case "security_alert", "security_advisory_credit":
		r.Kind = KindSecurity
		return r
	case "ci_activity":
		r.Kind = KindCI
		return r
	}
	switch n.SubjectType {
	case "Release":
		r.Kind = KindRelease
		return r
	case "Discussion":
		r.Kind = KindDiscussion
		return r
	case "Commit":
		r.Kind = KindCommit
		return r
	case "CheckSuite":
		r.Kind = KindCI
		return r
	case "RepositoryVulnerabilityAlert", "RepositoryAdvisory":
		r.Kind = KindSecurity
		return r
	}
	// Issues and pull requests: the reason names the event when it is about me.
	switch n.Reason {
	case "assign":
		r.Kind = KindAssignment
		return r
	case "review_requested", "approval_requested":
		r.Kind = KindReviewRequested
		return r
	case "mention", "team_mention":
		r.Kind = KindMention
		return r
	case "state_change":
		r.Kind = KindStateChange
		return r
	}
	if r.IsNewItem {
		if n.SubjectType == "PullRequest" {
			r.Kind = KindNewPR
		} else {
			r.Kind = KindNewIssue
		}
		return r
	}
	u := n.LatestCommentURL
	switch {
	case strings.Contains(u, "/pulls/comments/"), strings.Contains(u, "/pulls/") && strings.Contains(u, "/reviews/"):
		r.Kind = KindReview
	case strings.Contains(u, "/issues/comments/"), strings.Contains(u, "/comments/"):
		r.Kind = KindComment
	case n.SubjectType == "Issue" || n.SubjectType == "PullRequest":
		r.Kind = KindComment
	default:
		r.Kind = KindOther
	}
	return r
}

func relations(reason string) []string {
	switch reason {
	case "author":
		return []string{RelAuthor}
	case "assign":
		return []string{RelAssignee}
	case "review_requested", "approval_requested":
		return []string{RelReviewer}
	case "mention", "team_mention":
		return []string{RelMentioned}
	case "comment":
		return []string{RelParticipant}
	case "subscribed", "manual", "state_change", "ci_activity", "security_alert", "member_feature_requested":
		return []string{RelSubscriber}
	}
	return []string{RelSubscriber}
}

// HTMLURL converts a notification's subject API URL into a browser URL.
// e.g. https://api.github.com/repos/o/r/pulls/12 -> https://github.com/o/r/pull/12
func HTMLURL(host string, n ghmcp.Notification) string {
	if host == "" {
		host = "github.com"
	}
	base := "https://" + host + "/"
	if n.SubjectType == "CheckSuite" {
		// CI notifications carry no subject URL; the Actions tab is the useful target.
		if n.Repo != "" {
			return base + n.Repo + "/actions"
		}
		return base + "notifications"
	}
	u := n.SubjectURL
	i := strings.Index(u, "/repos/")
	if i < 0 {
		if n.Repo != "" {
			return base + n.Repo
		}
		return base + "notifications"
	}
	path := u[i+len("/repos/"):]
	path = strings.Replace(path, "/pulls/", "/pull/", 1)
	path = strings.Replace(path, "/commits/", "/commit/", 1)
	if n.SubjectType == "Release" {
		// /releases/<id> is an API id, not browseable; fall back to the releases page.
		if j := strings.Index(path, "/releases/"); j >= 0 {
			path = path[:j] + "/releases"
		}
	}
	return base + path
}
