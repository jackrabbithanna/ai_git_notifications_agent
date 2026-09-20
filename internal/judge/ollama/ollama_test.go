package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitinbox/internal/judge"
)

func TestAskSchemaAndParsing(t *testing.T) {
	var req map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			_ = json.NewDecoder(r.Body).Decode(&req)
			content := `{"urgent":{"probability":0.7},"cat":{"choice":"b","probabilities":{"a":0.2,"b":0.8}},"lvl":{"probabilities":{"0":0.1,"1":0.2,"2":0.7}}}`
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "qwen3:8b", "message": map[string]any{"role": "assistant", "content": content}, "prompt_eval_count": 500, "eval_count": 60})
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{{"name": "qwen3:8b"}, {"name": "llama3.1:8b"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	j := New(Config{BaseURL: srv.URL, Model: "qwen3:8b"})
	qs := []judge.Question{
		{ID: "urgent", Kind: judge.Noul, Instructions: "?", NoulTrue: "y", NoulFalse: "n"},
		{ID: "cat", Kind: judge.Choice, Instructions: "?", Options: []judge.Option{{ID: "a", Description: "A"}, {ID: "b", Description: "B"}}},
		{ID: "lvl", Kind: judge.Score, Instructions: "?", Levels: []string{"low", "mid", "high"}},
	}
	res, err := j.Ask(context.Background(), map[string]any{"x": 1}, qs)
	if err != nil {
		t.Fatal(err)
	}
	if req["model"] != "qwen3:8b" || req["stream"] != false {
		t.Fatalf("request: %v", req)
	}
	schema := req["format"].(map[string]any)
	props := schema["properties"].(map[string]any)
	if _, ok := props["cat"].(map[string]any)["properties"].(map[string]any)["choice"]; !ok {
		t.Fatalf("schema missing choice: %v", schema)
	}
	if res.Calibrated || res.Provider != "ollama" || res.Usage.InputTokens != 500 {
		t.Fatalf("meta: %+v", res)
	}
	if res.Answers["urgent"].Noul != 0.7 || res.Answers["cat"].Choice != "b" || res.Answers["cat"].Confidence != 0.8 {
		t.Fatalf("answers: %+v", res.Answers)
	}
	if s := res.Answers["lvl"]; s.Score < 1.59 || s.Score > 1.61 || s.Legend[2] != "high" {
		t.Fatalf("score: %+v", s)
	}
	models, err := j.Models(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("models: %v %v", models, err)
	}
}

func TestBadOutputIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": "I cannot answer"}})
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, Model: "m"}).Ask(context.Background(), "s", []judge.Question{{ID: "q", Kind: judge.Noul, Instructions: "?"}})
	if err == nil {
		t.Fatal("expected parse error")
	}
}
