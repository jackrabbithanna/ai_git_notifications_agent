package gitlab

import "gitinbox/internal/classify"

// classifyTodo maps a to-do action onto the shared activity kinds and relations.
func classifyTodo(action, targetKind string) (classify.Kind, []string) {
	switch action {
	case "assigned":
		return classify.KindAssignment, []string{classify.RelAssignee}
	case "mentioned", "directly_addressed":
		return classify.KindMention, []string{classify.RelMentioned}
	case "review_requested", "approval_required", "added_approver":
		return classify.KindReviewRequested, []string{classify.RelReviewer}
	case "review_submitted":
		return classify.KindReview, []string{classify.RelAuthor}
	case "build_failed":
		return classify.KindCI, []string{classify.RelAuthor}
	case "unmergeable", "merge_train_removed":
		return classify.KindStateChange, []string{classify.RelAuthor}
	case "marked":
		return classify.KindOther, []string{classify.RelSubscriber}
	}
	if targetKind == "Alert" {
		return classify.KindSecurity, []string{classify.RelSubscriber}
	}
	return classify.KindOther, []string{classify.RelSubscriber}
}

// classifyEvent maps a project event (already resolved to an issue/MR target)
// onto the shared kinds. noteKind is the event's target_type for note events.
func classifyEvent(action, targetKind, noteKind string, authorIsMe bool) (classify.Kind, []string) {
	rel := classify.RelSubscriber
	if authorIsMe {
		rel = classify.RelAuthor
	}
	rels := []string{rel}
	switch action {
	case "opened", "created":
		if targetKind == "MergeRequest" {
			return classify.KindNewPR, rels
		}
		return classify.KindNewIssue, rels
	case "closed", "reopened", "merged", "accepted":
		return classify.KindStateChange, rels
	case "approved":
		return classify.KindReview, rels
	case "commented on":
		if targetKind == "MergeRequest" && noteKind == "DiffNote" {
			return classify.KindReview, rels
		}
		return classify.KindComment, rels
	case "pushed to", "pushed new":
		return classify.KindCommit, rels
	}
	return classify.KindOther, rels
}
