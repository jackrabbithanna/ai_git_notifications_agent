// Package services holds the Go services bound to the frontend by Wails.
package services

import (
	"ghinbox/internal/mcpbin"
)

// DiagnosticsService exposes environment checks to the Diagnostics view.
type DiagnosticsService struct{}

// MCPServerInfo describes the github-mcp-server binary the app would launch.
type MCPServerInfo struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// MCPServerInfo locates the GitHub MCP server binary (bundled, override, or PATH).
func (s *DiagnosticsService) MCPServerInfo() MCPServerInfo {
	info, err := mcpbin.Locate(mcpbin.Options{})
	if err != nil {
		return MCPServerInfo{Error: err.Error()}
	}
	return MCPServerInfo{Path: info.Path, Source: string(info.Source), Version: info.Version}
}
