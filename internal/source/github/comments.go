package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitinbox/internal/source"
)

// Comments lists an issue's comments or a pull request's comments, reviews and
// review comments (issue_read / pull_request_read), oldest first, keeping the
// most recent `limit` entries. Read-only tools only, whatever the write mode.
func (s *Source) Comments(ctx context.Context, repo string, number int, subjectType string, limit int) ([]source.Comment, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || number <= 0 {
		return nil, fmt.Errorf("comments: bad reference %s#%d", repo, number)
	}
	if limit <= 0 {
		limit = 50
	}
	pages := (limit+99)/100 + 1
	if pages > 5 {
		pages = 5
	}
	var out []source.Comment
	collect := func(method, kind string, viaPR bool) error {
		for page := 1; page <= pages; page++ {
			var raw json.RawMessage
			var err error
			if viaPR {
				raw, err = s.client.PullRequestReadPage(ctx, owner, name, number, method, page, 100)
			} else {
				raw, err = s.client.IssueReadPage(ctx, owner, name, number, method, page, 100)
			}
			if err != nil {
				return fmt.Errorf("comments %s#%d %s: %w", repo, number, method, err)
			}
			batch := collectComments(raw, kind)
			out = append(out, batch...)
			if len(batch) < 100 {
				return nil
			}
		}
		return nil
	}
	if subjectType == "PullRequest" {
		if err := collect("get_comments", "comment", true); err != nil {
			return nil, err
		}
		if err := collect("get_reviews", "review", true); err != nil {
			return nil, err
		}
		if err := collect("get_review_comments", "review_comment", true); err != nil {
			return nil, err
		}
	} else if err := collect("get_comments", "comment", false); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

var reAnchorID = regexp.MustCompile(`#(?:discussion_r|pullrequestreview-|issuecomment-)(\d+)$`)

// collectComments walks any JSON shape (REST arrays, GraphQL review-thread
// wrappers) and returns every comment-like object (has a body or a review
// state) as a Comment. Empty reviews (a bare approval without text) keep their
// state so the discussion shows who approved.
func collectComments(raw json.RawMessage, kind string) []source.Comment {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []source.Comment
	var walk func(x any)
	walk = func(x any) {
		switch n := x.(type) {
		case []any:
			for _, it := range n {
				walk(it)
			}
		case map[string]any:
			body, hasBody := n["body"].(string)
			state, hasState := n["state"].(string)
			if hasBody || hasState {
				c := source.Comment{Kind: kind, Body: source.TrimText(body, source.MaxCommentBody)}
				switch id := n["id"].(type) {
				case float64:
					c.ID = strconv.FormatInt(int64(id), 10)
				case string:
					c.ID = id
				}
				if dbID, ok := n["databaseId"].(float64); ok && c.ID == "" {
					c.ID = strconv.FormatInt(int64(dbID), 10)
				}
				if u, ok := n["html_url"].(string); ok {
					c.HTMLURL = u
					if m := reAnchorID.FindStringSubmatch(u); m != nil && (c.ID == "" || !isNumeric(c.ID)) {
						c.ID = m[1]
					}
				} else if u, ok := n["url"].(string); ok && strings.HasPrefix(u, "https://github.com/") {
					c.HTMLURL = u
				}
				for _, key := range []string{"user", "author"} {
					switch u := n[key].(type) {
					case map[string]any:
						if l, ok := u["login"].(string); ok {
							c.Author = l
						}
					case string:
						c.Author = u
					}
				}
				for _, key := range []string{"created_at", "createdAt", "submitted_at", "submittedAt"} {
					if ts, ok := n[key].(string); ok {
						if at, err := time.Parse(time.RFC3339, ts); err == nil {
							c.CreatedAt = at.UTC()
							break
						}
					}
				}
				if p, ok := n["path"].(string); ok {
					c.Path = p
				}
				if hasState && kind == "review" {
					c.State = strings.ToLower(state)
				}
				if c.Body != "" || c.State != "" {
					out = append(out, c)
				}
				return // comment objects do not nest further comments
			}
			for _, it := range n {
				walk(it)
			}
		}
	}
	walk(v)
	return out
}

func isNumeric(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

var _ source.Discusser = (*Source)(nil)
