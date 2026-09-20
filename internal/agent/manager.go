package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config is everything the manager needs to run pi for GitInbox.
type Config struct {
	PiPath       string   // override; "" = Locate
	DataDir      string   // the app data directory; the agent dir is DataDir/agent
	OllamaURL    string   // Ollama base URL (without /v1)
	Model        string   // model id to start with
	Models       []string // additional model ids to list
	Thinking     string   // off | low | medium | high
	APIURL       string   // loopback API base URL
	APIToken     string   // loopback API bearer token
	SystemPrompt string
	Logger       *slog.Logger
	Stderr       io.Writer     // pi's stderr sink (nil = discard)
	OnEvent      func(UIEvent) // UI event sink (nil = none)
}

// UIEvent is the compact event stream the UI subscribes to.
type UIEvent struct {
	Type       string `json:"type"` // status | agent_start | agent_end | text_delta | thinking_delta | message_end | tool_start | tool_end | error | exit
	Delta      string `json:"delta,omitempty"`
	MessageID  int    `json:"messageId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Args       any    `json:"args,omitempty"`
	Result     string `json:"result,omitempty"`
	IsError    bool   `json:"isError,omitempty"`
	Error      string `json:"error,omitempty"`
	Busy       bool   `json:"busy"`
}

// Message is one transcript entry.
type Message struct {
	ID   int       `json:"id"`
	Role string    `json:"role"` // user | assistant | tool | error | system
	Text string    `json:"text"`
	Tool *ToolCall `json:"tool,omitempty"`
	At   time.Time `json:"at"`
	Done bool      `json:"done"`
}

// ToolCall is a tool execution shown in the transcript.
type ToolCall struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Args    any    `json:"args"`
	Result  string `json:"result"`
	IsError bool   `json:"isError"`
	Done    bool   `json:"done"`
}

// ModelInfo is one entry of pi's available models.
type ModelInfo struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

// Status is the manager's state for the UI/CLI.
type Status struct {
	Running     bool       `json:"running"`
	Busy        bool       `json:"busy"`
	Path        string     `json:"path"`
	Source      string     `json:"source"`
	Version     string     `json:"version"`
	AgentDir    string     `json:"agentDir"`
	SessionFile string     `json:"sessionFile"`
	Provider    string     `json:"provider"`
	Model       string     `json:"model"`
	APIURL      string     `json:"apiUrl"`
	Messages    int        `json:"messages"`
	Error       string     `json:"error"`
	StartedAt   *time.Time `json:"startedAt"`
}

// Manager owns one pi process and its transcript.
type Manager struct {
	cfg Config

	mu         sync.Mutex
	client     *Client
	info       Info
	layout     Layout
	running    bool
	busy       bool
	lastErr    string
	startedAt  *time.Time
	sessionFil string
	provider   string
	model      string
	transcript []Message
	nextID     int
	current    *Message // assistant message being streamed
	idle       chan struct{}
}

// New builds a manager; nothing runs until Start or the first Prompt.
func New(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Thinking == "" {
		cfg.Thinking = "off"
	}
	return &Manager{cfg: cfg, idle: closedChan()}
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Reconfigure replaces the configuration; a running sidecar is stopped so the
// next Start picks the new settings up.
func (m *Manager) Reconfigure(cfg Config) {
	_ = m.Stop()
	m.mu.Lock()
	if cfg.Logger == nil {
		cfg.Logger = m.cfg.Logger
	}
	if cfg.OnEvent == nil {
		cfg.OnEvent = m.cfg.OnEvent
	}
	if cfg.Thinking == "" {
		cfg.Thinking = "off"
	}
	m.cfg = cfg
	m.mu.Unlock()
}

// Locate reports which pi binary would be used (with its version).
func (m *Manager) Locate(ctx context.Context) (Info, error) {
	info, err := Locate(m.cfg.PiPath)
	if err != nil {
		return Info{}, err
	}
	if v, err := Version(ctx, info.Path); err == nil {
		info.Version = v
	}
	return info, nil
}

// Start prepares the agent directory and spawns pi.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	cfg := m.cfg
	m.mu.Unlock()

	if cfg.Model == "" {
		return m.fail(errors.New("no Ollama model configured for the agent (Settings → Agent or Triage judge)"))
	}
	if cfg.APIURL == "" || cfg.APIToken == "" {
		return m.fail(errors.New("local API not started"))
	}
	info, err := m.Locate(ctx)
	if err != nil {
		return m.fail(err)
	}
	layout, err := Prepare(DirConfig{Dir: filepath.Join(cfg.DataDir, "agent"), OllamaURL: cfg.OllamaURL, DefaultModel: cfg.Model, Models: cfg.Models})
	if err != nil {
		return m.fail(fmt.Errorf("prepare agent dir: %w", err))
	}
	args := []string{"--mode", "rpc", "--no-builtin-tools", "--no-extensions", "-e", layout.ExtensionPath,
		"--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "--offline",
		"--session-dir", layout.SessionsDir, "--provider", ProviderName, "--model", cfg.Model, "--thinking", cfg.Thinking}
	if cfg.SystemPrompt != "" {
		args = append(args, "--system-prompt", cfg.SystemPrompt)
	}
	env := append(os.Environ(),
		"PI_CODING_AGENT_DIR="+layout.Dir, "PI_CODING_AGENT_SESSION_DIR="+layout.SessionsDir,
		"PI_SKIP_VERSION_CHECK=1", "PI_OFFLINE=1", "PI_TELEMETRY=0",
		"GITINBOX_API_URL="+cfg.APIURL, "GITINBOX_API_TOKEN="+cfg.APIToken)
	client, err := Spawn(context.Background(), SpawnOptions{Path: info.Path, Args: args, Env: env, Dir: layout.Dir, Stderr: cfg.Stderr, OnEvent: m.handleEvent, OnExit: m.handleExit})
	if err != nil {
		return m.fail(fmt.Errorf("start pi: %w", err))
	}
	now := time.Now()
	m.mu.Lock()
	m.client, m.info, m.layout, m.running, m.lastErr, m.startedAt = client, info, layout, true, "", &now
	m.provider, m.model = ProviderName, cfg.Model
	m.mu.Unlock()

	// get_state confirms the handshake and reports the resolved model.
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := client.Call(sctx, map[string]any{"type": "get_state"})
	if err != nil {
		_ = m.Stop()
		return m.fail(fmt.Errorf("pi did not answer get_state: %w", err))
	}
	m.applyState(resp.Data)
	m.emit(UIEvent{Type: "status"})
	cfg.Logger.Info("agent started", "pi", info.Path, "version", info.Version, "model", cfg.Model)
	return nil
}

func (m *Manager) fail(err error) error {
	m.mu.Lock()
	m.lastErr = err.Error()
	m.mu.Unlock()
	m.emit(UIEvent{Type: "status", Error: err.Error()})
	return err
}

func (m *Manager) applyState(data json.RawMessage) {
	var st struct {
		Model *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		IsStreaming bool   `json:"isStreaming"`
		SessionFile string `json:"sessionFile"`
	}
	if json.Unmarshal(data, &st) != nil {
		return
	}
	m.mu.Lock()
	if st.Model != nil {
		m.model, m.provider = st.Model.ID, st.Model.Provider
	}
	m.sessionFil = st.SessionFile
	m.busy = st.IsStreaming
	m.mu.Unlock()
}

// Stop ends the pi process (the transcript is kept).
func (m *Manager) Stop() error {
	m.mu.Lock()
	c := m.client
	m.client = nil
	m.running = false
	m.busy = false
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	err := c.Close()
	m.emit(UIEvent{Type: "status"})
	return err
}

func (m *Manager) handleExit(err error) {
	m.mu.Lock()
	wasRunning := m.running
	m.running = false
	m.busy = false
	if wasRunning {
		msg := "pi exited"
		if err != nil {
			msg += ": " + err.Error()
		}
		m.lastErr = msg
		m.append(Message{Role: "error", Text: msg, Done: true})
	}
	idle := m.idle
	m.mu.Unlock()
	select {
	case <-idle:
	default:
		close(idle)
	}
	if wasRunning {
		m.emit(UIEvent{Type: "exit", Error: m.lastErr})
	}
}

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{Running: m.running, Busy: m.busy, Path: m.info.Path, Source: m.info.Source, Version: m.info.Version, AgentDir: m.layout.Dir,
		SessionFile: m.sessionFil, Provider: m.provider, Model: m.model, APIURL: m.cfg.APIURL, Messages: len(m.transcript), Error: m.lastErr, StartedAt: m.startedAt}
	if !m.running && s.Path == "" {
		if info, err := Locate(m.cfg.PiPath); err == nil {
			s.Path, s.Source = info.Path, info.Source
		} else if s.Error == "" {
			s.Error = err.Error()
		}
		s.AgentDir = filepath.Join(m.cfg.DataDir, "agent")
		s.Model = m.cfg.Model
	}
	return s
}

// Transcript returns a copy of the conversation.
func (m *Manager) Transcript() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.transcript))
	copy(out, m.transcript)
	return out
}

func (m *Manager) append(msg Message) *Message {
	m.nextID++
	msg.ID = m.nextID
	if msg.At.IsZero() {
		msg.At = time.Now()
	}
	m.transcript = append(m.transcript, msg)
	return &m.transcript[len(m.transcript)-1]
}

func (m *Manager) emit(ev UIEvent) {
	m.mu.Lock()
	ev.Busy = m.busy
	fn := m.cfg.OnEvent
	m.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

// Prompt sends a user message; pi is started when needed. When the agent is
// still working, the message is queued as a follow-up.
func (m *Manager) Prompt(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("empty prompt")
	}
	if err := m.Start(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	c := m.client
	m.busy = true
	m.idle = make(chan struct{})
	m.append(Message{Role: "user", Text: text, Done: true})
	m.mu.Unlock()
	m.emit(UIEvent{Type: "agent_start"})
	if _, err := c.Call(ctx, map[string]any{"type": "prompt", "message": text}); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "stream") {
			_, err = c.Call(ctx, map[string]any{"type": "prompt", "message": text, "streamingBehavior": "followUp"})
		}
		if err != nil {
			m.mu.Lock()
			m.busy = false
			m.append(Message{Role: "error", Text: err.Error(), Done: true})
			m.mu.Unlock()
			m.emit(UIEvent{Type: "error", Error: err.Error()})
			return err
		}
	}
	return nil
}

// Abort interrupts the current run.
func (m *Manager) Abort(ctx context.Context) error {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	_, err := c.Call(ctx, map[string]any{"type": "abort"})
	return err
}

// NewSession clears the conversation (pi's and ours).
func (m *Manager) NewSession(ctx context.Context) error {
	m.mu.Lock()
	c := m.client
	m.transcript = nil
	m.current = nil
	m.mu.Unlock()
	if c != nil {
		if _, err := c.Call(ctx, map[string]any{"type": "new_session"}); err != nil {
			return err
		}
		if resp, err := c.Call(ctx, map[string]any{"type": "get_state"}); err == nil {
			m.applyState(resp.Data)
		}
	}
	m.emit(UIEvent{Type: "status"})
	return nil
}

// Models lists the models pi knows (from the generated models.json).
func (m *Manager) Models(ctx context.Context) ([]ModelInfo, error) {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		out := []ModelInfo{}
		for _, id := range append([]string{m.cfg.Model}, m.cfg.Models...) {
			if id != "" {
				out = append(out, ModelInfo{Provider: ProviderName, ID: id, Name: id})
			}
		}
		return out, nil
	}
	resp, err := c.Call(ctx, map[string]any{"type": "get_available_models"})
	if err != nil {
		return nil, err
	}
	var data struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, err
	}
	return data.Models, nil
}

// SetModel switches pi to another model of the same provider.
func (m *Manager) SetModel(ctx context.Context, id string) error {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		m.mu.Lock()
		m.cfg.Model = id
		m.mu.Unlock()
		return nil
	}
	if _, err := c.Call(ctx, map[string]any{"type": "set_model", "provider": ProviderName, "modelId": id}); err != nil {
		return err
	}
	m.mu.Lock()
	m.model = id
	m.cfg.Model = id
	m.mu.Unlock()
	m.emit(UIEvent{Type: "status"})
	return nil
}

// WaitIdle blocks until the current run finishes (or ctx ends).
func (m *Manager) WaitIdle(ctx context.Context) error {
	m.mu.Lock()
	idle := m.idle
	m.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LastText returns the last assistant message text.
func (m *Manager) LastText(ctx context.Context) (string, error) {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()
	if c == nil {
		return "", errors.New("pi is not running")
	}
	resp, err := c.Call(ctx, map[string]any{"type": "get_last_assistant_text"})
	if err != nil {
		return "", err
	}
	var data struct {
		Text *string `json:"text"`
	}
	_ = json.Unmarshal(resp.Data, &data)
	if data.Text == nil {
		return "", nil
	}
	return *data.Text, nil
}

// --- event handling ---------------------------------------------------------

const maxToolResult = 8000

func (m *Manager) handleEvent(ev Event) {
	switch ev.Type() {
	case "agent_start":
		m.mu.Lock()
		m.busy = true
		m.mu.Unlock()
		m.emit(UIEvent{Type: "agent_start"})
	case "agent_end":
		m.mu.Lock()
		m.busy = false
		if m.current != nil {
			m.current.Done = true
			m.current = nil
		}
		idle := m.idle
		m.mu.Unlock()
		select {
		case <-idle:
		default:
			close(idle)
		}
		m.emit(UIEvent{Type: "agent_end"})
	case "message_start":
		if role := nested(ev, "message", "role"); role == "assistant" {
			m.mu.Lock()
			m.current = m.append(Message{Role: "assistant"})
			m.mu.Unlock()
		}
	case "message_update":
		d, _ := ev["assistantMessageEvent"].(map[string]any)
		if d == nil {
			return
		}
		switch d["type"] {
		case "text_delta":
			delta, _ := d["delta"].(string)
			m.mu.Lock()
			if m.current == nil {
				m.current = m.append(Message{Role: "assistant"})
			}
			m.current.Text += delta
			id := m.current.ID
			m.mu.Unlock()
			m.emit(UIEvent{Type: "text_delta", Delta: delta, MessageID: id})
		case "thinking_delta":
			delta, _ := d["delta"].(string)
			m.emit(UIEvent{Type: "thinking_delta", Delta: delta})
		}
	case "message_end":
		if nested(ev, "message", "role") != "assistant" {
			return
		}
		msg, _ := ev["message"].(map[string]any)
		m.mu.Lock()
		if m.current != nil {
			// The final message carries the complete text; prefer it over accumulated deltas.
			if full := textOf(msg); full != "" {
				m.current.Text = full
			}
			m.current.Done = true
			id := m.current.ID
			m.current = nil
			var errText string
			if stop, _ := msg["stopReason"].(string); stop == "error" {
				errText, _ = msg["errorMessage"].(string)
				if errText == "" {
					errText = "model error"
				}
				m.append(Message{Role: "error", Text: errText, Done: true})
			}
			m.mu.Unlock()
			m.emit(UIEvent{Type: "message_end", MessageID: id, Error: errText})
			return
		}
		m.mu.Unlock()
	case "tool_execution_start":
		id, name := ev.String("toolCallId"), ev.String("toolName")
		m.mu.Lock()
		m.current = nil
		m.append(Message{Role: "tool", Tool: &ToolCall{ID: id, Name: name, Args: ev["args"]}})
		m.mu.Unlock()
		m.emit(UIEvent{Type: "tool_start", ToolCallID: id, ToolName: name, Args: ev["args"]})
	case "tool_execution_end":
		id, name := ev.String("toolCallId"), ev.String("toolName")
		isErr, _ := ev["isError"].(bool)
		result := resultText(ev["result"])
		if len(result) > maxToolResult {
			result = result[:maxToolResult] + "\n…[truncated]"
		}
		m.mu.Lock()
		for i := len(m.transcript) - 1; i >= 0; i-- {
			t := m.transcript[i].Tool
			if t != nil && t.ID == id {
				t.Result, t.IsError, t.Done = result, isErr, true
				m.transcript[i].Done = true
				break
			}
		}
		m.mu.Unlock()
		m.emit(UIEvent{Type: "tool_end", ToolCallID: id, ToolName: name, Result: result, IsError: isErr})
	case "extension_error":
		text := ev.String("error")
		m.mu.Lock()
		m.append(Message{Role: "error", Text: "extension: " + text, Done: true})
		m.mu.Unlock()
		m.emit(UIEvent{Type: "error", Error: text})
	case "auto_retry_start":
		m.emit(UIEvent{Type: "status", Error: "retrying: " + ev.String("errorMessage")})
	case "extension_ui_request":
		// We never open dialogs; cancel any that an extension might request so pi does not block.
		switch ev.String("method") {
		case "select", "confirm", "input", "editor":
			m.mu.Lock()
			c := m.client
			m.mu.Unlock()
			if c != nil {
				_ = c.Send(map[string]any{"type": "extension_ui_response", "id": ev["id"], "cancelled": true})
			}
		}
	case "stdout":
		m.cfg.Logger.Debug("pi stdout", "text", ev.String("text"))
	}
}

func nested(ev Event, keys ...string) string {
	var cur any = map[string]any(ev)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// textOf joins the text blocks of an assistant message.
func textOf(msg map[string]any) string {
	content, _ := msg["content"].([]any)
	var b strings.Builder
	for _, c := range content {
		if mm, ok := c.(map[string]any); ok && mm["type"] == "text" {
			s, _ := mm["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}

// resultText joins the text blocks of a tool result.
func resultText(v any) string {
	mm, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	content, _ := mm["content"].([]any)
	var b strings.Builder
	for _, c := range content {
		if cm, ok := c.(map[string]any); ok && cm["type"] == "text" {
			s, _ := cm["text"].(string)
			b.WriteString(s)
		}
	}
	return b.String()
}
