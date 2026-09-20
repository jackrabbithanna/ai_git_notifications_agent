package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed ext/gitinbox.ts
var extensionSource string

//go:embed prompt.md
var promptTemplate string

// ProviderName is the models.json provider the agent uses.
const ProviderName = "ollama"

// DirConfig describes the app-owned agent directory (PI_CODING_AGENT_DIR).
type DirConfig struct {
	Dir           string   // e.g. $XDG_DATA_HOME/gitinbox/agent
	OllamaURL     string   // base URL of the Ollama server (without /v1)
	DefaultModel  string   // model id pi starts with
	Models        []string // every model to list (DefaultModel is added when missing)
	ContextWindow int      // tokens; 0 = 32768
	MaxTokens     int      // output tokens; 0 = 4096
}

// Layout is what Prepare wrote.
type Layout struct {
	Dir           string `json:"dir"`
	ExtensionPath string `json:"extensionPath"`
	SessionsDir   string `json:"sessionsDir"`
	ModelsPath    string `json:"modelsPath"`
}

// Prepare writes models.json, settings.json and the GitInbox extension into
// the agent directory. It is idempotent and safe to call before every start.
func Prepare(cfg DirConfig) (Layout, error) {
	if cfg.Dir == "" {
		return Layout{}, fmt.Errorf("agent: directory required")
	}
	l := Layout{Dir: cfg.Dir, ExtensionPath: filepath.Join(cfg.Dir, "extensions", "gitinbox.ts"), SessionsDir: filepath.Join(cfg.Dir, "sessions"), ModelsPath: filepath.Join(cfg.Dir, "models.json")}
	for _, d := range []string{cfg.Dir, filepath.Dir(l.ExtensionPath), l.SessionsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return l, err
		}
	}
	if cfg.ContextWindow <= 0 {
		cfg.ContextWindow = 32768
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range append([]string{cfg.DefaultModel}, cfg.Models...) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		ids = append(ids, m)
	}
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, map[string]any{"id": id, "name": id, "reasoning": false, "input": []string{"text"}, "contextWindow": cfg.ContextWindow, "maxTokens": cfg.MaxTokens,
			"cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}})
	}
	modelsJSON := map[string]any{"providers": map[string]any{ProviderName: map[string]any{
		"baseUrl": strings.TrimSuffix(cfg.OllamaURL, "/") + "/v1",
		"api":     "openai-completions",
		"apiKey":  "ollama",
		"compat":  map[string]any{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
		"models":  models,
	}}}
	if err := writeJSONFile(l.ModelsPath, modelsJSON); err != nil {
		return l, err
	}
	settings := map[string]any{
		"defaultProvider": ProviderName, "defaultModel": cfg.DefaultModel, "defaultThinkingLevel": "off",
		"quietStartup": true, "enableInstallTelemetry": false, "defaultProjectTrust": "never",
		"compaction": map[string]any{"enabled": true, "reserveTokens": 4096, "keepRecentTokens": 8000},
		"retry":      map[string]any{"enabled": true, "maxRetries": 2},
		"sessionDir": l.SessionsDir,
	}
	if err := writeJSONFile(filepath.Join(cfg.Dir, "settings.json"), settings); err != nil {
		return l, err
	}
	if cur, err := os.ReadFile(l.ExtensionPath); err != nil || string(cur) != extensionSource {
		if err := os.WriteFile(l.ExtensionPath, []byte(extensionSource), 0o600); err != nil {
			return l, err
		}
	}
	return l, nil
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// PromptData fills the system prompt template.
type PromptData struct {
	Date      string
	Accounts  []PromptAccount
	Interests string
	Profiles  []string
	ReadTools []string
}

// PromptAccount is one configured account as the prompt describes it.
type PromptAccount struct {
	Ref       string // login@host
	Forge     string
	WriteMode string
}

// RenderPrompt renders the system prompt.
func RenderPrompt(d PromptData) (string, error) {
	t, err := template.New("prompt").Parse(promptTemplate)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}
