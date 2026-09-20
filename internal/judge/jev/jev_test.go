package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gitinbox/internal/judge"
)

func TestAskEncodesAndParses(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{
		  "is_urgent":{"type":"noul","noul":0.92},
		  "category":{"type":"choice","choice":"technical","probabilities":{"billing":0.08,"technical":0.85,"sales":0.07},"confidence":0.82},
		  "frustration":{"type":"score","score":1.6,"legend":{"0":"Calm","1":"Frustrated","2":"Very angry"},"probabilities":{"0":0.05,"1":0.3,"2":0.65},"confidence":0.78}
		},"usage":{"input_tokens":312,"output_tokens":48}}`))
	}))
	defer srv.Close()
	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	qs := []judge.Question{
		{ID: "is_urgent", Kind: judge.Noul, Instructions: "urgent?", NoulTrue: "yes", NoulFalse: "no"},
		{ID: "category", Kind: judge.Choice, Instructions: "team?", Options: []judge.Option{{ID: "billing", Description: "b"}, {ID: "technical", Description: "t"}, {ID: "sales", Description: "s"}}},
		{ID: "frustration", Kind: judge.Score, Instructions: "how?", Levels: []string{"Calm", "Frustrated", "Very angry"}},
	}
	res, err := c.Ask(context.Background(), map[string]any{"text": "help"}, qs)
	if err != nil {
		t.Fatal(err)
	}
	// Request shape per docs/typesafeapi.md.
	if got["model"] != "jev-latest" || got["state"].(map[string]any)["text"] != "help" {
		t.Fatalf("request: %v", got)
	}
	q := got["questions"].(map[string]any)
	if q["category"].(map[string]any)["type"] != "choice" || q["category"].(map[string]any)["criteria"].(map[string]any)["billing"] != "b" {
		t.Fatalf("choice encoding: %v", q["category"])
	}
	if lv := q["frustration"].(map[string]any)["criteria"].([]any); len(lv) != 3 || lv[2] != "Very angry" {
		t.Fatalf("score encoding: %v", q["frustration"])
	}
	if crit := q["is_urgent"].(map[string]any)["criteria"].(map[string]any); crit["true"] != "yes" {
		t.Fatalf("noul encoding: %v", crit)
	}
	// Response mapping.
	if !res.Calibrated || res.Provider != "jev" || res.Usage.InputTokens != 312 {
		t.Fatalf("meta: %+v", res)
	}
	if a := res.Answers["is_urgent"]; a.Noul != 0.92 {
		t.Fatalf("noul: %+v", a)
	}
	if a := res.Answers["category"]; a.Choice != "technical" || a.Confidence != 0.82 || a.Probabilities["technical"] != 0.85 {
		t.Fatalf("choice: %+v", a)
	}
	if a := res.Answers["frustration"]; a.Score != 1.6 || a.Legend[2] != "Very angry" || a.Probabilities["2"] != 0.65 || a.Normalized() < 0.79 {
		t.Fatalf("score: %+v", a)
	}
}

func TestErrorsAndRetries(t *testing.T) {
	var calls, flaky int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.Header.Get("Authorization") {
		case "Bearer bad":
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer malformed":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"questions.x.criteria is required"}`))
		case "Bearer flaky":
			if atomic.AddInt32(&flaky, 1) < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.1}},"usage":{"input_tokens":1,"output_tokens":1}}`))
		}
	}))
	defer srv.Close()
	qs := []judge.Question{{ID: "q", Kind: judge.Noul, Instructions: "?"}}

	_, err := New(Config{APIKey: "bad", BaseURL: srv.URL}).Ask(context.Background(), "s", qs)
	if !errors.Is(err, judge.ErrUnauthorized) {
		t.Fatalf("401: %v", err)
	}
	_, err = New(Config{APIKey: "malformed", BaseURL: srv.URL}).Ask(context.Background(), "s", qs)
	if !errors.Is(err, judge.ErrInvalidRequest) || err.Error() == "" {
		t.Fatalf("422: %v", err)
	}
	before := atomic.LoadInt32(&calls)
	res, err := New(Config{APIKey: "flaky", BaseURL: srv.URL, Backoff: time.Millisecond}).Ask(context.Background(), "s", qs)
	if err != nil || res.Answers["q"].Noul != 0.1 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if atomic.LoadInt32(&calls)-before != 3 {
		t.Fatalf("expected 3 attempts on 429, got %d", atomic.LoadInt32(&calls)-before)
	}
	if _, err := New(Config{BaseURL: srv.URL}).Ask(context.Background(), "s", qs); !errors.Is(err, judge.ErrUnauthorized) {
		t.Fatalf("no key: %v", err)
	}
}
