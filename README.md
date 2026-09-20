# GH Inbox

Desktop dashboard that pulls GitHub notifications for one or more accounts through the
official GitHub MCP server, triages them with typed AI judgments (TypeSafe Jev) and a
self-hosted Ollama, and surfaces what needs you: prioritized inbox, "mine" lists, and
PR impact analysis for upstream projects you build on.

Architecture, decisions, and milestones live in [PLAN.md](PLAN.md). Current state is
milestone **M1**: accounts + keyring, MCP client to the bundled GitHub MCP server, notification
sync with deterministic classification and noise filtering, "Mine" searches, an Inbox/Mine/
Settings/Diagnostics UI with local read/done/snooze, a tray badge, and a headless CLI. No AI yet.

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
wails3 build          # production binary in bin/ghinbox (use this or `go build .`; `go build ./...` trips on the template's build/ios files)
go test ./...         # Go unit tests
go run ./cmd/ghinbox mcp path   # headless check: which github-mcp-server would be used
```

### Headless CLI

```sh
go build -o bin/ghinbox-cli ./cmd/ghinbox
bin/ghinbox-cli account add --token-file ~/.config/ghinbox/pat.txt   # validates with get_me, stores in keyring
bin/ghinbox-cli mcp tools            # what the server registered (read-only by default) + our allowlist
bin/ghinbox-cli sync --full          # pull the last 7 days incl. read; later runs are incremental
bin/ghinbox-cli inbox                # grouped by activity kind; --all/--noise/--done widen it
bin/ghinbox-cli mine                 # assigned / mentioned / review-requested / authored
bin/ghinbox-cli done <thread-id>     # local state; mirrored to GitHub only in write mode 'notifications'
```

The CLI and the desktop app share the same database (`$XDG_DATA_HOME/ghinbox/ghinbox.db`)
and keyring entries. `GHINBOX_DEBUG=1` shows server stderr and debug logs.

### GitHub writes are off by default

Each account has a write mode. `readonly` (default) starts `github-mcp-server` with
`--read-only`, so no write tool even exists; mark read/done/snooze/mute are local state.
`notifications` restarts the server without that flag and allows exactly two tools —
`dismiss_notification` and `manage_notification_subscription` — through a client-side
allowlist; nothing touching issues, PRs, comments, labels or repos is ever callable.
With a classic token the server additionally registers only tools its scopes permit, so a
`notifications`-only token never exposes issue/PR writes at all. See PLAN.md §4.9.

`wails3 build` runs `common:build:mcp`, which compiles `github-mcp-server` for the target
OS/arch into `internal/mcpbin/bin/` (git-ignored) so `//go:embed` bundles it. At runtime
`mcpbin.Locate` extracts it once to `$XDG_DATA_HOME/ghinbox/bin/` and prefers, in order:
an explicit override path → the bundled copy → `github-mcp-server` on `PATH`.

## Layout

```
main.go                  Wails application entry point
cmd/ghinbox/             headless CLI (accounts, mcp tools/call, sync, inbox, mine, triage)
internal/app/            wiring: db + secrets + MCP binary + pipeline
internal/ghmcp/          MCP client for github-mcp-server, per-write-mode tool allowlist
internal/mcpbin/         locate/extract the bundled GitHub MCP server
internal/store/          SQLite (pure Go) + embedded migrations
internal/secrets/        OS keyring with 0600-file fallback
internal/classify/       activity kind + relation tags from notification fields
internal/filter/         rule-based noise filter (bots, green CI, mutes)
internal/pipeline/       sync → classify → filter → store; mine searches; local actions; scheduler
internal/services/       Wails services: Accounts, Inbox, Mine, Diagnostics
frontend/                React + TS + Tailwind (Vite)
build/                   Wails build config and platform Taskfiles
.github/workflows/       Linux amd64 + arm64 CI builds
```
