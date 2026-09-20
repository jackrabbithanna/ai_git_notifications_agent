// Package services holds the Go services bound to the frontend by Wails.
package services

import (
	"context"
	"errors"

	"ghinbox/internal/app"
	"ghinbox/internal/filter"
	"ghinbox/internal/ghmcp"
	"ghinbox/internal/mcpbin"
)

// DiagnosticsService exposes environment checks to the Diagnostics view.
type DiagnosticsService struct {
	App *app.App
}

// MCPServerInfo describes the github-mcp-server binary the app launches.
type MCPServerInfo struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// MCPServerInfo locates the GitHub MCP server binary (bundled, override, or PATH).
func (s *DiagnosticsService) MCPServerInfo() MCPServerInfo {
	if s.App == nil {
		info, err := mcpbin.Locate(mcpbin.Options{})
		if err != nil {
			return MCPServerInfo{Error: err.Error()}
		}
		return MCPServerInfo{Path: info.Path, Source: string(info.Source), Version: info.Version}
	}
	if s.App.MCPErr != nil {
		return MCPServerInfo{Error: s.App.MCPErr.Error()}
	}
	return MCPServerInfo{Path: s.App.MCP.Path, Source: string(s.App.MCP.Source), Version: s.App.MCP.Version}
}

// Environment summarises where data lives.
type Environment struct {
	DBPath         string `json:"dbPath"`
	LogPath        string `json:"logPath"`
	SecretsBackend string `json:"secretsBackend"`
}

func (s *DiagnosticsService) Environment() Environment {
	if s.App == nil {
		return Environment{}
	}
	return Environment{DBPath: s.App.DBPath, LogPath: s.App.LogPath, SecretsBackend: s.App.Secrets.Backend()}
}

// Tools lists the tools the account's server exposes, annotated with our allowlist.
func (s *DiagnosticsService) Tools(accountID int64) ([]ghmcp.ToolInfo, error) {
	if s.App == nil {
		return nil, errors.New("app not initialised")
	}
	ctx := context.Background()
	acct, err := s.App.DB.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	client, err := s.App.Pipe.MCPClient(ctx, acct)
	if err != nil {
		return nil, err
	}
	return client.ListTools(ctx)
}

// ClientStats returns MCP call counters per account id.
func (s *DiagnosticsService) ClientStats() map[int64]ghmcp.Stats {
	if s.App == nil {
		return map[int64]ghmcp.Stats{}
	}
	return s.App.Pipe.ClientStats()
}

// FilterRules returns the current noise-filter rules.
func (s *DiagnosticsService) FilterRules() (filter.Rules, error) {
	if s.App == nil {
		return filter.Defaults(), nil
	}
	return s.App.Pipe.Rules(context.Background())
}

// SetFilterRules stores new rules; they apply to threads on their next sync.
func (s *DiagnosticsService) SetFilterRules(r filter.Rules) error {
	if s.App == nil {
		return errors.New("app not initialised")
	}
	return s.App.Pipe.SetRules(context.Background(), r)
}
