// Package gitlab is the GitLab Source: the To-Do list stands in for
// notifications, watched projects supply activity (GitLab has no notifications
// feed), and issue/MR queries feed "mine". It talks REST through the official
// client; read-only mode is enforced in code (only GETs are issued).
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go/v3"

	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Config describes one account's connection.
type Config struct {
	Host       string // e.g. gitlab.com, lab.civicrm.org
	Token      string
	Writes     bool         // write mode "notifications": mark to-dos done, unsubscribe
	BaseURL    string       // override (tests); default https://<host>/api/v4/
	HTTPClient *http.Client // override (tests)
}

// target identifies an issue/MR inside a project.
type target struct {
	projectID int64
	repo      string
	kind      string // Issue | MergeRequest
	iid       int64
}

// Source is one account's GitLab connection.
type Source struct {
	host   string
	writes bool
	c      *gl.Client

	mu          sync.Mutex
	me          *gl.User
	todoTargets map[string]bool  // "<repo>|<kind>|<iid>" of pending to-dos (event precedence)
	todoInfo    map[int64]target // to-do id → target (for unsubscribe)
}

// New builds the source. No request is made until a method is called.
func New(cfg Config) (*Source, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://" + strings.TrimSuffix(cfg.Host, "/") + "/api/v4/"
	}
	opts := []gl.ClientOptionFunc{gl.WithBaseURL(base)}
	if cfg.HTTPClient != nil {
		opts = append(opts, gl.WithHTTPClient(cfg.HTTPClient))
	}
	c, err := gl.NewClient(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("gitlab: client for %s: %w", cfg.Host, err)
	}
	return &Source{host: cfg.Host, writes: cfg.Writes, c: c, todoTargets: map[string]bool{}, todoInfo: map[int64]target{}}, nil
}

// Factory adapts New to the pipeline's constructor signature.
func Factory() func(acct store.Account, token string) (source.Source, error) {
	return func(acct store.Account, token string) (source.Source, error) {
		return New(Config{Host: acct.Host, Token: token, Writes: acct.WriteMode == "notifications"})
	}
}

func (s *Source) Forge() string { return store.ForgeGitLab }

// WriteMode lets the pipeline detect a stale source after a mode change.
func (s *Source) WriteMode() string {
	if s.writes {
		return "notifications"
	}
	return "readonly"
}

func (s *Source) Close() error { return nil }

func (s *Source) user(ctx context.Context) (*gl.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.me != nil {
		return s.me, nil
	}
	u, _, err := s.c.Users.CurrentUser(gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("gitlab: GET /user on %s: %w", s.host, translate(err))
	}
	s.me = u
	return u, nil
}

func (s *Source) Login(ctx context.Context) (string, error) {
	u, err := s.user(ctx)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// TokenScopes reads the token's scopes (GitLab 15.5+; "" when unavailable).
func (s *Source) TokenScopes(ctx context.Context) (string, error) {
	t, _, err := s.c.PersonalAccessTokens.GetSinglePersonalAccessToken(gl.WithContext(ctx))
	if err != nil {
		return "", translate(err)
	}
	return strings.Join(t.Scopes, ","), nil
}

// translate turns client errors into short, actionable messages.
func translate(err error) error {
	var e *gl.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		switch e.Response.StatusCode {
		case http.StatusUnauthorized:
			return errors.New("token rejected (401)")
		case http.StatusForbidden:
			return errors.New("token lacks the required scope or role (403)")
		case http.StatusNotFound:
			return errors.New("not found (404) — wrong host, missing permission, or an API this GitLab version lacks")
		case http.StatusTooManyRequests:
			return errors.New("rate limited (429); backing off")
		}
	}
	return err
}

func ptr[T any](v T) *T { return &v }

// --- to-dos -----------------------------------------------------------------

// Notifications lists pending to-dos (and, on full syncs, recently done ones).
func (s *Source) Notifications(ctx context.Context, opts source.Opts) (source.Result, error) {
	var res source.Result
	targets := map[string]bool{}
	info := map[int64]target{}
	var pendingIDs []string

	pending, err := s.listTodos(ctx, "pending", 20)
	if err != nil {
		return res, err
	}
	for _, t := range pending {
		th, tg := s.todoThread(opts.AccountID, t)
		res.Threads = append(res.Threads, th)
		pendingIDs = append(pendingIDs, th.ThreadID)
		if tg.iid != 0 {
			targets[tg.key()] = true
			info[t.ID] = tg
		}
	}
	if opts.Full {
		done, err := s.listTodos(ctx, "done", 3)
		if err != nil {
			res.Warnings = append(res.Warnings, "done to-dos: "+err.Error())
		}
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for _, t := range done {
			if t.CreatedAt != nil && t.CreatedAt.Before(cutoff) {
				continue
			}
			th, tg := s.todoThread(opts.AccountID, t)
			res.Threads = append(res.Threads, th)
			if tg.iid != 0 {
				info[t.ID] = tg
			}
		}
	}
	res.ReadExcept = &source.ReadExcept{Prefix: "todo:", IDs: pendingIDs}

	s.mu.Lock()
	s.todoTargets = targets
	s.todoInfo = info
	s.mu.Unlock()
	return res, nil
}

func (s *Source) listTodos(ctx context.Context, state string, maxPages int) ([]*gl.Todo, error) {
	opt := &gl.ListTodosOptions{State: ptr(state), ListOptions: gl.ListOptions{PerPage: 100, Page: 1}}
	var out []*gl.Todo
	for page := 1; page <= maxPages; page++ {
		opt.Page = int64(page)
		todos, resp, err := s.c.Todos.ListTodos(opt, gl.WithContext(ctx))
		if err != nil {
			return out, fmt.Errorf("gitlab: GET /todos?state=%s: %w", state, translate(err))
		}
		out = append(out, todos...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
	}
	return out, nil
}

func (t target) key() string { return t.repo + "|" + t.kind + "|" + strconv.FormatInt(t.iid, 10) }

// normalizeTargetType strips GitLab's namespaced type names to a short kind.
func normalizeTargetType(tt string) string {
	if i := strings.LastIndex(tt, "::"); i >= 0 {
		return tt[i+2:]
	}
	return tt
}

func (s *Source) todoThread(accountID int64, t *gl.Todo) (store.Thread, target) {
	repo := ""
	var pid int64
	if t.Project != nil {
		repo = t.Project.PathWithNamespace
		pid = t.Project.ID
	}
	kind := normalizeTargetType(string(t.TargetType))
	title := t.Body
	var iid int64
	webURL := ""
	if t.Target != nil {
		iid = t.Target.IID
		if t.Target.Title != "" {
			title = t.Target.Title
		}
		webURL = t.Target.WebURL
	}
	htmlURL := t.TargetURL
	if htmlURL == "" {
		htmlURL = webURL
	}
	latest := ""
	if strings.Contains(htmlURL, "#note_") {
		latest = htmlURL
	}
	actor := ""
	if t.Author != nil {
		actor = t.Author.Username
	}
	var updated time.Time
	if t.CreatedAt != nil {
		updated = t.CreatedAt.UTC()
	}
	ak, rels := classifyTodo(string(t.ActionName), kind)
	th := store.Thread{
		AccountID:        accountID,
		ThreadID:         "todo:" + strconv.FormatInt(t.ID, 10),
		Repo:             repo,
		SubjectType:      kind,
		SubjectURL:       webURL,
		SubjectNumber:    int(iid),
		HTMLURL:          htmlURL,
		Title:            title,
		Reason:           string(t.ActionName),
		Actor:            actor,
		Unread:           t.State == "pending",
		UpdatedAt:        updated,
		LatestCommentURL: latest,
		ActivityKind:     string(ak),
		RelationTags:     rels,
	}
	return th, target{projectID: pid, repo: repo, kind: kind, iid: iid}
}

// --- mine -------------------------------------------------------------------

// Mine merges assigned / authored / reviewer / mentioned issues and MRs.
func (s *Source) Mine(ctx context.Context) ([]store.Item, error) {
	me, err := s.user(ctx)
	if err != nil {
		return nil, err
	}
	merged := map[string]*store.Item{}
	var order []string
	add := func(rel string, it store.Item) {
		k := it.Repo + "|" + it.Kind + "|" + strconv.Itoa(it.Number)
		cur, ok := merged[k]
		if !ok {
			c := it
			cur = &c
			merged[k] = cur
			order = append(order, k)
		}
		cur.Relations = source.AppendUnique(cur.Relations, rel)
	}
	wc := gl.WithContext(ctx)
	lo := gl.ListOptions{PerPage: 100, Page: 1}

	for _, q := range []struct{ scope, rel string }{{"assigned_to_me", "assigned"}, {"created_by_me", "author"}} {
		issues, _, err := s.c.Issues.ListIssues(&gl.ListIssuesOptions{Scope: ptr(q.scope), State: ptr("opened"), ListOptions: lo}, wc)
		if err != nil {
			return nil, fmt.Errorf("gitlab: issues %s: %w", q.scope, translate(err))
		}
		for _, is := range issues {
			add(q.rel, issueItem(is, s.host))
		}
		mrs, _, err := s.c.MergeRequests.ListMergeRequests(&gl.ListMergeRequestsOptions{Scope: ptr(q.scope), State: ptr("opened"), ListOptions: lo}, wc)
		if err != nil {
			return nil, fmt.Errorf("gitlab: merge requests %s: %w", q.scope, translate(err))
		}
		for _, mr := range mrs {
			add(q.rel, mrItem(mr, s.host))
		}
	}
	mrs, _, err := s.c.MergeRequests.ListMergeRequests(&gl.ListMergeRequestsOptions{ReviewerID: gl.ReviewerID(me.ID), Scope: ptr("all"), State: ptr("opened"), ListOptions: lo}, wc)
	if err != nil {
		return nil, fmt.Errorf("gitlab: merge requests reviewer: %w", translate(err))
	}
	for _, mr := range mrs {
		add("review_requested", mrItem(mr, s.host))
	}
	for _, action := range []string{"mentioned", "directly_addressed"} {
		todos, _, err := s.c.Todos.ListTodos(&gl.ListTodosOptions{State: ptr("pending"), Action: ptr(gl.TodoAction(action)), ListOptions: lo}, wc)
		if err != nil {
			return nil, fmt.Errorf("gitlab: todos %s: %w", action, translate(err))
		}
		for _, t := range todos {
			if it, ok := todoItem(t); ok {
				add("mentioned", it)
			}
		}
	}
	out := make([]store.Item, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out, nil
}

// normalizeState maps GitLab's "opened" onto the store's "open" (GitHub vocabulary);
// closed/merged/locked pass through.
func normalizeState(st string) string {
	if st == "opened" {
		return "open"
	}
	return st
}

func issueItem(is *gl.Issue, host string) store.Item {
	it := store.Item{Number: int(is.IID), Kind: "issue", Title: is.Title, State: normalizeState(is.State), HTMLURL: is.WebURL, Labels: []string(is.Labels), Comments: int(is.UserNotesCount)}
	if is.Author != nil {
		it.Author = is.Author.Username
	}
	for _, a := range is.Assignees {
		it.Assignees = append(it.Assignees, a.Username)
	}
	if is.CreatedAt != nil {
		it.CreatedAt = is.CreatedAt.UTC()
	}
	if is.UpdatedAt != nil {
		it.UpdatedAt = is.UpdatedAt.UTC()
	}
	it.Repo = repoFromReference(is.References, is.WebURL)
	return it
}

func mrItem(mr *gl.BasicMergeRequest, host string) store.Item {
	it := store.Item{Number: int(mr.IID), Kind: "mr", Title: mr.Title, State: normalizeState(mr.State), HTMLURL: mr.WebURL, Labels: []string(mr.Labels), Draft: mr.Draft, Comments: int(mr.UserNotesCount)}
	if mr.Author != nil {
		it.Author = mr.Author.Username
	}
	for _, a := range mr.Assignees {
		it.Assignees = append(it.Assignees, a.Username)
	}
	if mr.CreatedAt != nil {
		it.CreatedAt = mr.CreatedAt.UTC()
	}
	if mr.UpdatedAt != nil {
		it.UpdatedAt = mr.UpdatedAt.UTC()
	}
	it.Repo = repoFromReference(mr.References, mr.WebURL)
	return it
}

func todoItem(t *gl.Todo) (store.Item, bool) {
	kind := normalizeTargetType(string(t.TargetType))
	if t.Target == nil || (kind != "Issue" && kind != "MergeRequest") {
		return store.Item{}, false
	}
	it := store.Item{Number: int(t.Target.IID), Kind: "issue", Title: t.Target.Title, State: normalizeState(t.Target.State), HTMLURL: t.Target.WebURL}
	if kind == "MergeRequest" {
		it.Kind = "mr"
	}
	if t.Project != nil {
		it.Repo = t.Project.PathWithNamespace
	} else {
		it.Repo = repoFromWebURL(t.Target.WebURL)
	}
	if t.CreatedAt != nil {
		it.CreatedAt = t.CreatedAt.UTC()
		it.UpdatedAt = it.CreatedAt
	}
	if it.State == "" {
		it.State = "open"
	}
	return it, true
}

// repoFromReference reads "group/project#12" / "group/project!12"; falls back to the web URL.
func repoFromReference(ref *gl.IssueReferences, webURL string) string {
	if ref != nil && ref.Full != "" {
		if i := strings.IndexAny(ref.Full, "#!"); i > 0 {
			return ref.Full[:i]
		}
	}
	return repoFromWebURL(webURL)
}

// repoFromWebURL turns https://host/group/project/-/issues/12 into group/project.
func repoFromWebURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return ""
	}
	rest := u[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[j+1:]
	} else {
		return ""
	}
	if k := strings.Index(rest, "/-/"); k >= 0 {
		return rest[:k]
	}
	return strings.Trim(rest, "/")
}

// --- writes -----------------------------------------------------------------

// MarkRead has no GitLab equivalent short of "done"; local only.
func (s *Source) MarkRead(ctx context.Context, threadID string) error { return source.ErrNotMirrorable }

// MarkDone marks a to-do done on GitLab (write). Event threads are local only.
func (s *Source) MarkDone(ctx context.Context, threadID string) error {
	id, ok := todoID(threadID)
	if !ok {
		return source.ErrNotMirrorable
	}
	if !s.writes {
		return source.ErrWritesDisabled
	}
	if _, err := s.c.Todos.MarkTodoAsDone(id, gl.WithContext(ctx)); err != nil {
		return fmt.Errorf("gitlab: mark to-do %d done: %w", id, translate(err))
	}
	return nil
}

// Unsubscribe stops notifications for the thread's issue/MR (write).
func (s *Source) Unsubscribe(ctx context.Context, threadID string) error {
	tg, ok := s.targetFor(threadID)
	if !ok || (tg.kind != "Issue" && tg.kind != "MergeRequest") {
		return source.ErrNotMirrorable
	}
	if !s.writes {
		return source.ErrWritesDisabled
	}
	var pid any = tg.projectID
	if tg.projectID == 0 {
		pid = tg.repo
	}
	var err error
	if tg.kind == "Issue" {
		_, _, err = s.c.Issues.UnsubscribeFromIssue(pid, tg.iid, gl.WithContext(ctx))
	} else {
		_, _, err = s.c.MergeRequests.UnsubscribeFromMergeRequest(pid, tg.iid, gl.WithContext(ctx))
	}
	if err != nil {
		return fmt.Errorf("gitlab: unsubscribe %s %s!%d: %w", tg.kind, tg.repo, tg.iid, translate(err))
	}
	return nil
}

func todoID(threadID string) (int64, bool) {
	if !strings.HasPrefix(threadID, "todo:") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(threadID, "todo:"), 10, 64)
	return id, err == nil
}

// targetFor resolves a thread id ("todo:<id>" via the last fetch, or
// "gl:<pid>:<Kind>:<iid>") to its issue/MR.
func (s *Source) targetFor(threadID string) (target, bool) {
	if id, ok := todoID(threadID); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		tg, ok := s.todoInfo[id]
		return tg, ok
	}
	parts := strings.Split(threadID, ":")
	if len(parts) != 4 || parts[0] != "gl" {
		return target{}, false
	}
	pid, err1 := strconv.ParseInt(parts[1], 10, 64)
	iid, err2 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil {
		return target{}, false
	}
	return target{projectID: pid, kind: parts[2], iid: iid}, true
}

var _ source.Source = (*Source)(nil)
var _ source.ScopeReporter = (*Source)(nil)
