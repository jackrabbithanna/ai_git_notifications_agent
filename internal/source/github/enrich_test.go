package github

import (
	"encoding/json"
	"testing"
)

func TestFindCommentShapes(t *testing.T) {
	rest := json.RawMessage(`[{"id":1,"body":"first","user":{"login":"a"},"created_at":"2026-09-01T00:00:00Z"},
	                          {"id":4680391878,"body":"the one","user":{"login":"alice"},"created_at":"2026-06-11T12:13:21Z"}]`)
	c, ok, n := findComment(rest, 4680391878)
	if !ok || c.body != "the one" || c.author != "alice" || c.at.IsZero() || n != 2 {
		t.Fatalf("rest: %+v ok=%v n=%d", c, ok, n)
	}
	graphql := json.RawMessage(`{"review_threads":[{"comments":[
	  {"author":"colemanw","body":"nit","created_at":"2026-09-18T10:00:00Z","html_url":"https://github.com/o/r/pull/1#discussion_r555","path":"x.php"}]}],
	  "totalCount":1,"pageInfo":{"hasNextPage":false}}`)
	c, ok, n = findComment(graphql, 555)
	if !ok || c.body != "nit" || c.author != "colemanw" || n != 1 {
		t.Fatalf("graphql: %+v ok=%v n=%d", c, ok, n)
	}
	reviews := json.RawMessage(`[{"id":77,"state":"APPROVED","user":{"login":"bob"},"submitted_at":"2026-09-18T11:00:00Z","html_url":"https://github.com/o/r/pull/1#pullrequestreview-77"}]`)
	c, ok, _ = findComment(reviews, 77)
	if !ok || c.author != "bob" || c.body != "(review: approved)" || c.at.IsZero() {
		t.Fatalf("review: %+v ok=%v", c, ok)
	}
	if _, ok, _ = findComment(rest, 999); ok {
		t.Fatal("unknown id must not match")
	}
}

func TestLabelListShapes(t *testing.T) {
	var a, b labelList
	if err := json.Unmarshal([]byte(`["bug","needs-review"]`), &a); err != nil || len(a) != 2 || a[1] != "needs-review" {
		t.Fatalf("strings: %v %v", a, err)
	}
	if err := json.Unmarshal([]byte(`[{"name":"x","color":"fff"},{"name":"y"}]`), &b); err != nil || len(b) != 2 || b[0] != "x" {
		t.Fatalf("objects: %v %v", b, err)
	}
}
