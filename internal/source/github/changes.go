package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ghinbox/internal/profiles"
	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// Changes fetches a pull request (pull_request_read get) and its files
// (get_files, paged); when the server omits per-file patches it falls back to
// the unified diff (get_diff) and splits it per file.
func (s *Source) Changes(ctx context.Context, repo string, number int) (source.ChangeSet, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return source.ChangeSet{}, fmt.Errorf("changes: bad repo %q", repo)
	}
	raw, err := s.client.PullRequestRead(ctx, owner, name, number, "get")
	if err != nil {
		return source.ChangeSet{}, fmt.Errorf("changes %s#%d: %w", repo, number, err)
	}
	var pr struct {
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		State     string    `json:"state"`
		Merged    bool      `json:"merged"`
		MergedAt  *string   `json:"merged_at"`
		Draft     bool      `json:"draft"`
		HTMLURL   string    `json:"html_url"`
		CreatedAt string    `json:"created_at"`
		UpdatedAt string    `json:"updated_at"`
		Labels    labelList `json:"labels"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		ChangedFiles int `json:"changed_files"`
		Additions    int `json:"additions"`
		Deletions    int `json:"deletions"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return source.ChangeSet{}, fmt.Errorf("changes %s#%d: decode: %w", repo, number, err)
	}
	cs := source.ChangeSet{
		Forge: store.ForgeGitHub, Repo: repo, Number: number, Kind: "pr", Title: pr.Title, Body: source.TrimText(pr.Body, 4000),
		Labels: []string(pr.Labels), Base: pr.Base.Ref, Draft: pr.Draft, Author: pr.User.Login, HeadSHA: pr.Head.SHA, HTMLURL: pr.HTMLURL,
		FilesTotal: pr.ChangedFiles, Additions: pr.Additions, Deletions: pr.Deletions,
	}
	cs.State = pr.State
	if pr.Merged || pr.MergedAt != nil {
		cs.State = "merged"
		if pr.MergedAt != nil {
			if t, err := time.Parse(time.RFC3339, *pr.MergedAt); err == nil {
				t = t.UTC()
				cs.MergedAt = &t
			}
		}
	}
	cs.CreatedAt, _ = time.Parse(time.RFC3339, pr.CreatedAt)
	cs.UpdatedAt, _ = time.Parse(time.RFC3339, pr.UpdatedAt)

	// Files: REST-shaped list with optional patches.
	for page := 1; page <= 3 && len(cs.Files) < source.MaxChangeFiles; page++ {
		fraw, err := s.client.PullRequestReadPage(ctx, owner, name, number, "get_files", page, 100)
		if err != nil {
			return cs, fmt.Errorf("changes %s#%d files: %w", repo, number, err)
		}
		var files []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
			Patch     string `json:"patch"`
		}
		if err := json.Unmarshal(fraw, &files); err != nil {
			return cs, fmt.Errorf("changes %s#%d files: decode: %w", repo, number, err)
		}
		for _, f := range files {
			cs.Files = append(cs.Files, profiles.FileChange{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
		}
		if len(files) < 100 {
			break
		}
	}
	if len(cs.Files) >= source.MaxChangeFiles || (cs.FilesTotal > 0 && len(cs.Files) < cs.FilesTotal) {
		cs.Truncated = true
	}
	if cs.FilesTotal == 0 {
		cs.FilesTotal = len(cs.Files)
	}
	// Fallback: no patches at all → unified diff split per file.
	if len(cs.Files) > 0 && len(cs.Files) <= 60 && !anyPatch(cs.Files) {
		if draw, err := s.client.PullRequestRead(ctx, owner, name, number, "get_diff"); err == nil {
			diff := string(draw)
			var asStr string
			if json.Unmarshal(draw, &asStr) == nil {
				diff = asStr
			}
			patches := splitUnifiedDiff(diff)
			for i := range cs.Files {
				if p, ok := patches[cs.Files[i].Path]; ok {
					cs.Files[i].Patch = p
				}
			}
		}
	}
	return cs, nil
}

func anyPatch(files []profiles.FileChange) bool {
	for _, f := range files {
		if f.Patch != "" {
			return true
		}
	}
	return false
}

// splitUnifiedDiff maps file paths to their hunks from a git unified diff.
func splitUnifiedDiff(diff string) map[string]string {
	out := map[string]string{}
	var cur string
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			out[cur] = buf.String()
		}
		buf.Reset()
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			cur = ""
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				cur = strings.TrimPrefix(parts[3], "b/")
			}
			continue
		}
		if cur != "" && (strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ")) &&
			!strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "--- ") {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return out
}

// RecentlyMerged lists pull requests merged after since (newest first).
func (s *Source) RecentlyMerged(ctx context.Context, repo string, since time.Time, limit int) ([]source.ChangeRef, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("recently merged: bad repo %q", repo)
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	raw, err := s.client.ListPullRequests(ctx, owner, name, "closed", "updated", "desc", limit, []string{"number", "title", "html_url", "merged_at", "updated_at", "user", "state"})
	if err != nil {
		return nil, fmt.Errorf("recently merged %s: %w", repo, err)
	}
	var prs []struct {
		Number    int     `json:"number"`
		Title     string  `json:"title"`
		HTMLURL   string  `json:"html_url"`
		MergedAt  *string `json:"merged_at"`
		UpdatedAt string  `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &prs); err != nil {
		return nil, fmt.Errorf("recently merged %s: decode: %w", repo, err)
	}
	var out []source.ChangeRef
	for _, pr := range prs {
		if pr.MergedAt == nil {
			continue
		}
		m, err := time.Parse(time.RFC3339, *pr.MergedAt)
		if err != nil || !m.After(since) {
			continue
		}
		m = m.UTC()
		u, _ := time.Parse(time.RFC3339, pr.UpdatedAt)
		out = append(out, source.ChangeRef{Repo: repo, Number: pr.Number, Title: pr.Title, HTMLURL: pr.HTMLURL, Author: pr.User.Login, MergedAt: &m, UpdatedAt: u.UTC()})
	}
	return out, nil
}

var _ source.Changer = (*Source)(nil)
var _ source.LandedLister = (*Source)(nil)
