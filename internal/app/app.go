// Package app wires the desktop application's long-lived pieces: database,
// secrets, the bundled MCP server, the pipeline, and (M6) the loopback API
// plus the pi agent sidecar. Both the Wails services and the CLI use it.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitinbox/internal/agent"
	"gitinbox/internal/judge/ollama"
	"gitinbox/internal/localapi"
	"gitinbox/internal/mcpbin"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/profiles"
	"gitinbox/internal/secrets"
	gitlabsrc "gitinbox/internal/source/gitlab"
	"gitinbox/internal/store"
)

// AgentEvent is the UI event name carrying agent.UIEvent payloads.
const AgentEvent = "agent:event"

// App holds the shared runtime state.
type App struct {
	DB      *store.DB
	Secrets secrets.Store
	Pipe    *pipeline.Pipeline
	MCP     mcpbin.Info
	MCPErr  error
	Logger  *slog.Logger
	LogSink io.Writer
	DBPath  string
	LogPath string
	Notify  func(n pipeline.Notification) // desktop notification sink (nil in the CLI)
	Emit    func(name string, data any)   // UI event sink (never nil)

	// M6: loopback API and the pi sidecar, created on first use.
	API   *localapi.Server
	Agent *agent.Manager

	agentMu  sync.Mutex
	agentCfg agent.Config
	cancel   context.CancelFunc
}

// Open initialises everything. emit forwards pipeline events to the UI; notify
// (optional) delivers desktop notifications.
func Open(emit func(name string, data any), notify func(n pipeline.Notification)) (*App, error) {
	if emit == nil {
		emit = func(string, any) {}
	}
	dbPath := os.Getenv("GITINBOX_DB")
	if dbPath == "" {
		var err error
		if dbPath, err = store.DefaultPath(); err != nil {
			return nil, err
		}
	}
	if moved, err := store.MigrateLegacyDataDir(dbPath); err != nil {
		return nil, fmt.Errorf("migrate legacy data dir: %w", err)
	} else if moved {
		fmt.Fprintf(os.Stderr, "migrated data directory from ghinbox to %s\n", filepath.Dir(dbPath))
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}

	logPath := filepath.Join(filepath.Dir(dbPath), "gitinbox.log")
	var logSink io.Writer = os.Stderr
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		logSink = io.MultiWriter(os.Stderr, f)
	}
	level := slog.LevelInfo
	if os.Getenv("GITINBOX_DEBUG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: level}))

	a := &App{DB: db, Secrets: secrets.Open(), Logger: logger, LogSink: logSink, DBPath: dbPath, LogPath: logPath, Notify: notify, Emit: emit}
	a.MCP, a.MCPErr = mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GITINBOX_MCP_PATH")})
	if a.MCPErr != nil {
		logger.Warn("github-mcp-server not found", "err", a.MCPErr)
	}
	a.Pipe = pipeline.New(pipeline.Deps{
		DB:        db,
		Secrets:   a.Secrets,
		MCPPath:   a.MCP.Path,
		Logger:    logger,
		Emit:      emit,
		ServerLog: logSink,
		Notify:    notify,
	})
	a.Pipe.SetGitLabFactory(gitlabsrc.Factory())
	return a, nil
}

// StartScheduler runs periodic syncs in the background until Close.
func (a *App) StartScheduler(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.Pipe.RunScheduler(ctx, interval)
}

// Close stops the scheduler, the agent, the local API, MCP servers and database.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.StopAgent()
	a.agentMu.Lock()
	if a.API != nil {
		_ = a.API.Close()
	}
	a.agentMu.Unlock()
	a.Pipe.Close()
	_ = a.DB.Close()
}

// --- M6: agent sidecar --------------------------------------------------------

// ErrAgentDisabled is returned while the agent is switched off in Settings.
var ErrAgentDisabled = errors.New("the agent is disabled (Settings → Agent)")

// AgentManager returns the sidecar manager built from the current settings,
// starting the loopback API on first use. A running sidecar is restarted only
// when a setting that affects it changed.
func (a *App) AgentManager(ctx context.Context) (*agent.Manager, error) {
	a.agentMu.Lock()
	defer a.agentMu.Unlock()
	settings, err := a.Pipe.AgentSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, ErrAgentDisabled
	}
	if a.API == nil {
		a.API = localapi.New(a.DB, a.Pipe, a.Logger)
	}
	if a.API.URL() == "" {
		if err := a.API.Start(); err != nil {
			return nil, fmt.Errorf("local API: %w", err)
		}
	}
	cfg, err := a.agentConfig(ctx, settings)
	if err != nil {
		return nil, err
	}
	switch {
	case a.Agent == nil:
		a.Agent = agent.New(cfg)
	case cfgChanged(a.agentCfg, cfg):
		a.Agent.Reconfigure(cfg)
	}
	a.agentCfg = cfg
	return a.Agent, nil
}

func cfgChanged(old, cur agent.Config) bool {
	return old.PiPath != cur.PiPath || old.Model != cur.Model || old.Thinking != cur.Thinking || old.OllamaURL != cur.OllamaURL ||
		old.SystemPrompt != cur.SystemPrompt || old.APIURL != cur.APIURL || strings.Join(old.Models, ",") != strings.Join(cur.Models, ",")
}

func (a *App) agentConfig(ctx context.Context, settings pipeline.AgentSettings) (agent.Config, error) {
	url, model := a.Pipe.AgentModel(ctx)
	var models []string
	if url != "" {
		mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if list, err := ollama.New(ollama.Config{BaseURL: url, Model: model}).Models(mctx); err == nil {
			models = list
		}
		cancel()
	}
	prompt, err := a.agentPrompt(ctx)
	if err != nil {
		return agent.Config{}, err
	}
	return agent.Config{
		PiPath: settings.PiPath, DataDir: filepath.Dir(a.DBPath), OllamaURL: url, Model: model, Models: models, Thinking: settings.Thinking,
		APIURL: a.API.URL(), APIToken: a.API.Token(), SystemPrompt: prompt, Logger: a.Logger, Stderr: a.LogSink,
		OnEvent: func(ev agent.UIEvent) { a.Emit(AgentEvent, ev) },
	}, nil
}

func (a *App) agentPrompt(ctx context.Context) (string, error) {
	accts, err := a.DB.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	js, _ := a.Pipe.JudgeSettings(ctx)
	data := agent.PromptData{Date: time.Now().Format("2006-01-02 (Monday)"), Interests: strings.TrimSpace(js.ProfileInterests), ReadTools: pipeline.ReadTools()}
	for _, acct := range accts {
		data.Accounts = append(data.Accounts, agent.PromptAccount{Ref: acct.Login + "@" + acct.Host, Forge: acct.Forge, WriteMode: acct.WriteMode})
	}
	if profs, _, err := a.Pipe.Profiles(ctx); err == nil {
		for _, p := range profs {
			if p.Enabled && p.ID != profiles.GenericID {
				data.Profiles = append(data.Profiles, p.Name+" ("+strings.Join(p.Repos, ", ")+")")
			}
		}
	}
	return agent.RenderPrompt(data)
}

// StartAgent builds the manager from settings and starts pi.
func (a *App) StartAgent(ctx context.Context) error {
	m, err := a.AgentManager(ctx)
	if err != nil {
		return err
	}
	return m.Start(ctx)
}

// StopAgent stops the sidecar if it runs.
func (a *App) StopAgent() {
	a.agentMu.Lock()
	m := a.Agent
	a.agentMu.Unlock()
	if m != nil {
		_ = m.Stop()
	}
}
