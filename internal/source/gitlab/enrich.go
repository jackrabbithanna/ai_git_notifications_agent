package gitlab

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

const bodyLimit = 2000

var reNote = regexp.MustCompile(`#note_(\d+)$`)

// Enrich fetches the issue/MR and, when the thread points at a note, that note.
func (s *Source) Enrich(ctx context.Context, t store.Thread) (store.Enrichment, error) {
	e := store.Enrichment{EnrichedVersion: t.Version()}
	if t.SubjectNumber == 0 || t.Repo == "" || (t.SubjectType != "Issue" && t.SubjectType != "MergeRequest") {
		return e, nil
	}
	wc := gl.WithContext(ctx)
	iid := int64(t.SubjectNumber)
	switch t.SubjectType {
	case "Issue":
		is, _, err := s.c.Issues.GetIssue(t.Repo, iid, wc)
		if err != nil {
			return e, fmt.Errorf("enrich %s#%d: %w", t.Repo, iid, translate(err))
		}
		if is.Author != nil {
			e.ItemAuthor = is.Author.Username
		}
		e.ItemState = normalizeState(is.State)
		e.ItemLabels = []string(is.Labels)
		e.ItemBody = source.TrimText(is.Description, bodyLimit)
		if is.CreatedAt != nil {
			c := is.CreatedAt.UTC()
			e.ItemCreatedAt = &c
		}
	case "MergeRequest":
		mr, _, err := s.c.MergeRequests.GetMergeRequest(t.Repo, iid, nil, wc)
		if err != nil {
			return e, fmt.Errorf("enrich %s!%d: %w", t.Repo, iid, translate(err))
		}
		if mr.Author != nil {
			e.ItemAuthor = mr.Author.Username
		}
		e.ItemState = normalizeState(mr.State)
		e.ItemDraft = mr.Draft
		e.ItemLabels = []string(mr.Labels)
		e.ItemBody = source.TrimText(mr.Description, bodyLimit)
		if mr.CreatedAt != nil {
			c := mr.CreatedAt.UTC()
			e.ItemCreatedAt = &c
		}
	}
	m := reNote.FindStringSubmatch(t.LatestCommentURL)
	if m == nil {
		return e, nil
	}
	noteID, _ := strconv.ParseInt(m[1], 10, 64)
	var body, author string
	var at *gl.Note
	if t.SubjectType == "Issue" {
		n, _, err := s.c.Notes.GetIssueNote(t.Repo, iid, noteID, wc)
		if err != nil {
			return e, nil // note may be gone; the item data is still useful
		}
		at, body, author = n, n.Body, n.Author.Username
	} else {
		n, _, err := s.c.Notes.GetMergeRequestNote(t.Repo, iid, noteID, wc)
		if err != nil {
			return e, nil
		}
		at, body, author = n, n.Body, n.Author.Username
	}
	e.LatestAuthor = author
	e.LatestBody = source.TrimText(body, bodyLimit)
	if at != nil && at.CreatedAt != nil {
		ts := at.CreatedAt.UTC()
		e.LatestAt = &ts
	}
	return e, nil
}

var _ source.Enricher = (*Source)(nil)
