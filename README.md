# GitInbox

Desktop dashboard that pulls GitHub notifications for one or more accounts through the
official GitHub MCP server, triages them with typed AI judgments (TypeSafe Jev) and a
self-hosted Ollama, and surfaces what needs you: prioritized inbox, "mine" lists, and
PR impact analysis for upstream projects you build on.

Architecture, decisions, and milestones live in [PLAN.md](PLAN.md). Current state is
milestone **M1**: accounts + keyring, MCP client to the bundled GitHub MCP server, notification
sync with deterministic classification and noise filtering, "Mine" searches, an Inbox/Mine/
Settings/Diagnostics UI with local read/done/snooze, a tray badge, and a headless CLI; **M1.5** adds
GitLab (gitlab.com or self-hosted) as a second source: To-Do list, watched-project activity, and Mine
via the official REST client. **M2** adds typed triage judgments: TypeSafe Jev (calibrated) or an
Ollama model (uncalibrated fallback) answers six questions per thread (category, needs me, urgency,
relevance, resolved, next action); a weighted score orders the inbox and re-ranks instantly when
weights change; an explain panel shows the probabilities and the state the judge saw. **M3** adds
the PR impact engine: repo-agnostic impact profiles (built-in CiviCRM + generic, editable YAML),
deterministic analysis of a PR/MR's changed files (layers touched, signal keywords, surface-pattern
hits), `impact.v1` judgments (change kind, downstream impact none…certain, backward compatibility),
Ollama-written impact notes, an Impact view (proposed / landed) and a scan of recently merged PRs
in profile repos. **M4** adds prose: Ollama thread summaries (lazy: on open, top-N after sync, with
"what changed since last read"), a period digest, desktop notifications for threads entering
"Needs me" and for high-impact PRs, and a usage/latency table. **M5** adds evaluation: label threads
(category, needs action, urgency, relevance, priority, resolved, noise) and PRs (impact, change kind)
in the Eval view or CLI, re-judge the labeled set with any provider, and get category accuracy,
precision/recall + Brier, ordinal error, NDCG/Spearman rank agreement, filter recall, and a weight
tuner that maximises NDCG on your priority labels.

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
wails3 build          # production binary in bin/gitinbox (use this or `go build .`; `go build ./...` trips on the template's build/ios files)
go test ./...         # Go unit tests
go run ./cmd/gitinbox mcp path   # headless check: which github-mcp-server would be used
```

### Headless CLI

```sh
go build -o bin/gitinbox-cli ./cmd/gitinbox
bin/gitinbox-cli account add --token-file ~/.config/gitinbox/pat.txt   # validates with get_me, stores in keyring
bin/gitinbox-cli mcp tools            # what the server registered (read-only by default) + our allowlist
bin/gitinbox-cli sync --full          # pull the last 7 days incl. read; later runs are incremental
bin/gitinbox-cli inbox                # grouped by activity kind; --all/--noise/--done widen it
bin/gitinbox-cli mine                 # assigned / mentioned / review-requested / authored
bin/gitinbox-cli done <thread-id>     # local state; mirrored to GitHub only in write mode 'notifications'
bin/gitinbox-cli account add --forge gitlab --host lab.civicrm.org --token-file ~/.config/gitinbox/pat-lab.civicrm.org.txt
bin/gitinbox-cli watch add --account <login> dev/core   # GitLab: poll a project's activity each sync
bin/gitinbox-cli jev-key --token-file ~/.config/gitinbox/jev-key.txt   # TypeSafe Jev key → keyring
bin/gitinbox-cli settings set ollama.url http://gpu-box:11434 && bin/gitinbox-cli settings set ollama.model qwen3:8b
bin/gitinbox-cli settings set interests "CiviCRM extensions using APIv4, Drupal integration"
bin/gitinbox-cli judge                # enrich + judge unread threads lacking a fresh judgment
bin/gitinbox-cli explain --now <thread-id>   # answers, probabilities, state and score for one thread
bin/gitinbox-cli analyze civicrm/civicrm-core#36990 --note   # impact analysis (+ Ollama note) for one PR
bin/gitinbox-cli analyze --pending          # analyse pending PR threads in profile repos + scan landed changes
bin/gitinbox-cli impact --min 2 --landed    # analysed PRs at level likely+ that already merged
bin/gitinbox-cli profiles list | show civicrm | import my-profile.yaml
bin/gitinbox-cli summarize <thread-id>       # Ollama summary (key points, asks of me, changed since last read)
bin/gitinbox-cli digest --generate --hours 24   # period digest; `digest` shows the latest
bin/gitinbox-cli usage                       # tokens and latency per provider/model
bin/gitinbox-cli eval queue                  # what to label next; label in the Eval view or:
bin/gitinbox-cli eval label <thread-id> category=needs_my_reply action=y urgency=2 relevance=2 priority=3 resolved=n noise=n
bin/gitinbox-cli eval judge --provider ollama   # judge the labeled set with a second provider
bin/gitinbox-cli eval report                 # metrics per provider → eval/report-<stamp>.md
bin/gitinbox-cli eval tune --apply           # weights that maximise NDCG@25 on your labels
```

Judge providers: **Jev** (TypeSafe) is calibrated and fast (~0.6 s/thread). **Ollama** is the
uncalibrated fallback; pick a model that fits entirely in VRAM (a 27B model that spills to CPU took
~5 min per thread; a 9B model ~1–2 min) and keep `max` small. Weight changes re-rank from stored
answers without new inference.

The CLI and the desktop app share the same database (`$XDG_DATA_HOME/gitinbox/gitinbox.db`)
and keyring entries. `GITINBOX_DEBUG=1` shows server stderr and debug logs.

The app was called `ghinbox` until 2026-09-20. On first start the renamed binaries move
`$XDG_DATA_HOME/ghinbox/` to `$XDG_DATA_HOME/gitinbox/` (renaming `ghinbox.db`/`.log`) and copy
keyring entries from service `ghinbox` to `gitinbox` on first read; the token files you keep
under `~/.config/` can stay wherever they are — the CLI only takes their path.

### GitHub writes are off by default

Each account has a write mode. `readonly` (default) starts `github-mcp-server` with
`--read-only`, so no write tool even exists; mark read/done/snooze/mute are local state.
`notifications` restarts the server without that flag and allows exactly two tools —
`dismiss_notification` and `manage_notification_subscription` — through a client-side
allowlist; nothing touching issues, PRs, comments, labels or repos is ever callable.
With a classic token the server additionally registers only tools its scopes permit, so a
`notifications`-only token never exposes issue/PR writes at all. See PLAN.md §4.9.

GitLab accounts have no MCP server: reads use the official REST client with a `read_api` token;
in `notifications` mode the only writes are marking a to-do done and unsubscribing from an
issue/MR (needs an `api`-scoped token). "Read" is local-only on GitLab.

`wails3 build` runs `common:build:mcp`, which compiles `github-mcp-server` for the target
OS/arch into `internal/mcpbin/bin/` (git-ignored) so `//go:embed` bundles it. At runtime
`mcpbin.Locate` extracts it once to `$XDG_DATA_HOME/gitinbox/bin/` and prefers, in order:
an explicit override path → the bundled copy → `github-mcp-server` on `PATH`.

## Layout

```
main.go                  Wails application entry point
cmd/gitinbox/             headless CLI (accounts, mcp tools/call, sync, inbox, mine, triage)
internal/app/            wiring: db + secrets + MCP binary + pipeline
internal/ghmcp/          MCP client for github-mcp-server, per-write-mode tool allowlist
internal/mcpbin/         locate/extract the bundled GitHub MCP server
internal/store/          SQLite (pure Go) + embedded migrations
internal/secrets/        OS keyring with 0600-file fallback
internal/classify/       activity kind + relation tags from notification fields
internal/filter/         rule-based noise filter (bots, green CI, mutes)
internal/pipeline/       sync → classify → filter → store; mine searches; local actions; scheduler
internal/source/         forge seam; source/github (ghmcp) and source/gitlab (client-go REST)
internal/judge/          typed judgments: triage.v1 rubric, jev (TypeSafe) and ollama providers
internal/scoring/        composite priority from stored judgments + user weights (+ impact term)
internal/profiles/       impact profiles (YAML, built-ins embedded), layer/signal/surface analysis
internal/llm/            schema-constrained generation (Ollama): impact notes, summaries, digests
internal/eval/           metrics (binary/ordinal/categorical/NDCG) and the weight tuner
internal/services/       Wails services: Accounts, Inbox, Mine, Diagnostics, Watches, Judge
frontend/                React + TS + Tailwind (Vite)
build/                   Wails build config and platform Taskfiles
.github/workflows/       Linux amd64 + arm64 CI builds
```
