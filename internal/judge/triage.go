package judge

// TriageVersion identifies the triage question set; bump when rubric text changes.
const TriageVersion = "triage.v1"

// Triage returns the triage.v1 question set (PLAN.md §4.3). All questions are
// asked together over one TriageState.
func Triage() Set {
	return Set{Version: TriageVersion, Questions: []Question{
		{
			ID: "category", Kind: Choice,
			Instructions: "Given the notification `thread`, the account `me` and its `me.relation_tags`, and the `latest_activity`, which single category best describes what this thread is for `me` right now?",
			Options: []Option{
				{"needs_my_review", "Someone is waiting for me to review or approve a change (review requested, approval required, re-review after updates)"},
				{"needs_my_reply", "A question, request or mention is directed at me and expects an answer or a decision from me"},
				{"blocking_or_failing", "Something I own is broken or stuck: CI failed on my change, merge conflict, changes requested on my work, a blocker flagged on my item"},
				{"awaiting_others", "I am involved but the next step belongs to someone else (my review was posted, my question is pending, waiting on a maintainer)"},
				{"fyi_progress", "Informational progress on something I follow: new activity, discussion, pushes, labels; no action expected from me"},
				{"release_or_announcement", "A release, tag, or announcement-style update"},
				{"resolved_no_action", "The thread has been closed, merged, or otherwise settled and nothing remains for me"},
			},
		},
		{
			ID: "requires_action_from_me", Kind: Noul,
			Instructions: "Does the `latest_activity` on `thread` ask for, or clearly expect, something from `me` specifically (a reply, a review, a fix, a decision)? Consider `me.relation_tags` and whether `me.login` is addressed.",
			NoulTrue:     "The latest activity is directed at me or requires my action to move forward",
			NoulFalse:    "Nothing is expected from me; the activity is informational or aimed at others",
		},
		{
			ID: "urgency", Kind: Score,
			Instructions: "How time-sensitive is the latest activity in `thread` for `me`, given `me.relation_tags` and what the activity says?",
			Levels: []string{
				"No time pressure: informational, or nothing is waiting on me",
				"This week: someone expects a response or action from me within days",
				"Today: a reviewer, release, or teammate is waiting on me now",
				"Blocking right now: CI is red on my change, a merge or release is held on my action, or I am explicitly pinged as blocking",
			},
		},
		{
			ID: "relevance", Kind: Score,
			Instructions: "How relevant is `thread` (title, repo, activity) to the work described in `profile.interests`? Judge topical overlap, not urgency.",
			Levels: []string{
				"Unrelated: touches areas the profile does not mention",
				"Tangential: same project or ecosystem but not the described areas",
				"Directly my area: matches the components, APIs or projects the profile names",
			},
		},
		{
			ID: "resolved", Kind: Noul,
			Instructions: "Judging from `thread.state`, the `latest_activity` and the activity kind, has this thread already been resolved (merged, closed, answered, fixed) so that nothing remains for `me`?",
			NoulTrue:     "Resolved: closed/merged/answered; no open ask remains",
			NoulFalse:    "Still open or the ask is unresolved",
		},
		{
			ID: "next_action", Kind: Choice,
			Instructions: "What is the single most appropriate next action for `me` on `thread`, given `me.relation_tags` and the `latest_activity`?",
			Options: []Option{
				{"review", "Review or approve the change"},
				{"reply", "Answer or comment"},
				{"rebase_or_fix", "Fix my change: address review comments, resolve conflicts, fix CI"},
				{"merge", "Merge or close it myself"},
				{"read_only", "Read it to stay informed; no response needed"},
				{"nothing", "Nothing; it can be archived"},
			},
		},
	}}
}

// TriageState is the JSON state sent with the triage questions. Field names
// are referenced by the instructions above; keep them in sync.
type TriageState struct {
	Thread         TriageThread    `json:"thread"`
	Me             TriageMe        `json:"me"`
	LatestActivity *TriageActivity `json:"latest_activity,omitempty"`
	Profile        TriageProfile   `json:"profile"`
}

type TriageThread struct {
	Forge        string   `json:"forge"`
	Repo         string   `json:"repo"`
	SubjectType  string   `json:"subject_type"`
	Number       int      `json:"number,omitempty"`
	Title        string   `json:"title"`
	Body         string   `json:"body,omitempty"` // trimmed item description
	State        string   `json:"state,omitempty"`
	IsDraft      bool     `json:"is_draft,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Author       string   `json:"author,omitempty"`
	ActivityKind string   `json:"activity_kind"`
	Reason       string   `json:"reason"`
	UpdatedAt    string   `json:"updated_at"`
}

type TriageMe struct {
	Login        string   `json:"login"`
	RelationTags []string `json:"relation_tags"`
	IsAuthor     bool     `json:"is_author"`
}

type TriageActivity struct {
	Author    string `json:"author,omitempty"`
	Body      string `json:"body,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Kind      string `json:"kind"` // comment | review | item_created | push | state_change | ...
}

type TriageProfile struct {
	Interests string `json:"interests"`
}
