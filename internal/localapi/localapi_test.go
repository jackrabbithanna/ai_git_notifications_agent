package localapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitinbox/internal/localapi"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/secrets"
	"gitinbox/internal/store"
)

type env struct {
	t    *testing.T
	s    *localapi.Server
	h    http.Handler
	db   *store.DB
	acct store.Account
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sec := secrets.OpenFile(filepath.Join(t.TempDir(), "s.json"))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := pipeline.New(pipeline.Deps{DB: db, Secrets: sec, Logger: logger})
	acct, err := db.InsertAccount(ctx, store.ForgeGitHub, "me", "github.com")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	threads := []store.Thread{
		{AccountID: acct.ID, ThreadID: "t1", Repo: "o/r", SubjectType: "PullRequest", SubjectNumber: 7, HTMLURL: "https://github.com/o/r/pull/7", Title: "Fix afform validation", Reason: "mention", Unread: true, UpdatedAt: now, ActivityKind: "mention", RelationTags: []string{"mentioned"}, FilterVerdict: "keep", FirstSeenAt: now, LastSyncedAt: now},
		{AccountID: acct.ID, ThreadID: "t2", Repo: "o/r", SubjectType: "Issue", SubjectNumber: 8, HTMLURL: "https://github.com/o/r/issues/8", Title: "Docs typo", Reason: "subscribed", Unread: true, UpdatedAt: now.Add(-time.Hour), ActivityKind: "comment", RelationTags: []string{"subscriber"}, FilterVerdict: "keep", FirstSeenAt: now, LastSyncedAt: now},
	}
	for _, th := range threads {
		if err := db.UpsertThread(ctx, th); err != nil {
			t.Fatal(err)
		}
	}
	s := localapi.New(db, p, logger)
	return &env{t: t, s: s, h: s.Handler(), db: db, acct: acct}
}

func (e *env) do(method, path string, body any, auth bool) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if auth {
		req.Header.Set("Authorization", "Bearer "+e.s.Token())
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			e.t.Fatalf("%s %s: non-JSON body %q", method, path, rec.Body.String())
		}
	}
	return rec.Code, out
}

func rows(v map[string]any, key string) []map[string]any {
	list, _ := v[key].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.do("GET", "/v1/accounts", nil, false); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	req := httptest.NewRequest("GET", "/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if len(e.s.Token()) < 32 {
		t.Fatal("token too short")
	}
}

func TestAccountsThreadsAndSearch(t *testing.T) {
	e := newEnv(t)
	code, v := e.do("GET", "/v1/accounts", nil, true)
	accts := rows(v, "accounts")
	if code != 200 || len(accts) != 1 || accts[0]["ref"] != "me@github.com" || accts[0]["forge"] != "github" {
		t.Fatalf("accounts: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/threads?limit=10", nil, true)
	list := rows(v, "threads")
	if code != 200 || len(list) != 2 {
		t.Fatalf("threads: %d %+v", code, v)
	}
	r := list[0]
	if r["account"] != "me@github.com" || r["repo"] != "o/r" || r["bucket"] == "" || r["url"] == "" || r["unread"] != true {
		t.Fatalf("row: %+v", r)
	}
	// Unjudged mention (prior 0.5+0.5) ranks above the subscriber comment.
	if r["thread_id"] != "t1" {
		t.Fatalf("expected t1 first, got %+v", list)
	}
	code, v = e.do("GET", "/v1/threads?q=afform+%237", nil, true)
	if list = rows(v, "threads"); code != 200 || len(list) != 1 || list[0]["thread_id"] != "t1" {
		t.Fatalf("search: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/threads?q=nothing-here", nil, true)
	if list = rows(v, "threads"); code != 200 || len(list) != 0 {
		t.Fatalf("empty search: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/threads?account=nobody", nil, true)
	if code != 400 {
		t.Fatalf("unknown account: %d %+v", code, v)
	}
}

func TestThreadDetailFindAndActions(t *testing.T) {
	e := newEnv(t)
	code, v := e.do("GET", "/v1/threads/me@github.com/t1", nil, true)
	if code != 200 {
		t.Fatalf("detail: %d %+v", code, v)
	}
	th, _ := v["thread"].(map[string]any)
	st, _ := v["state"].(map[string]any)
	sth, _ := st["thread"].(map[string]any)
	if th["thread_id"] != "t1" || sth["title"] != "Fix afform validation" || v["draft"] != nil {
		t.Fatalf("detail content: %+v", v)
	}
	code, v = e.do("GET", "/v1/threads/1/missing", nil, true)
	if code != 404 {
		t.Fatalf("missing thread: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/threads/find?account=1&repo=o/r&number=8", nil, true)
	if th, _ = v["thread"].(map[string]any); code != 200 || th["thread_id"] != "t2" {
		t.Fatalf("find: %d %+v", code, v)
	}

	// done hides t1 from the default list; undone brings it back.
	if code, v = e.do("POST", "/v1/threads/1/t1/done", nil, true); code != 200 || v["ok"] != true {
		t.Fatalf("done: %d %+v", code, v)
	}
	_, v = e.do("GET", "/v1/threads", nil, true)
	if list := rows(v, "threads"); len(list) != 1 || list[0]["thread_id"] != "t2" {
		t.Fatalf("after done: %+v", list)
	}
	if code, _ = e.do("POST", "/v1/threads/1/t1/undone", nil, true); code != 200 {
		t.Fatalf("undone: %d", code)
	}
	// Done also marked the thread read locally (app semantics), so it is back but read.
	_, v = e.do("GET", "/v1/threads?q=afform&include_read=1", nil, true)
	if list := rows(v, "threads"); len(list) != 1 || list[0]["done"] != false || list[0]["unread"] != false {
		t.Fatalf("after undone: %+v", list)
	}
	if code, _ = e.do("POST", "/v1/threads/1/t2/snooze", map[string]any{"hours": 2}, true); code != 200 {
		t.Fatalf("snooze: %d", code)
	}
	// t1 is read (done marked it), t2 snoozed → the default (unread) list is empty.
	_, v = e.do("GET", "/v1/threads", nil, true)
	if list := rows(v, "threads"); len(list) != 0 {
		t.Fatalf("after snooze: %+v", list)
	}
	if code, _ = e.do("POST", "/v1/threads/1/t2/explode", nil, true); code != 404 {
		t.Fatalf("bad action: %d", code)
	}
	// Search with include_read sees the snoozed thread again.
	_, v = e.do("GET", "/v1/threads?q=typo&include_read=1", nil, true)
	if list := rows(v, "threads"); len(list) != 1 || list[0]["snoozed"] != true {
		t.Fatalf("include_read search: %+v", list)
	}
}

func TestDraftsAndGitHubGuard(t *testing.T) {
	e := newEnv(t)
	code, v := e.do("POST", "/v1/drafts", map[string]any{"account": "me@github.com", "thread_id": "t1", "text": "Thanks, will look."}, true)
	if code != 200 || v["ok"] != true || !strings.Contains(v["stored"].(string), "nothing was posted") {
		t.Fatalf("put draft: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/drafts", nil, true)
	if list := rows(v, "drafts"); code != 200 || len(list) != 1 || list[0]["text"] != "Thanks, will look." || list[0]["repo"] != "o/r" {
		t.Fatalf("drafts: %d %+v", code, v)
	}
	_, v = e.do("GET", "/v1/threads/1/t1", nil, true)
	if d, _ := v["draft"].(map[string]any); d == nil || d["text"] != "Thanks, will look." {
		t.Fatalf("detail draft: %+v", v["draft"])
	}
	if code, _ = e.do("POST", "/v1/drafts", map[string]any{"account": "1", "thread_id": "t1", "text": "  "}, true); code != 400 {
		t.Fatalf("empty draft: %d", code)
	}
	// Write tools are refused before any server is contacted.
	code, v = e.do("POST", "/v1/github/1/call", map[string]any{"tool": "dismiss_notification", "args": map[string]any{}}, true)
	if code != 403 {
		t.Fatalf("write tool: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/tools", nil, true)
	if list, _ := v["github_read_tools"].([]any); code != 200 || len(list) == 0 {
		t.Fatalf("tools: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/mine", nil, true)
	if code != 200 || v["items"] == nil {
		t.Fatalf("mine: %d %+v", code, v)
	}
	code, v = e.do("GET", "/v1/impact", nil, true)
	if code != 200 || v["analyses"] == nil {
		t.Fatalf("impact: %d %+v", code, v)
	}
}

func TestStartServesOnLoopback(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.s.Close()
	if !strings.HasPrefix(e.s.URL(), "http://127.0.0.1:") {
		t.Fatalf("url: %s", e.s.URL())
	}
	req, _ := http.NewRequest("GET", e.s.URL()+"/v1/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+e.s.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
