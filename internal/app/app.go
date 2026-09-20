// Package app wires the desktop application's long-lived pieces: database,
// secrets, the bundled MCP server, and the pipeline. Both the Wails services
// and the tray use it.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"ghinbox/internal/mcpbin"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/secrets"
	"ghinbox/internal/store"
)

// App holds the shared runtime state.
type App struct {
	DB      *store.DB
	Secrets secrets.Store
	Pipe    *pipeline.Pipeline
	MCP     mcpbin.Info
	MCPErr  error
	Logger  *slog.Logger
	DBPath  string
	LogPath string

	cancel context.CancelFunc
}

// Open initialises everything. emit forwards pipeline events to the UI.
func Open(emit func(name string, data any)) (*App, error) {
	dbPath := os.Getenv("GHINBOX_DB")
	if dbPath == "" {
		var err error
		if dbPath, err = store.DefaultPath(); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}

	logPath := filepath.Join(filepath.Dir(dbPath), "ghinbox.log")
	var logSink io.Writer = os.Stderr
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		logSink = io.MultiWriter(os.Stderr, f)
	}
	level := slog.LevelInfo
	if os.Getenv("GHINBOX_DEBUG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(logSink, &slog.HandlerOptions{Level: level}))

	a := &App{DB: db, Secrets: secrets.Open(), Logger: logger, DBPath: dbPath, LogPath: logPath}
	a.MCP, a.MCPErr = mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GHINBOX_MCP_PATH")})
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
	})
	return a, nil
}

// StartScheduler runs periodic syncs in the background until Close.
func (a *App) StartScheduler(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.Pipe.RunScheduler(ctx, interval)
}

// Close stops the scheduler, MCP servers and database.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.Pipe.Close()
	_ = a.DB.Close()
}
