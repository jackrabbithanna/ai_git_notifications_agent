package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

const bodyLimit = 2000

var (
	reIssueComment  = regexp.MustCompile(`/issues/comments/(\d+)$`)
	reReviewComment = regexp.MustCompile(`/pulls/comments/(\d+)$`)
	reReview        = regexp.MustCompile(`/pulls/\d+/reviews/(\d+)$`)
)

// Enrich fetches the item (issue_read / pull_request_read "get") and, when the
// notification points at a specific comment/review, that comment by id.
func (s *Source) Enrich(ctx context.Context, t store.Thread) (store.Enrichment, error) {
	e := store.Enrichment{EnrichedVersion: t.Version()}
	if t.SubjectNumber == 0 || t.Repo == "" || (t.SubjectType != "Issue" && t.SubjectType != "PullRequest") {
		return e, nil
	}
	owner, name, ok := strings.Cut(t.Repo, "/")
	if !ok {
		return e, nil
	}
	var raw json.RawMessage
	var err error
	if t.SubjectType == "PullRequest" {
		raw, err = s.client.PullRequestRead(ctx, owner, name, t.SubjectNumber, "get")
	} else {
		raw, err = s.client.IssueRead(ctx, owner, name, t.SubjectNumber, "get")
	}
	if err != nil {
		return e, fmt.Errorf("enrich %s#%d: %w", t.Repo, t.SubjectNumber, err)
	}
	var item struct {
		Body   string `json:"body"`
		State  string `json:"state"`
		Draft  bool   `json:"draft"`
		Merged bool   `json:"merged"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		Labels    labelList `json:"labels"`
		CreatedAt string    `json:"created_at"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return e, fmt.Errorf("enrich %s#%d: decode: %w", t.Repo, t.SubjectNumber, err)
	}
	if c, err := time.Parse(time.RFC3339, item.CreatedAt); err == nil {
		c = c.UTC()
		e.ItemCreatedAt = &c
	}
	e.ItemAuthor = item.User.Login
	e.ItemState = item.State
	if item.Merged {
		e.ItemState = "merged"
	}
	e.ItemDraft = item.Draft
	e.ItemBody = source.TrimText(item.Body, bodyLimit)
	e.ItemLabels = []string(item.Labels)

	// Latest comment, located by the id GitHub put in latest_comment_url.
	var method string
	var id int64
	switch {
	case reIssueComment.MatchString(t.LatestCommentURL):
		id, _ = strconv.ParseInt(reIssueComment.FindStringSubmatch(t.LatestCommentURL)[1], 10, 64)
		method = "get_comments"
	case reReviewComment.MatchString(t.LatestCommentURL):
		id, _ = strconv.ParseInt(reReviewComment.FindStringSubmatch(t.LatestCommentURL)[1], 10, 64)
		method = "get_review_comments"
	case reReview.MatchString(t.LatestCommentURL):
		id, _ = strconv.ParseInt(reReview.FindStringSubmatch(t.LatestCommentURL)[1], 10, 64)
		method = "get_reviews"
	}
	if method == "" || id == 0 {
		return e, nil
	}
	for page := 1; page <= 3; page++ {
		var page_ json.RawMessage
		var err error
		if method == "get_comments" && t.SubjectType == "Issue" {
			page_, err = s.client.IssueReadPage(ctx, owner, name, t.SubjectNumber, method, page, 100)
		} else {
			page_, err = s.client.PullRequestReadPage(ctx, owner, name, t.SubjectNumber, method, page, 100)
		}
		if err != nil {
			return e, fmt.Errorf("enrich %s#%d %s: %w", t.Repo, t.SubjectNumber, method, err)
		}
		if c, found, count := findComment(page_, id); found {
			e.LatestAuthor = c.author
			e.LatestBody = source.TrimText(c.body, bodyLimit)
			if !c.at.IsZero() {
				at := c.at
				e.LatestAt = &at
			}
			return e, nil
		} else if count < 100 {
			break
		}
	}
	return e, nil
}

// labelList accepts GitHub labels as objects ({"name": …}, REST) or plain
// strings (the MCP server's trimmed "get" payloads).
type labelList []string

func (l *labelList) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			out = append(out, s)
			continue
		}
		var o struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &o); err == nil && o.Name != "" {
			out = append(out, o.Name)
		}
	}
	*l = out
	return nil
}

type comment struct {
	author string
	body   string
	at     time.Time
}

// findComment walks any JSON shape (REST arrays, GraphQL review_threads
// wrappers) for the comment with the given id. REST payloads carry a numeric
// id; the GraphQL-shaped review comments only expose it in html_url anchors
// (#discussion_r<id>, #pullrequestreview-<id>, #issuecomment-<id>). Returns
// how many comment-like objects were seen so callers can decide whether
// another page may exist.
func findComment(raw json.RawMessage, id int64) (comment, bool, int) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return comment{}, false, 0
	}
	idStr := strconv.FormatInt(id, 10)
	anchors := []string{"#discussion_r" + idStr, "#pullrequestreview-" + idStr, "#issuecomment-" + idStr}
	count := 0
	var found *comment
	var walk func(x any)
	walk = func(x any) {
		if found != nil {
			return
		}
		switch n := x.(type) {
		case []any:
			for _, it := range n {
				walk(it)
			}
		case map[string]any:
			_, hasBody := n["body"]
			_, hasState := n["state"]
			if hasBody || hasState {
				count++
				matched := numEq(n["id"], id) || numEq(n["databaseId"], id) || numEq(n["database_id"], id)
				if !matched {
					if u, ok := n["html_url"].(string); ok {
						for _, a := range anchors {
							if strings.HasSuffix(u, a) {
								matched = true
								break
							}
						}
					}
				}
				if matched {
					c := comment{}
					if body, ok := n["body"].(string); ok {
						c.body = body
					}
					for _, key := range []string{"user", "author"} {
						switch u := n[key].(type) {
						case map[string]any:
							if l, ok := u["login"].(string); ok {
								c.author = l
							}
						case string:
							c.author = u
						}
					}
					for _, key := range []string{"created_at", "createdAt", "submitted_at", "submittedAt"} {
						if ts, ok := n[key].(string); ok {
							if at, err := time.Parse(time.RFC3339, ts); err == nil {
								c.at = at.UTC()
								break
							}
						}
					}
					if st, ok := n["state"].(string); ok && c.body == "" {
						c.body = "(review: " + strings.ToLower(st) + ")"
					}
					found = &c
					return
				}
			}
			for _, it := range n {
				walk(it)
			}
		}
	}
	walk(v)
	if found != nil {
		return *found, true, count
	}
	return comment{}, false, count
}

func numEq(v any, id int64) bool {
	switch n := v.(type) {
	case float64:
		return int64(n) == id
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return err == nil && i == id
	}
	return false
}

var _ source.Enricher = (*Source)(nil)
