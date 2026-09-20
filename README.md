# GH Inbox

Desktop dashboard that pulls GitHub notifications for one or more accounts through the
official GitHub MCP server, triages them with typed AI judgments (TypeSafe Jev) and a
self-hosted Ollama, and surfaces what needs you: prioritized inbox, "mine" lists, and
PR impact analysis for upstream projects you build on.

Architecture, decisions, and milestones live in [PLAN.md](PLAN.md). This is milestone
**M0**: the Wails v3 scaffold, Tailwind frontend shell, and the bundled GitHub MCP server
binary wired through a Diagnostics card.

## Stack

- [Wails v3](https://v3.wails.io) (Go backend, GTK4 + WebKitGTK 6.0 on Linux)
- React + TypeScript + Tailwind v4 (`frontend/`)
- [github-mcp-server](https://github.com/github/github-mcp-server), pinned as a Go
  `tool` dependency and embedded into the app binary (`internal/mcpbin`)

## Prerequisites (Linux)

```sh
sudo apt install build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
```

Go 1.25+ and Node 22+ are required (`go.mod` / `frontend/package.json`).

## Develop

```sh
wails3 dev            # hot-reloading app; also stages the MCP server binary
wails3 build          # production binary in bin/ghinbox
go test ./...         # Go unit tests
go run ./cmd/ghinbox mcp path   # headless check: which github-mcp-server would be used
```

`wails3 build` runs `common:build:mcp`, which compiles `github-mcp-server` for the target
OS/arch into `internal/mcpbin/bin/` (git-ignored) so `//go:embed` bundles it. At runtime
`mcpbin.Locate` extracts it once to `$XDG_DATA_HOME/ghinbox/bin/` and prefers, in order:
an explicit override path → the bundled copy → `github-mcp-server` on `PATH`.

## Layout

```
main.go                  Wails application entry point
cmd/ghinbox/             headless CLI (M0: `mcp path`, `version`)
internal/mcpbin/         locate/extract the bundled GitHub MCP server
internal/services/       Go services bound to the frontend (M0: DiagnosticsService)
frontend/                React + TS + Tailwind (Vite)
build/                   Wails build config and platform Taskfiles
.github/workflows/       Linux amd64 + arm64 CI builds
```
