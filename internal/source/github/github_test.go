package github

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"ghinbox/internal/ghmcp"
	"ghinbox/internal/store"
)

// fakeMCP serves canned tool results keyed by tool name + method (or page).
type fakeMCP struct {
	calls map[string]int
	files map[int]string // page → JSON for get_files
	diff  string
	prGet string
}

func (f *fakeMCP) handler(name string) func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		method, _ := args["method"].(string)
		key := name
		if method != "" {
			key += ":" + method
		}
		f.calls[key]++
		var text string
		switch key {
		case "pull_request_read:get":
			text = f.prGet
		case "pull_request_read:get_files":
			page := 1
			if p, ok := args["page"].(float64); ok {
				page = int(p)
			}
			text = f.files[page]
			if text == "" {
				text = "[]"
			}
		case "pull_request_read:get_diff":
			b, _ := json.Marshal(f.diff)
			text = string(b)
		case "list_pull_requests":
			text = `[{"number":5,"title":"landed","html_url":"https://github.com/o/r/pull/5","merged_at":"2026-09-19T10:00:00Z","updated_at":"2026-09-19T10:00:00Z","user":{"login":"a"}},
			         {"number":6,"title":"closed","html_url":"https://github.com/o/r/pull/6","merged_at":null,"updated_at":"2026-09-19T11:00:00Z","user":{"login":"b"}},
			         {"number":4,"title":"old","html_url":"https://github.com/o/r/pull/4","merged_at":"2026-09-01T10:00:00Z","updated_at":"2026-09-01T10:00:00Z","user":{"login":"c"}}]`
		case "issue_read:get":
			text = `{"body":"issue body","state":"open","user":{"login":"alice"},"labels":["bug"],"created_at":"2026-09-18T09:00:00Z"}`
		default:
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "unexpected tool " + key}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}

func newSource(t *testing.T, f *fakeMCP) *Source {
	t.Helper()
	f.calls = map[string]int{}
	c := ghmcp.NewForTest(ghmcp.Config{BinaryPath: "fake", Token: "t"}, func(ctx context.Context) (ghmcp.Session, error) {
		srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "0"}, nil)
		for _, name := range []string{ghmcp.ToolPullRequestRead, ghmcp.ToolIssueRead, ghmcp.ToolListPullRequests} {
			mcp.AddTool(srv, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, f.handler(name))
		}
		ct, st := mcp.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, st, nil); err != nil {
			return nil, err
		}
		return mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	})
	t.Cleanup(func() { c.Close() })
	return NewFromClient(c, "")
}

const prGet = `{"title":"APIv4 - change getFields","body":"BREAKING: signature change","state":"open","merged":false,"draft":false,
 "html_url":"https://github.com/civicrm/civicrm-core/pull/7","created_at":"2026-09-18T09:00:00Z","updated_at":"2026-09-19T09:00:00Z",
 "labels":["master"],"user":{"login":"dev"},"head":{"sha":"abc123"},"base":{"ref":"master"},"changed_files":2,"additions":10,"deletions":2}`

func TestChangesWithPatches(t *testing.T) {
	f := &fakeMCP{prGet: prGet, files: map[int]string{1: `[{"filename":"Civi/Api4/Contact.php","status":"modified","additions":8,"deletions":2,"patch":"@@ -1 +1 @@\n-  public function getFields() {\n+  public function getFields(bool $x) {"},
		{"filename":"tests/x.php","status":"added","additions":2,"deletions":0,"patch":"+x"}]`}}
	s := newSource(t, f)
	cs, err := s.Changes(context.Background(), "civicrm/civicrm-core", 7)
	if err != nil {
		t.Fatal(err)
	}
	if cs.HeadSHA != "abc123" || cs.State != "open" || cs.Base != "master" || cs.Author != "dev" || cs.Labels[0] != "master" || cs.FilesTotal != 2 || len(cs.Files) != 2 {
		t.Fatalf("changeset: %+v", cs)
	}
	if cs.Files[0].Patch == "" || cs.Files[0].Additions != 8 || cs.CreatedAt.IsZero() {
		t.Fatalf("file: %+v", cs.Files[0])
	}
	if f.calls["pull_request_read:get_diff"] != 0 {
		t.Fatal("get_diff must not be called when patches are present")
	}
}

func TestChangesDiffFallbackAndMerged(t *testing.T) {
	merged := `{"title":"t","state":"closed","merged":true,"merged_at":"2026-09-19T10:00:00Z","html_url":"u","created_at":"2026-09-18T09:00:00Z","updated_at":"2026-09-19T10:00:00Z","labels":[{"name":"x"}],"user":{"login":"d"},"head":{"sha":"h"},"base":{"ref":"master"}}`
	f := &fakeMCP{prGet: merged, files: map[int]string{1: `[{"filename":"a/b.php","status":"modified","additions":1,"deletions":1},{"filename":"c.txt","status":"added","additions":1,"deletions":0}]`},
		diff: "diff --git a/a/b.php b/a/b.php\nindex 1..2 100644\n--- a/a/b.php\n+++ b/a/b.php\n@@ -1 +1 @@\n-old\n+new\ndiff --git a/c.txt b/c.txt\n--- /dev/null\n+++ b/c.txt\n@@ -0,0 +1 @@\n+hello\n"}
	s := newSource(t, f)
	cs, err := s.Changes(context.Background(), "o/r", 9)
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != "merged" || cs.MergedAt == nil || cs.Labels[0] != "x" || cs.FilesTotal != 2 {
		t.Fatalf("merged changeset: %+v", cs)
	}
	if f.calls["pull_request_read:get_diff"] != 1 || cs.Files[0].Patch != "@@ -1 +1 @@\n-old\n+new\n" || cs.Files[1].Patch != "@@ -0,0 +1 @@\n+hello\n" {
		t.Fatalf("diff fallback: calls=%v patches=%q / %q", f.calls, cs.Files[0].Patch, cs.Files[1].Patch)
	}
}

func TestRecentlyMergedAndEnrichCreated(t *testing.T) {
	f := &fakeMCP{prGet: prGet}
	s := newSource(t, f)
	since := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	refs, err := s.RecentlyMerged(context.Background(), "o/r", since, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Number != 5 || refs[0].Author != "a" || refs[0].MergedAt == nil {
		t.Fatalf("recently merged: %+v", refs)
	}
	e, err := s.Enrich(context.Background(), store.Thread{Repo: "o/r", SubjectType: "Issue", SubjectNumber: 3, UpdatedAt: time.Now()})
	if err != nil || e.ItemCreatedAt == nil || e.ItemAuthor != "alice" || e.ItemLabels[0] != "bug" {
		t.Fatalf("enrich: %+v %v", e, err)
	}
}
