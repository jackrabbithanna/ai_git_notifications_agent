package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitinbox/internal/classify"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	reqs []string // "METHOD path?query"
}

func (r *recorder) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
}

func (r *recorder) nonGET() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, x := range r.reqs {
		if !strings.HasPrefix(x, "GET ") {
			out = append(out, x)
		}
	}
	return out
}

func ts(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

// fakeGitLab serves the subset of /api/v4 the source uses.
func fakeGitLab(t *testing.T) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	todoIssue := map[string]any{"id": 1, "action_name": "assigned", "target_type": "Issue", "state": "pending", "created_at": ts(-2 * time.Hour),
		"project": map[string]any{"id": 42, "path_with_namespace": "dev/core"}, "author": map[string]any{"username": "alice"},
		"target":     map[string]any{"iid": 5, "title": "Issue five", "web_url": "https://lab.example.org/dev/core/-/issues/5", "state": "opened"},
		"target_url": "https://lab.example.org/dev/core/-/issues/5"}
	todoMR := map[string]any{"id": 2, "action_name": "mentioned", "target_type": "MergeRequest", "state": "pending", "created_at": ts(-1 * time.Hour),
		"project": map[string]any{"id": 42, "path_with_namespace": "dev/core"}, "author": map[string]any{"username": "bob"},
		"target":     map[string]any{"iid": 9, "title": "MR nine", "web_url": "https://lab.example.org/dev/core/-/merge_requests/9", "state": "opened"},
		"target_url": "https://lab.example.org/dev/core/-/merge_requests/9#note_11"}
	todoDone := map[string]any{"id": 3, "action_name": "review_requested", "target_type": "MergeRequest", "state": "done", "created_at": ts(-3 * time.Hour),
		"project": map[string]any{"id": 42, "path_with_namespace": "dev/core"}, "author": map[string]any{"username": "carol"},
		"target":     map[string]any{"iid": 4, "title": "MR four", "web_url": "https://lab.example.org/dev/core/-/merge_requests/4", "state": "opened"},
		"target_url": "https://lab.example.org/dev/core/-/merge_requests/4"}
	issue := func(iid int, title string) map[string]any {
		return map[string]any{"id": iid * 100, "iid": iid, "title": title, "state": "opened", "web_url": fmt.Sprintf("https://lab.example.org/dev/core/-/issues/%d", iid),
			"author": map[string]any{"username": "me"}, "assignees": []map[string]any{{"username": "me"}}, "labels": []string{"bug"}, "user_notes_count": 2,
			"created_at": ts(-48 * time.Hour), "updated_at": ts(-1 * time.Hour), "references": map[string]any{"full": fmt.Sprintf("dev/core#%d", iid)}}
	}
	mr := func(iid int, title string) map[string]any {
		return map[string]any{"id": iid * 100, "iid": iid, "title": title, "state": "opened", "web_url": fmt.Sprintf("https://lab.example.org/dev/core/-/merge_requests/%d", iid),
			"author": map[string]any{"username": "dave"}, "assignees": []map[string]any{}, "labels": []string{}, "draft": iid == 4, "user_notes_count": 0,
			"created_at": ts(-72 * time.Hour), "updated_at": ts(-2 * time.Hour), "references": map[string]any{"full": fmt.Sprintf("dev/core!%d", iid)}}
	}
	events := []map[string]any{
		{"id": 1, "project_id": 42, "action_name": "opened", "target_type": "Issue", "target_iid": 5, "target_title": "Issue five", "author": map[string]any{"username": "alice"}, "created_at": ts(-5 * time.Hour)},
		{"id": 2, "project_id": 42, "action_name": "commented on", "target_type": "Note", "target_iid": 0, "target_title": "Issue eight", "author": map[string]any{"username": "bob"}, "created_at": ts(-4 * time.Hour),
			"note": map[string]any{"id": 77, "noteable_type": "Issue", "noteable_iid": 8, "body": "hi"}},
		{"id": 3, "project_id": 42, "action_name": "merged", "target_type": "MergeRequest", "target_iid": 3, "target_title": "MR three", "author": map[string]any{"username": "me"}, "created_at": ts(-3 * time.Hour)},
		{"id": 4, "project_id": 42, "action_name": "commented on", "target_type": "DiffNote", "target_iid": 0, "target_title": "MR four", "author": map[string]any{"username": "erin"}, "created_at": ts(-2 * time.Hour),
			"note": map[string]any{"id": 78, "noteable_type": "MergeRequest", "noteable_iid": 4, "body": "nit"}},
		{"id": 5, "project_id": 42, "action_name": "pushed to", "target_type": nil, "author": map[string]any{"username": "me"}, "created_at": ts(-1 * time.Hour), "push_data": map[string]any{"ref": "main"}},
		{"id": 6, "project_id": 42, "action_name": "closed", "target_type": "Issue", "target_iid": 8, "target_title": "Issue eight", "author": map[string]any{"username": "alice"}, "created_at": ts(-30 * time.Minute)},
	}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/", func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-test" {
			w.WriteHeader(http.StatusUnauthorized)
			write(w, map[string]any{"message": "401 Unauthorized"})
			return
		}
		q := r.URL.Query()
		p := r.URL.Path
		switch {
		case p == "/api/v4/user":
			write(w, map[string]any{"id": 7, "username": "me", "name": "Me"})
		case p == "/api/v4/personal_access_tokens/self":
			write(w, map[string]any{"id": 1, "scopes": []string{"read_api"}})
		case p == "/api/v4/todos" && r.Method == http.MethodGet:
			switch {
			case q.Get("action") == "mentioned":
				write(w, []any{todoMR})
			case q.Get("action") == "directly_addressed":
				write(w, []any{})
			case q.Get("state") == "done":
				write(w, []any{todoDone})
			default:
				write(w, []any{todoIssue, todoMR})
			}
		case p == "/api/v4/todos/1/mark_as_done" && r.Method == http.MethodPost:
			write(w, todoIssue)
		case p == "/api/v4/issues":
			switch q.Get("scope") {
			case "assigned_to_me":
				write(w, []any{issue(5, "Issue five")})
			case "created_by_me":
				write(w, []any{issue(6, "Issue six")})
			default:
				write(w, []any{})
			}
		case p == "/api/v4/merge_requests":
			switch {
			case q.Get("reviewer_id") == "7":
				write(w, []any{mr(4, "MR four")})
			case q.Get("scope") == "created_by_me":
				write(w, []any{mr(3, "MR three")})
			default:
				write(w, []any{})
			}
		case p == "/api/v4/projects/dev/core" && r.Method == http.MethodGet:
			write(w, map[string]any{"id": 42, "path_with_namespace": "dev/core"})
		case p == "/api/v4/projects/42/events":
			write(w, events)
		case strings.HasPrefix(p, "/api/v4/projects/dev/core/merge_requests/3/diffs"):
			write(w, []map[string]any{
				{"old_path": "CRM/Core/BAO/X.php", "new_path": "CRM/Core/BAO/X.php", "diff": "@@ -1 +1 @@\n-  public static function create(&$params) {\n+  public static function create(array $params) {\n", "new_file": false, "renamed_file": false, "deleted_file": false},
				{"old_path": "", "new_path": "docs/new.md", "diff": "@@ -0,0 +1 @@\n+hi\n", "new_file": true, "renamed_file": false, "deleted_file": false},
			})
		case strings.HasPrefix(p, "/api/v4/projects/dev/core/merge_requests/3"):
			write(w, map[string]any{"iid": 3, "title": "BAO create signature", "description": "BREAKING", "state": "merged", "sha": "deadbeef", "draft": false, "target_branch": "master",
				"web_url": "https://lab.example.org/dev/core/-/merge_requests/3", "labels": []string{"api"}, "author": map[string]any{"username": "dave"},
				"created_at": ts(-72 * time.Hour), "updated_at": ts(-1 * time.Hour), "merged_at": ts(-2 * time.Hour)})
		case strings.HasPrefix(p, "/api/v4/projects/dev/core/merge_requests") && q.Get("state") == "merged":
			write(w, []map[string]any{{"iid": 3, "title": "BAO create signature", "web_url": "https://lab.example.org/dev/core/-/merge_requests/3", "merged_at": ts(-2 * time.Hour), "updated_at": ts(-1 * time.Hour), "author": map[string]any{"username": "dave"}}})
		case p == "/api/v4/projects/42/issues/8/unsubscribe" && r.Method == http.MethodPost:
			write(w, issue(8, "Issue eight"))
		default:
			w.WriteHeader(http.StatusNotFound)
			write(w, map[string]any{"message": "404 Not Found: " + r.Method + " " + p})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, rec
}

func newSource(t *testing.T, srv *httptest.Server, writes bool) *Source {
	t.Helper()
	s, err := New(Config{Host: "lab.example.org", Token: "glpat-test", Writes: writes, BaseURL: srv.URL + "/api/v4/"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoginAndScopes(t *testing.T) {
	srv, _ := fakeGitLab(t)
	s := newSource(t, srv, false)
	login, err := s.Login(context.Background())
	if err != nil || login != "me" {
		t.Fatalf("login=%q err=%v", login, err)
	}
	sc, err := s.TokenScopes(context.Background())
	if err != nil || sc != "read_api" {
		t.Fatalf("scopes=%q err=%v", sc, err)
	}
	bad, _ := New(Config{Host: "lab.example.org", Token: "wrong", BaseURL: srv.URL + "/api/v4/"})
	if _, err := bad.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 translation, got %v", err)
	}
}

func TestNotificationsTodos(t *testing.T) {
	srv, _ := fakeGitLab(t)
	s := newSource(t, srv, false)
	res, err := s.Notifications(context.Background(), source.Opts{AccountID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Threads) != 2 {
		t.Fatalf("want 2 pending threads, got %d", len(res.Threads))
	}
	a, m := res.Threads[0], res.Threads[1]
	if a.ThreadID != "todo:1" || a.Repo != "dev/core" || a.SubjectType != "Issue" || a.SubjectNumber != 5 || a.ActivityKind != string(classify.KindAssignment) ||
		a.RelationTags[0] != classify.RelAssignee || a.Actor != "alice" || !a.Unread || a.AccountID != 9 || a.LatestCommentURL != "" {
		t.Fatalf("assigned todo: %+v", a)
	}
	if m.ThreadID != "todo:2" || m.SubjectType != "MergeRequest" || m.ActivityKind != string(classify.KindMention) || m.LatestCommentURL == "" ||
		!strings.HasSuffix(m.HTMLURL, "#note_11") || m.Title != "MR nine" {
		t.Fatalf("mention todo: %+v", m)
	}
	if res.ReadExcept == nil || res.ReadExcept.Prefix != "todo:" || len(res.ReadExcept.IDs) != 2 {
		t.Fatalf("read-except: %+v", res.ReadExcept)
	}
	full, err := s.Notifications(context.Background(), source.Opts{AccountID: 9, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Threads) != 3 || full.Threads[2].Unread || full.Threads[2].ThreadID != "todo:3" {
		t.Fatalf("full sync should include the recently done to-do as read: %+v", full.Threads)
	}
}

func TestMine(t *testing.T) {
	srv, _ := fakeGitLab(t)
	s := newSource(t, srv, false)
	items, err := s.Mine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.Item{}
	for _, it := range items {
		got[fmt.Sprintf("%s|%s|%d", it.Repo, it.Kind, it.Number)] = it
	}
	want := map[string]string{
		"dev/core|issue|5": "assigned",
		"dev/core|issue|6": "author",
		"dev/core|mr|3":    "author",
		"dev/core|mr|4":    "review_requested",
		"dev/core|mr|9":    "mentioned",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d items: %+v", len(got), items)
	}
	for k, rel := range want {
		it, ok := got[k]
		if !ok || len(it.Relations) != 1 || it.Relations[0] != rel {
			t.Fatalf("%s: %+v", k, it)
		}
	}
	if !got["dev/core|mr|4"].Draft || got["dev/core|issue|5"].Labels[0] != "bug" || got["dev/core|issue|5"].Comments != 2 {
		t.Fatalf("item details: %+v", got)
	}
	for k, it := range got {
		if it.State != "open" {
			t.Fatalf("%s: GitLab 'opened' must normalise to 'open', got %q", k, it.State)
		}
	}
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	srv, rec := fakeGitLab(t)
	s := newSource(t, srv, false)
	if _, err := s.Notifications(context.Background(), source.Opts{}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDone(context.Background(), "todo:1"); !errors.Is(err, source.ErrWritesDisabled) {
		t.Fatalf("MarkDone in readonly: %v", err)
	}
	if err := s.Unsubscribe(context.Background(), "todo:1"); !errors.Is(err, source.ErrWritesDisabled) {
		t.Fatalf("Unsubscribe in readonly: %v", err)
	}
	if err := s.MarkRead(context.Background(), "todo:1"); !errors.Is(err, source.ErrNotMirrorable) {
		t.Fatalf("MarkRead: %v", err)
	}
	if err := s.MarkDone(context.Background(), "gl:42:Issue:8"); !errors.Is(err, source.ErrNotMirrorable) {
		t.Fatalf("MarkDone on event thread: %v", err)
	}
	if ng := rec.nonGET(); len(ng) != 0 {
		t.Fatalf("readonly source issued non-GET requests: %v", ng)
	}
}

func TestWritesWhenEnabled(t *testing.T) {
	srv, rec := fakeGitLab(t)
	s := newSource(t, srv, true)
	if _, err := s.Notifications(context.Background(), source.Opts{}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDone(context.Background(), "todo:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe(context.Background(), "gl:42:Issue:8"); err != nil {
		t.Fatal(err)
	}
	ng := rec.nonGET()
	if len(ng) != 2 || !strings.HasPrefix(ng[0], "POST /api/v4/todos/1/mark_as_done") || !strings.HasPrefix(ng[1], "POST /api/v4/projects/42/issues/8/unsubscribe") {
		t.Fatalf("writes: %v", ng)
	}
}

func TestSyncWatched(t *testing.T) {
	srv, _ := fakeGitLab(t)
	s := newSource(t, srv, false)
	if _, err := s.Notifications(context.Background(), source.Opts{AccountID: 9}); err != nil {
		t.Fatal(err)
	}
	threads, updates, warns, err := s.SyncWatched(context.Background(), []store.WatchedProject{{AccountID: 9, Path: "dev/core"}}, 9)
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if len(updates) != 1 || updates[0].ProjectID != 42 || updates[0].LastEventAt == nil || updates[0].Err != "" {
		t.Fatalf("updates: %+v", updates)
	}
	byID := map[string]store.Thread{}
	for _, th := range threads {
		byID[th.ThreadID] = th
	}
	if _, dup := byID["gl:42:Issue:5"]; dup {
		t.Fatal("issue 5 has a pending to-do; event thread must be suppressed")
	}
	if len(byID) != 3 {
		t.Fatalf("want 3 event threads, got %d: %v", len(byID), threads)
	}
	i8 := byID["gl:42:Issue:8"]
	if i8.Reason != "closed" || i8.ActivityKind != string(classify.KindStateChange) || i8.Actor != "alice" || i8.Title != "Issue eight" ||
		i8.HTMLURL != "https://lab.example.org/dev/core/-/issues/8" || i8.LatestCommentURL != "" {
		t.Fatalf("issue 8 (newest event wins): %+v", i8)
	}
	m3 := byID["gl:42:MergeRequest:3"]
	if m3.ActivityKind != string(classify.KindStateChange) || m3.RelationTags[0] != classify.RelAuthor || m3.SubjectNumber != 3 {
		t.Fatalf("mr 3: %+v", m3)
	}
	m4 := byID["gl:42:MergeRequest:4"]
	if m4.ActivityKind != string(classify.KindReview) || !strings.HasSuffix(m4.HTMLURL, "/merge_requests/4#note_78") || m4.LatestCommentURL == "" || m4.Actor != "erin" {
		t.Fatalf("mr 4 diff note: %+v", m4)
	}
	// Second pass with the stored cursor sees nothing new.
	threads2, _, _, err := s.SyncWatched(context.Background(), []store.WatchedProject{{AccountID: 9, Path: "dev/core", ProjectID: 42, LastEventAt: updates[0].LastEventAt}}, 9)
	if err != nil || len(threads2) != 0 {
		t.Fatalf("second pass: %d threads, err=%v", len(threads2), err)
	}
}

func TestClassifyTables(t *testing.T) {
	if k, r := classifyTodo("build_failed", "MergeRequest"); k != classify.KindCI || r[0] != classify.RelAuthor {
		t.Fatalf("build_failed: %s %v", k, r)
	}
	if k, _ := classifyTodo("whatever", "Alert"); k != classify.KindSecurity {
		t.Fatalf("alert: %s", k)
	}
	if k, _ := classifyEvent("opened", "MergeRequest", "", false); k != classify.KindNewPR {
		t.Fatalf("opened mr: %s", k)
	}
	if k, _ := classifyEvent("commented on", "Issue", "Note", false); k != classify.KindComment {
		t.Fatalf("issue note: %s", k)
	}
	if repoFromWebURL("https://lab.example.org/dev/core/-/issues/5") != "dev/core" || repoFromWebURL("https://gitlab.com/group/sub/proj/-/merge_requests/1") != "group/sub/proj" {
		t.Fatal("repoFromWebURL")
	}
}

func TestChangesAndRecentlyMerged(t *testing.T) {
	srv, _ := fakeGitLab(t)
	s := newSource(t, srv, false)
	cs, err := s.Changes(context.Background(), "dev/core", 3)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Kind != "mr" || cs.State != "merged" || cs.MergedAt == nil || cs.HeadSHA != "deadbeef" || cs.Base != "master" || cs.Author != "dave" || cs.Labels[0] != "api" {
		t.Fatalf("changeset: %+v", cs)
	}
	if len(cs.Files) != 2 || cs.Files[0].Additions != 1 || cs.Files[0].Deletions != 1 || cs.Files[1].Status != "added" || cs.Files[1].Path != "docs/new.md" || cs.Additions != 2 {
		t.Fatalf("files: %+v", cs.Files)
	}
	refs, err := s.RecentlyMerged(context.Background(), "dev/core", time.Now().Add(-24*time.Hour), 10)
	if err != nil || len(refs) != 1 || refs[0].Number != 3 || refs[0].MergedAt == nil {
		t.Fatalf("recently merged: %+v %v", refs, err)
	}
	e, err := s.Enrich(context.Background(), store.Thread{Repo: "dev/core", SubjectType: "MergeRequest", SubjectNumber: 3, UpdatedAt: time.Now()})
	if err != nil || e.ItemCreatedAt == nil || e.ItemState != "merged" || e.ItemAuthor != "dave" {
		t.Fatalf("enrich mr: %+v %v", e, err)
	}
}
