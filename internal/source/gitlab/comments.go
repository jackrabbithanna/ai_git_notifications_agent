package gitlab

import (
	"context"
	"fmt"
	"strconv"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"gitinbox/internal/source"
)

// Comments lists the (non-system) notes of an issue or merge request, oldest
// first, keeping the most recent `limit` entries. GET only.
func (s *Source) Comments(ctx context.Context, repo string, number int, subjectType string, limit int) ([]source.Comment, error) {
	if repo == "" || number <= 0 {
		return nil, fmt.Errorf("comments: bad reference %s#%d", repo, number)
	}
	if limit <= 0 {
		limit = 50
	}
	wc := gl.WithContext(ctx)
	iid := int64(number)
	var out []source.Comment
	for page := 1; page <= 5; page++ {
		lo := gl.ListOptions{PerPage: 100, Page: int64(page)}
		var notes []*gl.Note
		var err error
		if subjectType == "MergeRequest" {
			notes, _, err = s.c.Notes.ListMergeRequestNotes(repo, iid, &gl.ListMergeRequestNotesOptions{ListOptions: lo, OrderBy: ptr("created_at"), Sort: ptr("asc")}, wc)
		} else {
			notes, _, err = s.c.Notes.ListIssueNotes(repo, iid, &gl.ListIssueNotesOptions{ListOptions: lo, OrderBy: ptr("created_at"), Sort: ptr("asc")}, wc)
		}
		if err != nil {
			return nil, fmt.Errorf("comments %s#%d: %w", repo, number, translate(err))
		}
		for _, n := range notes {
			if n == nil || n.System {
				continue
			}
			c := source.Comment{ID: strconv.FormatInt(n.ID, 10), Kind: "note", Author: n.Author.Username, Body: source.TrimText(n.Body, source.MaxCommentBody)}
			if n.CreatedAt != nil {
				c.CreatedAt = n.CreatedAt.UTC()
			}
			if n.Position != nil {
				c.Kind = "review_comment"
				c.Path = n.Position.NewPath
				if c.Path == "" {
					c.Path = n.Position.OldPath
				}
			}
			out = append(out, c)
		}
		if len(notes) < 100 {
			break
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

var _ source.Discusser = (*Source)(nil)
