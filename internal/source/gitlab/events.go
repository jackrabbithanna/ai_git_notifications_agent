package gitlab

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"ghinbox/internal/source"
	"ghinbox/internal/store"
)

// maxEventPages caps how far back one sync reads per watched project.
const maxEventPages = 5

// SyncWatched polls each watched project's events and folds them into one
// thread per issue/MR (newest event wins). Pending to-dos take precedence, so
// an item that already has a to-do produces no event thread.
func (s *Source) SyncWatched(ctx context.Context, watched []store.WatchedProject, accountID int64) ([]store.Thread, []source.WatchUpdate, []string, error) {
	me, err := s.user(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	s.mu.Lock()
	todoTargets := s.todoTargets
	s.mu.Unlock()

	var threads []store.Thread
	var updates []source.WatchUpdate
	var warnings []string
	now := time.Now()

	for _, w := range watched {
		upd := source.WatchUpdate{Path: w.Path, ProjectID: w.ProjectID}
		path := w.Path
		if w.ProjectID == 0 {
			p, _, err := s.c.Projects.GetProject(w.Path, nil, gl.WithContext(ctx))
			if err != nil {
				upd.Err = "resolve project: " + translate(err).Error()
				warnings = append(warnings, w.Path+": "+upd.Err)
				updates = append(updates, upd)
				continue
			}
			upd.ProjectID = p.ID
			if p.PathWithNamespace != "" {
				path = p.PathWithNamespace
			}
		}
		since := now.Add(-7 * 24 * time.Hour)
		if w.LastEventAt != nil {
			since = *w.LastEventAt
		}
		after := gl.ISOTime(since.Add(-24 * time.Hour))
		opt := &gl.ListProjectVisibleEventsOptions{After: &after, Sort: ptr("asc"), ListOptions: gl.ListOptions{PerPage: 100, Page: 1}}
		type agg struct {
			tg      target
			newest  *gl.ProjectEvent
			newestT time.Time
			isNote  bool
			noteID  int64
			noteTyp string
			title   string
		}
		groups := map[string]*agg{}
		var order []string
		newestSeen := since
		for page := 1; page <= maxEventPages; page++ {
			opt.Page = int64(page)
			events, resp, err := s.c.Events.ListProjectVisibleEvents(upd.ProjectID, opt, gl.WithContext(ctx))
			if err != nil {
				upd.Err = "events: " + translate(err).Error()
				warnings = append(warnings, w.Path+": "+upd.Err)
				break
			}
			for _, ev := range events {
				t, err := time.Parse(time.RFC3339, ev.CreatedAt)
				if err != nil {
					continue
				}
				if !t.After(since) {
					continue
				}
				if t.After(newestSeen) {
					newestSeen = t
				}
				tg, isNote, noteTyp, ok := eventTarget(ev, upd.ProjectID, path)
				if !ok {
					continue
				}
				k := tg.key()
				if todoTargets[k] {
					continue
				}
				g, exists := groups[k]
				if !exists {
					g = &agg{tg: tg}
					groups[k] = g
					order = append(order, k)
				}
				if g.newest == nil || t.After(g.newestT) {
					g.newest, g.newestT, g.isNote, g.noteTyp = ev, t, isNote, noteTyp
					g.noteID = ev.Note.ID
					if ev.TargetTitle != "" {
						g.title = ev.TargetTitle
					}
				}
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
		}
		for _, k := range order {
			g := groups[k]
			threads = append(threads, s.eventThread(accountID, path, g.tg, g.newest, g.newestT, g.isNote, g.noteTyp, g.noteID, g.title, me.Username))
		}
		if upd.Err == "" {
			upd.LastEventAt = &newestSeen
		}
		updates = append(updates, upd)
	}
	return threads, updates, warnings, nil
}

// eventTarget resolves an event to its issue/MR. Pushes, milestones, wiki and
// membership events have no such target and are dropped.
func eventTarget(ev *gl.ProjectEvent, projectID int64, path string) (tg target, isNote bool, noteType string, ok bool) {
	switch ev.TargetType {
	case "Issue", "MergeRequest":
		if ev.TargetIID == 0 {
			return target{}, false, "", false
		}
		return target{projectID: projectID, repo: path, kind: ev.TargetType, iid: ev.TargetIID}, false, "", true
	case "Note", "DiffNote", "DiscussionNote":
		kind := ev.Note.NoteableType
		if kind != "Issue" && kind != "MergeRequest" {
			return target{}, false, "", false
		}
		iid := ev.Note.NoteableIID
		if iid == 0 {
			iid = ev.TargetIID
		}
		if iid == 0 {
			return target{}, false, "", false
		}
		return target{projectID: projectID, repo: path, kind: kind, iid: iid}, true, ev.TargetType, true
	}
	return target{}, false, "", false
}

func (s *Source) eventThread(accountID int64, path string, tg target, ev *gl.ProjectEvent, at time.Time, isNote bool, noteType string, noteID int64, title, me string) store.Thread {
	seg := "issues"
	if tg.kind == "MergeRequest" {
		seg = "merge_requests"
	}
	htmlURL := fmt.Sprintf("https://%s/%s/-/%s/%d", s.host, path, seg, tg.iid)
	latest := ""
	if isNote && noteID != 0 {
		htmlURL += "#note_" + strconv.FormatInt(noteID, 10)
		latest = htmlURL
	}
	actor := ev.Author.Username
	if actor == "" {
		actor = ev.AuthorUsername
	}
	if title == "" {
		title = strings.TrimSpace(ev.Title)
	}
	if title == "" {
		title = fmt.Sprintf("%s %s %d", tg.kind, ev.ActionName, tg.iid)
	}
	kind, rels := classifyEvent(ev.ActionName, tg.kind, noteType, actor == me)
	return store.Thread{
		AccountID:        accountID,
		ThreadID:         fmt.Sprintf("gl:%d:%s:%d", tg.projectID, tg.kind, tg.iid),
		Repo:             path,
		SubjectType:      tg.kind,
		SubjectURL:       htmlURL,
		SubjectNumber:    int(tg.iid),
		HTMLURL:          htmlURL,
		Title:            title,
		Reason:           ev.ActionName,
		Actor:            actor,
		Unread:           true,
		UpdatedAt:        at.UTC(),
		LatestCommentURL: latest,
		ActivityKind:     string(kind),
		RelationTags:     rels,
	}
}

var _ source.Watcher = (*Source)(nil)
