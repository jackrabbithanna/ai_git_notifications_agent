package ghmcp

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const notificationsFixture = `[{"id":"123","unread":true,"reason":"review_requested","updated_at":"2026-09-19T10:00:00Z",
 "subject":{"title":"Fix bug","url":"https://api.github.com/repos/o/r/pulls/7","latest_comment_url":"https://api.github.com/repos/o/r/pulls/7","type":"PullRequest"},
 "repository":{"id":1,"name":"r","full_name":"o/r","private":false,"owner":{"login":"o"}},
 "url":"https://api.github.com/notifications/threads/123"},
 {"id":"124","unread":false,"reason":"comment","updated_at":"2026-09-18T09:00:00Z","last_read_at":"2026-09-18T09:30:00Z",
 "subject":{"title":"Question","url":"https://api.github.com/repos/o/r/issues/8","latest_comment_url":"https://api.github.com/repos/o/r/issues/comments/99","type":"Issue"},
 "repository":{"full_name":"o/r","name":"r","owner":{"login":"o"}}}]`

const searchFixture = `{"total_count":1,"incomplete_results":false,"items":[{"number":5,"title":"T","state":"open",
 "html_url":"https://github.com/o/r/pull/5","repository_url":"https://api.github.com/repos/o/r","user":{"login":"me"},
 "assignees":[{"login":"me"},{"login":"you"}],"labels":[{"name":"bug"}],"draft":true,"comments":2,
 "created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z","pull_request":{"url":"x"}}]}`

type fakeServer struct {
	calls    map[string]int
	lastArgs map[string]any
	fail     map[string]string // tool -> error text (IsError result)
}

func (f *fakeServer) handler(name, reply string) func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		f.calls[name]++
		f.lastArgs = args
		if msg, ok := f.fail[name]; ok {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: reply}}}, nil, nil
	}
}

// newFake wires a Client to an in-memory fake server; readOnly mirrors --read-only
// by not registering write tools at all. connects counts (re)connections.
func newFake(t *testing.T, mode WriteMode) (*Client, *fakeServer, *int, *mcp.ServerSession) {
	t.Helper()
	f := &fakeServer{calls: map[string]int{}, fail: map[string]string{}}
	connects := 0
	var serverSess *mcp.ServerSession
	c := New(Config{BinaryPath: "fake", Token: "t", WriteMode: mode})
	c.connect = func(ctx context.Context) (session, error) {
		connects++
		srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0"}, nil)
		add := func(name, reply string, readOnly bool) {
			mcp.AddTool(srv, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly}}, f.handler(name, reply))
		}
		add(ToolListNotifications, notificationsFixture, true)
		add(ToolSearchIssues, searchFixture, true)
		add(ToolGetMe, `{"login":"octocat","name":"Octo"}`, true)
		if mode != WriteModeReadOnly {
			add(ToolDismissNotification, "Notification marked as done", false)
		}
		ct, st := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(ctx, st, nil)
		if err != nil {
			return nil, err
		}
		serverSess = ss
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
		return client.Connect(ctx, ct, nil)
	}
	t.Cleanup(func() { c.Close() })
	return c, f, &connects, serverSess
}

func TestArgsReadOnlyFlag(t *testing.T) {
	ro := Config{WriteMode: WriteModeReadOnly}.args()
	if !contains(ro, "--read-only") {
		t.Fatalf("readonly args missing --read-only: %v", ro)
	}
	rw := Config{WriteMode: WriteModeNotifications}.args()
	if contains(rw, "--read-only") {
		t.Fatalf("notifications mode must not pass --read-only: %v", rw)
	}
	if ro[0] != "stdio" || !contains(ro, "--toolsets") {
		t.Fatalf("unexpected args: %v", ro)
	}
}

func TestAllowlistRejectsWritesInReadOnly(t *testing.T) {
	c, f, _, _ := newFake(t, WriteModeReadOnly)
	err := c.DismissNotification(context.Background(), "123", "done")
	var na *NotAllowedError
	if !errors.As(err, &na) || na.Tool != ToolDismissNotification {
		t.Fatalf("expected NotAllowedError, got %v", err)
	}
	if f.calls[ToolDismissNotification] != 0 {
		t.Fatal("rejected call must never reach the server")
	}
	if c.Stats().Rejected != 1 {
		t.Fatalf("rejected counter = %d", c.Stats().Rejected)
	}
	// Tools outside the app's surface are rejected in every mode.
	if _, err := c.CallRaw(context.Background(), "merge_pull_request", nil); !errors.As(err, &na) {
		t.Fatalf("expected NotAllowedError for merge_pull_request, got %v", err)
	}
}

func TestAllowlistAllowsNotificationWrites(t *testing.T) {
	c, f, _, _ := newFake(t, WriteModeNotifications)
	if err := c.DismissNotification(context.Background(), "123", "done"); err != nil {
		t.Fatal(err)
	}
	if f.calls[ToolDismissNotification] != 1 || f.lastArgs["threadID"] != "123" || f.lastArgs["state"] != "done" {
		t.Fatalf("server saw calls=%v args=%v", f.calls, f.lastArgs)
	}
	if _, err := c.CallRaw(context.Background(), "merge_pull_request", nil); err == nil {
		t.Fatal("merge_pull_request must stay blocked in notifications mode")
	}
}

func TestListNotificationsParses(t *testing.T) {
	c, f, _, _ := newFake(t, WriteModeReadOnly)
	ns, err := c.ListNotifications(context.Background(), ListNotificationsOpts{Filter: "default", PerPage: 50, Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastArgs["filter"] != "default" || f.lastArgs["perPage"] != float64(50) || f.lastArgs["page"] != float64(2) {
		t.Fatalf("args not forwarded: %v", f.lastArgs)
	}
	if len(ns) != 2 {
		t.Fatalf("want 2 notifications, got %d", len(ns))
	}
	n := ns[0]
	if n.ID != "123" || !n.Unread || n.Reason != "review_requested" || n.Repo != "o/r" || n.RepoOwner != "o" || n.SubjectType != "PullRequest" || n.SubjectNumber() != 7 {
		t.Fatalf("bad conversion: %+v", n)
	}
	if n.UpdatedAt.IsZero() || n.LastReadAt != nil {
		t.Fatalf("times: %+v", n)
	}
	if ns[1].LastReadAt == nil || ns[1].Unread || ns[1].SubjectNumber() != 8 {
		t.Fatalf("second: %+v", ns[1])
	}
}

func TestSearchParses(t *testing.T) {
	c, f, _, _ := newFake(t, WriteModeReadOnly)
	res, err := c.SearchIssues(context.Background(), "assignee:@me is:open", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastArgs["query"] != "assignee:@me is:open" {
		t.Fatalf("query not forwarded: %v", f.lastArgs)
	}
	if fields, ok := f.lastArgs["fields"].([]any); !ok || len(fields) != len(searchFields) {
		t.Fatalf("fields not forwarded: %v", f.lastArgs["fields"])
	}
	if res.TotalCount != 1 || len(res.Items) != 1 {
		t.Fatalf("result: %+v", res)
	}
	it := res.Items[0]
	if it.Repo != "o/r" || it.Number != 5 || !it.IsPR || !it.Draft || it.Author != "me" || len(it.Assignees) != 2 || it.Labels[0] != "bug" || it.Comments != 2 {
		t.Fatalf("item: %+v", it)
	}
}

func TestToolErrorSurfaces(t *testing.T) {
	c, f, _, _ := newFake(t, WriteModeReadOnly)
	f.fail[ToolGetMe] = "bad credentials"
	_, err := c.GetMe(context.Background())
	var te *ToolError
	if !errors.As(err, &te) || te.Message != "bad credentials" {
		t.Fatalf("expected ToolError, got %v", err)
	}
	if c.Stats().Errors[ToolGetMe] != 1 || c.Stats().LastError != "bad credentials" {
		t.Fatalf("stats: %+v", c.Stats())
	}
}

func TestRestartAfterTransportFailure(t *testing.T) {
	c, _, connects, _ := newFake(t, WriteModeReadOnly)
	ctx := context.Background()
	if _, err := c.GetMe(ctx); err != nil {
		t.Fatal(err)
	}
	// Kill the live session from underneath the client: the next call must
	// reconnect once and succeed.
	c.mu.Lock()
	c.sess.Close()
	c.mu.Unlock()
	if _, err := c.GetMe(ctx); err != nil {
		t.Fatalf("call after dead session: %v", err)
	}
	if *connects != 2 || c.Stats().Restarts != 1 {
		t.Fatalf("connects=%d restarts=%d", *connects, c.Stats().Restarts)
	}
}

func TestListToolsAnnotates(t *testing.T) {
	c, _, _, _ := newFake(t, WriteModeNotifications)
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ToolInfo{}
	for _, ti := range tools {
		byName[ti.Name] = ti
	}
	if d := byName[ToolDismissNotification]; d.ReadOnly || !d.Allowed {
		t.Fatalf("dismiss: %+v", d)
	}
	if g := byName[ToolGetMe]; !g.ReadOnly || !g.Allowed {
		t.Fatalf("get_me: %+v", g)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
