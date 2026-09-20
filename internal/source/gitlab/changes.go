package gitlab

import (
	"context"
	"fmt"
	"strings"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"ghinbox/internal/profiles"
	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// Changes fetches a merge request and its diffs (paged).
func (s *Source) Changes(ctx context.Context, repo string, number int) (source.ChangeSet, error) {
	wc := gl.WithContext(ctx)
	iid := int64(number)
	mr, _, err := s.c.MergeRequests.GetMergeRequest(repo, iid, nil, wc)
	if err != nil {
		return source.ChangeSet{}, fmt.Errorf("changes %s!%d: %w", repo, number, translate(err))
	}
	cs := source.ChangeSet{
		Forge: store.ForgeGitLab, Repo: repo, Number: number, Kind: "mr", Title: mr.Title, Body: source.TrimText(mr.Description, 4000),
		Labels: []string(mr.Labels), Base: mr.TargetBranch, Draft: mr.Draft, HeadSHA: mr.SHA, HTMLURL: mr.WebURL,
	}
	if mr.Author != nil {
		cs.Author = mr.Author.Username
	}
	switch mr.State {
	case "merged":
		cs.State = "merged"
	case "closed":
		cs.State = "closed"
	default:
		cs.State = "open"
	}
	if mr.CreatedAt != nil {
		cs.CreatedAt = mr.CreatedAt.UTC()
	}
	if mr.UpdatedAt != nil {
		cs.UpdatedAt = mr.UpdatedAt.UTC()
	}
	if mr.MergedAt != nil {
		m := mr.MergedAt.UTC()
		cs.MergedAt = &m
	}
	opt := &gl.ListMergeRequestDiffsOptions{ListOptions: gl.ListOptions{PerPage: 100, Page: 1}}
	for page := 1; page <= 3 && len(cs.Files) < source.MaxChangeFiles; page++ {
		opt.Page = int64(page)
		diffs, resp, err := s.c.MergeRequests.ListMergeRequestDiffs(repo, iid, opt, wc)
		if err != nil {
			return cs, fmt.Errorf("changes %s!%d diffs: %w", repo, number, translate(err))
		}
		for _, d := range diffs {
			path := d.NewPath
			if path == "" {
				path = d.OldPath
			}
			status := "modified"
			switch {
			case d.NewFile:
				status = "added"
			case d.DeletedFile:
				status = "removed"
			case d.RenamedFile:
				status = "renamed"
			}
			add, del := countDiff(d.Diff)
			cs.Files = append(cs.Files, profiles.FileChange{Path: path, Status: status, Additions: add, Deletions: del, Patch: d.Diff})
			cs.Additions += add
			cs.Deletions += del
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		if page == 3 {
			cs.Truncated = true
		}
	}
	if len(cs.Files) >= source.MaxChangeFiles {
		cs.Truncated = true
	}
	cs.FilesTotal = len(cs.Files)
	return cs, nil
}

func countDiff(diff string) (add, del int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			add++
		case strings.HasPrefix(line, "-"):
			del++
		}
	}
	return add, del
}

// RecentlyMerged lists merge requests merged after since (newest first).
func (s *Source) RecentlyMerged(ctx context.Context, repo string, since time.Time, limit int) ([]source.ChangeRef, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	opt := &gl.ListProjectMergeRequestsOptions{State: ptr("merged"), OrderBy: ptr("updated_at"), Sort: ptr("desc"), UpdatedAfter: &since, ListOptions: gl.ListOptions{PerPage: int64(limit), Page: 1}}
	mrs, _, err := s.c.MergeRequests.ListProjectMergeRequests(repo, opt, gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("recently merged %s: %w", repo, translate(err))
	}
	var out []source.ChangeRef
	for _, mr := range mrs {
		if mr.MergedAt == nil || !mr.MergedAt.After(since) {
			continue
		}
		m := mr.MergedAt.UTC()
		ref := source.ChangeRef{Repo: repo, Number: int(mr.IID), Title: mr.Title, HTMLURL: mr.WebURL, MergedAt: &m}
		if mr.Author != nil {
			ref.Author = mr.Author.Username
		}
		if mr.UpdatedAt != nil {
			ref.UpdatedAt = mr.UpdatedAt.UTC()
		}
		out = append(out, ref)
	}
	return out, nil
}

var _ source.Changer = (*Source)(nil)
var _ source.LandedLister = (*Source)(nil)
