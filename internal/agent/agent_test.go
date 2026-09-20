package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as a fake pi when GITINBOX_FAKE_PI is set: the manager
// spawns the test binary itself, which then speaks the RPC protocol.
func TestMain(m *testing.M) {
	if os.Getenv("GITINBOX_FAKE_PI") == "1" {
		fakePi()
		return
	}
	os.Exit(m.Run())
}

func fakePi() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("0.0.0-fake")
		os.Exit(0)
	}
	out := json.NewEncoder(os.Stdout)
	emit := func(v map[string]any) { _ = out.Encode(v) }
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var cmd map[string]any
		if json.Unmarshal(sc.Bytes(), &cmd) != nil {
			emit(map[string]any{"type": "response", "command": "parse", "success": false, "error": "bad json"})
			continue
		}
		id := cmd["id"]
		respond := func(command string, data any) {
			r := map[string]any{"type": "response", "id": id, "command": command, "success": true}
			if data != nil {
				r["data"] = data
			}
			emit(r)
		}
		switch cmd["type"] {
		case "get_state":
			// sessionFile carries the agent dir so the test can check the environment.
			respond("get_state", map[string]any{"model": map[string]any{"id": "fake-model", "provider": "ollama"}, "isStreaming": false,
				"sessionFile": os.Getenv("PI_CODING_AGENT_DIR") + "|" + os.Getenv("GITINBOX_API_URL") + "|" + strings.Join(os.Args[1:], " ")})
		case "prompt":
			respond("prompt", nil)
			emit(map[string]any{"type": "agent_start"})
			emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Hello "}})
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "world"}})
			emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Hello world"}}, "stopReason": "toolUse"}})
			emit(map[string]any{"type": "tool_execution_start", "toolCallId": "t1", "toolName": "list_accounts", "args": map[string]any{}})
			emit(map[string]any{"type": "tool_execution_end", "toolCallId": "t1", "toolName": "list_accounts", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "{\"accounts\":[]}"}}}, "isError": false})
			emit(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant"}})
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "No accounts."}})
			emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "No accounts."}}, "stopReason": "stop"}})
			emit(map[string]any{"type": "agent_end", "messages": []any{}})
		case "get_last_assistant_text":
			respond("get_last_assistant_text", map[string]any{"text": "No accounts."})
		case "get_available_models":
			respond("get_available_models", map[string]any{"models": []any{map[string]any{"provider": "ollama", "id": "fake-model", "name": "fake-model"}}})
		case "set_model":
			respond("set_model", map[string]any{"id": cmd["modelId"]})
		case "abort", "new_session":
			respond(cmd["type"].(string), map[string]any{"cancelled": false})
		default:
			emit(map[string]any{"type": "response", "id": id, "command": cmd["type"], "success": false, "error": "unknown command"})
		}
	}
	os.Exit(0)
}

type recorder struct {
	mu     sync.Mutex
	events []UIEvent
}

func (r *recorder) on(ev UIEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func TestManagerRoundTrip(t *testing.T) {
	t.Setenv("GITINBOX_FAKE_PI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	data := t.TempDir()
	m := New(Config{PiPath: exe, DataDir: data, OllamaURL: "http://ollama:11434", Model: "fake-model", Models: []string{"other"}, APIURL: "http://127.0.0.1:1", APIToken: "tok", SystemPrompt: "sys", OnEvent: rec.on})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	st := m.Status()
	if !st.Running || st.Version != "0.0.0-fake" || st.Model != "fake-model" || st.Source != "override" {
		t.Fatalf("status: %+v", st)
	}
	// The fake reports the environment and args through sessionFile.
	parts := strings.Split(st.SessionFile, "|")
	if len(parts) != 3 || parts[0] != filepath.Join(data, "agent") || parts[1] != "http://127.0.0.1:1" {
		t.Fatalf("env not passed: %q", st.SessionFile)
	}
	for _, want := range []string{"--mode rpc", "--no-builtin-tools", "--no-extensions", "-e " + filepath.Join(data, "agent", "extensions", "gitinbox.ts"), "--provider ollama", "--model fake-model", "--thinking off", "--system-prompt sys", "--offline", "--no-approve"} {
		if !strings.Contains(parts[2], want) {
			t.Errorf("args missing %q in %q", want, parts[2])
		}
	}
	// models.json / settings.json / extension were written.
	for _, f := range []string{"models.json", "settings.json", filepath.Join("extensions", "gitinbox.ts")} {
		if _, err := os.Stat(filepath.Join(data, "agent", f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	models, _ := os.ReadFile(filepath.Join(data, "agent", "models.json"))
	if !strings.Contains(string(models), `"fake-model"`) || !strings.Contains(string(models), `"other"`) || !strings.Contains(string(models), "http://ollama:11434/v1") {
		t.Fatalf("models.json: %s", models)
	}

	if err := m.Prompt(ctx, "hi"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if err := m.WaitIdle(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	tr := m.Transcript()
	if len(tr) != 4 {
		t.Fatalf("transcript: %+v", tr)
	}
	if tr[0].Role != "user" || tr[0].Text != "hi" || tr[1].Role != "assistant" || tr[1].Text != "Hello world" || !tr[1].Done {
		t.Fatalf("first messages: %+v", tr[:2])
	}
	if tr[2].Role != "tool" || tr[2].Tool == nil || tr[2].Tool.Name != "list_accounts" || !tr[2].Tool.Done || tr[2].Tool.Result != `{"accounts":[]}` {
		t.Fatalf("tool message: %+v", tr[2])
	}
	if tr[3].Role != "assistant" || tr[3].Text != "No accounts." {
		t.Fatalf("final message: %+v", tr[3])
	}
	if text, err := m.LastText(ctx); err != nil || text != "No accounts." {
		t.Fatalf("last text: %q %v", text, err)
	}
	types := strings.Join(rec.types(), ",")
	for _, want := range []string{"agent_start", "text_delta", "message_end", "tool_start", "tool_end", "agent_end"} {
		if !strings.Contains(types, want) {
			t.Errorf("event %s missing in %s", want, types)
		}
	}
	if m.Status().Busy {
		t.Fatal("still busy after agent_end")
	}
	list, err := m.Models(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "fake-model" {
		t.Fatalf("models: %+v %v", list, err)
	}
	if err := m.SetModel(ctx, "other"); err != nil || m.Status().Model != "other" {
		t.Fatalf("set model: %v %+v", err, m.Status())
	}
	if err := m.NewSession(ctx); err != nil || len(m.Transcript()) != 0 {
		t.Fatalf("new session: %v", err)
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if m.Status().Running {
		t.Fatal("still running after stop")
	}
}

func TestPrepareAndPrompt(t *testing.T) {
	dir := t.TempDir()
	l, err := Prepare(DirConfig{Dir: dir, OllamaURL: "http://x:11434/", DefaultModel: "m1", Models: []string{"m1", "m2", ""}})
	if err != nil {
		t.Fatal(err)
	}
	var models struct {
		Providers map[string]struct {
			BaseURL string           `json:"baseUrl"`
			API     string           `json:"api"`
			Models  []map[string]any `json:"models"`
		} `json:"providers"`
	}
	b, _ := os.ReadFile(l.ModelsPath)
	if err := json.Unmarshal(b, &models); err != nil {
		t.Fatal(err)
	}
	p := models.Providers[ProviderName]
	if p.BaseURL != "http://x:11434/v1" || p.API != "openai-completions" || len(p.Models) != 2 || p.Models[0]["id"] != "m1" || p.Models[1]["id"] != "m2" {
		t.Fatalf("models.json: %+v", p)
	}
	ext, _ := os.ReadFile(l.ExtensionPath)
	if !strings.Contains(string(ext), "registerTool") || !strings.Contains(string(ext), "GITINBOX_API_TOKEN") {
		t.Fatal("extension not written")
	}
	// Idempotent: a second Prepare keeps the file.
	if _, err := Prepare(DirConfig{Dir: dir, OllamaURL: "http://x:11434", DefaultModel: "m1"}); err != nil {
		t.Fatal(err)
	}
	prompt, err := RenderPrompt(PromptData{Date: "2026-09-20", Accounts: []PromptAccount{{Ref: "me@github.com", Forge: "github", WriteMode: "readonly"}}, Interests: "CiviCRM", Profiles: []string{"CiviCRM (civicrm/civicrm-core)"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"me@github.com", "readonly", "CiviCRM", "civicrm/civicrm-core", "never post", "2026-09-20"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestLocateMissing(t *testing.T) {
	if _, err := Locate(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for a missing override")
	}
}
