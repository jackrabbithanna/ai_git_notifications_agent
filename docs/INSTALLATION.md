# GitInbox — Installation & Setup

This guide takes you from a bare Linux machine to a running GitInbox with at least one
account, a triage judge, and (optionally) an Ollama model for prose. It covers the desktop
app and the headless CLI, which share the same database, secrets and pipeline.

For what the application does and how to use it, see [USER-DOCUMENTATION.md](USER-DOCUMENTATION.md).
For how it is built internally, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 1. What you need

| Requirement | Notes |
|---|---|
| Linux, x86_64 or arm64 | The only supported platform for v1 (macOS/Windows are planned via the Wails build matrix). Development and daily use happen on a Raspberry Pi 5, so arm64 is first-class. |
| GTK 4 + WebKitGTK 6.0 runtime | The desktop window is a WebKitGTK webview. Packages: `libgtk-4-1`, `libwebkitgtk-6.0-4` (Debian/Ubuntu), `gtk4`, `webkitgtk6.0` (Fedora), `gtk4`, `webkitgtk-6.0` (Arch). |
| A Secret Service keyring (recommended) | `gnome-keyring` or KWallet with the Secret Service bridge. Tokens and API keys go there. Without one, GitInbox falls back to a `0600` JSON file (`~/.config/gitinbox/secrets.json`) and says so in Diagnostics. |
| D-Bus session bus | For desktop notifications and the tray icon. Headless use through the CLI needs neither. |
| Network access | `github.com` (or your GitHub Enterprise host), your GitLab hosts, `api.typesafe.ai` for the Jev judge, and your Ollama server. |
| A GitHub and/or GitLab personal access token | Details in §4. |
| Optional: TypeSafe Jev API key | The calibrated triage/impact judge. Get one at typesafe.ai. |
| Optional: an Ollama server | Local or on the LAN; used for summaries, digests, impact notes, as a fallback judge, and by the agent. |
| Optional: pi (pi.dev) + Node 22.19+ | The agent sidecar for the Agent view / `agent chat`. Bring your own: `npm install -g @earendil-works/pi-coding-agent`, or `cd agent && npm install` in the repository. |

For building from source you also need Go 1.26+, Node 22+ with npm, a C compiler, `pkg-config`,
and the GTK/WebKit *development* packages (§3).

---

## 2. Installing a prebuilt binary

There are no published releases yet. Two sources of prebuilt binaries exist:

- **CI artifacts.** Every push to `main` builds `gitinbox-linux-amd64` and `gitinbox-linux-arm64`
  (`.github/workflows/build.yml`). Download the artifact for your architecture, unpack the
  `gitinbox` binary, `chmod +x` it and put it somewhere on your `PATH` (`~/.local/bin` is fine).
- **Packages you build yourself.** `wails3 package` produces a `.deb`, `.rpm`, an Arch package and an
  AppImage under `bin/` (see §3.4). The packages install `/usr/local/bin/gitinbox`, an icon and a
  `.desktop` entry, and declare the GTK4/WebKitGTK runtime dependencies.

The desktop binary contains the frontend and the bundled `github-mcp-server`; nothing else needs to
be installed next to it. The CLI (`gitinbox-cli`) is a separate binary — build it with
`go build -o bin/gitinbox-cli ./cmd/gitinbox` (§3.3) or ask for it from CI.

Install the runtime packages first:

```sh
# Debian 13+ / Ubuntu 24.04+
sudo apt install libgtk-4-1 libwebkitgtk-6.0-4 gnome-keyring
```

---

## 3. Building from source

### 3.1 Toolchain

```sh
# Debian / Ubuntu (24.04 or newer; Raspberry Pi OS Trixie works)
sudo apt install build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev

# Go 1.26+ (https://go.dev/dl) and Node 22+ with npm (https://nodejs.org)
go version && node --version && npm --version

# Wails v3 CLI, pinned to the version the project is built with
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
```

Make sure `$(go env GOPATH)/bin` is on your `PATH` so `wails3` is found.

Notes:

- Wails v3 beta.23 links **GTK 4 + WebKitGTK 6.0** on Linux. If your distribution only ships
  GTK 3 / WebKit2GTK 4.1, the template keeps a `gtk3` build tag as a fallback (see the comments in
  `build/linux/nfpm/nfpm.yaml`); the runtime dependencies change accordingly.
- Only the *development* packages (`-dev`) satisfy `pkg-config`; the runtime libraries alone are not
  enough, and Flatpak SDK copies are not seen by the system `pkg-config`.

### 3.2 Build the desktop app

```sh
git clone <this repository> gitinbox && cd gitinbox
wails3 build            # ≈3 min on a Raspberry Pi 5; produces bin/gitinbox
go test ./...           # unit tests (no network, no tokens)
```

`wails3 build` runs the Taskfile pipeline: `go mod tidy` → build the pinned `github-mcp-server`
into `internal/mcpbin/bin/` (embedded into the app) → `npm install` + Vite production build of
`frontend/` → generate TypeScript bindings → `go build`. Use `wails3 build` or `go build .`;
`go build ./...` fails on the template's `build/ios` files and is not needed.

To run with hot reload during development:

```sh
wails3 dev              # rebuilds Go on change, Vite serves the frontend
```

### 3.3 Build the headless CLI

```sh
go build -o bin/gitinbox-cli ./cmd/gitinbox
bin/gitinbox-cli version          # prints the bundled github-mcp-server version
bin/gitinbox-cli mcp path         # which github-mcp-server binary would be used
```

The CLI needs no GTK, no display and no D-Bus, so it also works over SSH.

### 3.4 Packages (optional)

```sh
wails3 package          # bin/*.deb, *.rpm, Arch package, AppImage
```

Packaging uses `build/linux/nfpm/nfpm.yaml` (name `gitinbox`, version `0.1.0`, GTK4/WebKitGTK
dependencies per distribution) and `build/linux/gitinbox.desktop`.

### 3.5 Cross-compiling

The MCP server is pure Go and cross-compiles for any target. The Wails binary needs the target's
GTK/WebKit libraries; the template's `setup:docker` / `build:docker` tasks build inside a container
when the host lacks a C toolchain. CI builds natively on `ubuntu-24.04` and `ubuntu-24.04-arm`.

---

## 4. Tokens and keys

GitInbox never asks you to paste a token into a shell command. Put each secret in a file with
`0600` permissions and hand the *path* to the CLI, or paste it into the Settings view. The value is
stored in the keyring and never printed or logged.

```sh
mkdir -p ~/.config/gitinbox && chmod 700 ~/.config/gitinbox
# then create the files below with your editor and:
chmod 600 ~/.config/gitinbox/*.txt
```

(The directory name is only a convention; files under `~/.config/ghinbox/` from before the rename
work just as well — the CLI takes any path.)

### 4.1 GitHub

Create a **classic** personal access token (Settings → Developer settings → Personal access
tokens → Tokens (classic)) with:

- `notifications` — required; the notifications API is only reachable with this scope.
- `repo` — **only** if you need notifications for *private* repositories. Public repositories, issue
  and PR reads, searches and PR diffs need no extra scope.

Why classic: the bundled `github-mcp-server` reads the token's `X-OAuth-Scopes` header and
registers only the tools those scopes permit. With a `notifications`-only token the write tools
for issues, PRs and repos never exist on the server, which is the third of the three write-gating
layers GitInbox relies on. Fine-grained tokens work too (grant *Notifications: read*, plus
*Metadata*, *Issues* and *Pull requests* read on the repositories you follow) but do not send that
header, so only the app's own guards apply.

For GitHub Enterprise Server pass the host when adding the account (`--host ghe.example.com`); the
MCP server is started with `GITHUB_HOST` set accordingly.

### 4.2 GitLab (gitlab.com or self-hosted)

Create a personal access token (User settings → Access tokens) with:

- `read_api` — everything GitInbox reads: to-dos, project events, issues, merge requests, diffs,
  `/user`, and `/personal_access_tokens/self` (GitLab 15.5+, used to record the token's scopes).
- `api` — **only** if you later enable the `notifications` write mode for the account, which allows
  exactly two writes: marking a to-do done and unsubscribing from an issue/MR. GitLab has no narrower
  write scope.

One token per instance. Self-hosted instances are addressed by host name (`lab.civicrm.org`).

### 4.3 TypeSafe Jev

Create an API key in your TypeSafe account and save it as `~/.config/gitinbox/jev-key.txt`. Jev is
called at `https://api.typesafe.ai/v1/systemone` with model `jev-latest`; both are overridable
(`settings set jev.url …`, `jev.model …`) if you are told to use a different endpoint or a pinned
model.

---

## 5. First run

Start the app (`bin/gitinbox`, or from your launcher after packaging). With no accounts it opens on
**Settings**. Or do everything from the CLI:

```sh
# GitHub (validates with get_me, stores the token in the keyring)
bin/gitinbox-cli account add --token-file ~/.config/gitinbox/pat.txt

# GitLab, self-hosted (validates with GET /user, records token scopes)
bin/gitinbox-cli account add --forge gitlab --host lab.civicrm.org \
    --token-file ~/.config/gitinbox/pat-lab.civicrm.org.txt

bin/gitinbox-cli account list      # ID, forge, login, host, write mode, scopes; "secrets backend: keyring|file:…"
bin/gitinbox-cli sync --full       # last 7 days including read; later syncs are incremental
bin/gitinbox-cli inbox             # what arrived, grouped by activity kind
```

On first start the application creates:

| Path | Contents |
|---|---|
| `~/.local/share/gitinbox/gitinbox.db` | SQLite database (accounts, threads, judgments, analyses, summaries, labels, settings). `$XDG_DATA_HOME` is honoured; `GITINBOX_DB=/path/to.db` overrides. |
| `~/.local/share/gitinbox/gitinbox.log` | Application log (also written to stderr). |
| `~/.local/share/gitinbox/bin/` | The bundled `github-mcp-server`, extracted once per version. |
| `~/.local/share/gitinbox/agent/` | pi's app-owned config (`models.json`, `settings.json`, `extensions/gitinbox.ts`) and chat sessions; created when the agent first starts. |
| keyring service `gitinbox` | `account:<id>` tokens and `jev.api_key`. |
| `~/.config/gitinbox/secrets.json` | Only when no keyring is reachable (`0600`). |

**Upgrading from `ghinbox`.** The application was renamed on 2026-09-20. The first run of the
renamed binaries moves `~/.local/share/ghinbox/` to `~/.local/share/gitinbox/` (renaming the
database and log) and copies keyring entries from service `ghinbox` to `gitinbox` when first read.
Nothing needs to be done by hand; old keyring entries are left in place and can be deleted with
Seahorse or `secret-tool` at your leisure.

---

## 6. Configure the AI providers

Everything below is optional; without a judge the inbox still syncs, classifies, filters and
sorts by deterministic priors, and every action still works.

### 6.1 Judge (Jev, or Ollama as fallback)

```sh
bin/gitinbox-cli jev-key --token-file ~/.config/gitinbox/jev-key.txt
bin/gitinbox-cli settings set interests "CiviCRM extensions using APIv4, Drupal integration, Afform/SearchKit"
bin/gitinbox-cli judge                       # enrich + judge unread threads now
bin/gitinbox-cli explain <thread-id>         # see the answers and the score
```

`provider` defaults to `auto`: Jev when a key is stored, else Ollama when a judge model is set,
else off. Set `provider jev|ollama|off` explicitly to pin it. `interests` is free text that becomes
the `relevance` question's reference and is worth writing carefully.

### 6.2 Ollama

On the Ollama machine pull a model that fits **entirely in VRAM** (a model that spills to CPU
turns 6-second judgments into 5-minute ones):

```sh
ollama pull qwen3.5:9b            # good default for summaries, notes, digests and fallback judging
```

Then point GitInbox at it:

```sh
bin/gitinbox-cli settings set ollama.url http://10.0.0.136:11434
bin/gitinbox-cli settings set ollama.model qwen3.5:9b            # judge model (fallback judge; also the default for prose)
bin/gitinbox-cli settings set summary.model qwen3.5:9b           # optional overrides per task
bin/gitinbox-cli settings set impact.note-model qwen3.5:9b
bin/gitinbox-cli settings show
bin/gitinbox-cli summarize <thread-id>                           # smoke test: a summary in a few seconds
```

Or in the app: **Settings → Triage judge** (URL, *List models*, judge model) and **Settings →
Summaries, digest & notifications** / **PR impact analysis** for the per-task models.

The Ollama requests use structured output (`format` = JSON schema), `keep_alive: 30m` so the model
stays resident between threads, `temperature` 0 (judge) / 0.2 (prose), and `think: false` to skip
hidden reasoning on thinking models (GitInbox retries without the flag for models that reject it).
Ollama 0.9+ is assumed; the LAN box in use runs 0.34.

### 6.3 Impact profiles

The built-in **civicrm** profile (CiviCRM core, CMS integrations, packages, buildkit) and the
**generic** fallback are enabled out of the box. Check `bin/gitinbox-cli profiles list`, and import
your own YAML with `profiles import my-project.yaml` (schema in the user documentation). PR/MR
threads in profile repositories are analysed automatically after each sync (20 per run by default);
any PR can be analysed on demand.

### 6.4 Agent (pi sidecar)

The Agent view needs [pi](https://pi.dev) and an Ollama model that supports tool calling. Node
22.19 or newer is required by pi.

```sh
# either globally …
npm install -g @earendil-works/pi-coding-agent
pi --version                                     # 0.86.1 at the time of writing
# … or pinned inside the repository (what development uses)
cd agent && npm install && cd ..                  # → agent/node_modules/.bin/pi, found automatically

bin/gitinbox-cli agent status                    # where pi is, which model the agent will use
bin/gitinbox-cli settings set agent.model qwen3.5:9b   # optional; defaults to the summary/judge model
bin/gitinbox-cli agent chat "What needs me most right now? Top 3 with links."
```

Or in the app: **Settings → Agent** (enable, autostart, pi path, model, thinking). GitInbox writes
pi's configuration into `~/.local/share/gitinbox/agent/` (`models.json`, `settings.json`, the
extension, sessions) on every start and runs pi with `PI_CODING_AGENT_DIR` pointing there, so your
own `~/.pi` setup is never touched and pi runs with no built-in tools, offline, and without
telemetry. Tool results can be large: give the model a bigger context on the Ollama server
(`OLLAMA_CONTEXT_LENGTH=32768` in its environment) if answers get cut off.

### 6.5 Desktop notifications

Enabled by default for threads entering *Needs me* and for impact analyses at level *certain*
(`notify.impact-level 3`). **Settings → Summaries, digest & notifications → Test notification**
checks that D-Bus notifications reach your desktop.

---

## 7. GitLab watched projects

GitLab has no notifications feed; to-dos are the "needs me" signal and everything else comes from
polling the activity of projects you choose:

```sh
bin/gitinbox-cli watch add --account jackrabbithanna@lab.civicrm.org dev/core
bin/gitinbox-cli watch list
bin/gitinbox-cli sync
```

The project path is resolved once; each sync reads the project's events since the stored cursor
(up to 5 pages of 100). The same editor exists under **Settings → Accounts** for GitLab accounts.

---

## 8. Write modes (off by default)

Every account starts in `readonly`: the GitHub MCP server is started with `--read-only`, GitLab is
only ever sent `GET`s, and Read / Done / Snooze / Mute are local state. To mirror Done and Mute to
the forge:

```sh
bin/gitinbox-cli account write-mode <login|id|login@host> notifications
```

This restarts the account's MCP server without `--read-only` and admits exactly two tools through
the client allowlist (`dismiss_notification`, `manage_notification_subscription`); on GitLab it
enables `mark_as_done` and unsubscribe (needs an `api` token). Nothing that touches issues, PRs,
comments, labels or repositories is ever callable. Switch back with `… readonly`.

---

## 9. Running unattended

The desktop app runs its scheduler (sync every 3 min → judge → impact → prose) only while it is
open (it keeps running minimised to the tray). For a headless machine, schedule the CLI instead:

```ini
# ~/.config/systemd/user/gitinbox-sync.service
[Unit]
Description=GitInbox sync, judge, analyse

[Service]
Type=oneshot
ExecStart=%h/.local/bin/gitinbox-cli sync
ExecStart=%h/.local/bin/gitinbox-cli judge
ExecStart=%h/.local/bin/gitinbox-cli analyze --pending

# ~/.config/systemd/user/gitinbox-sync.timer
[Unit]
Description=Run GitInbox every 5 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
```

```sh
systemctl --user enable --now gitinbox-sync.timer
```

The CLI reads the same keyring; on a headless session make sure the keyring is unlocked (or accept
the file backend). Both the app and the CLI can run at the same time: they share the database
(SQLite with per-account "sync already running" guards).

---

## 10. Environment variables

| Variable | Effect |
|---|---|
| `GITINBOX_DB` | Path to the SQLite database (default `$XDG_DATA_HOME/gitinbox/gitinbox.db`). The log and extracted MCP binary live next to it. |
| `GITINBOX_MCP_PATH` | Use this `github-mcp-server` binary instead of the bundled one (resolution order: override → bundled → `PATH`). |
| `GITINBOX_DEBUG=1` | Debug-level logs and the MCP server's stderr. |
| `XDG_DATA_HOME`, `XDG_CONFIG_HOME` | Honoured for the data directory and the secrets fallback file. |

---

## 11. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `go install …/wails3` or `wails3 build` fails with `Package gtk4 was not found` / `webkitgtk-6.0` | The `-dev` packages are missing: `sudo apt install libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev`. Runtime libraries alone are not enough. |
| `wails3 dev`: "unable to connect to frontend server" | Vite takes longer than Wails' 5 s retry window on slow hosts. The project's `build/config.yml` inserts a `common:wait:frontend` step; if it still fails, run `wails3 dev` again once `node_modules` is warm. |
| `account list` shows `secrets backend: file:…` | No Secret Service keyring is reachable. Install/unlock `gnome-keyring` (or accept the `0600` file). Over SSH the keyring may be locked; tokens then fall back to the file only if they were stored there. |
| `github-mcp-server not found` in Diagnostics / log | The bundled binary could not be extracted (read-only data dir?) and none is on `PATH`. Set `GITINBOX_MCP_PATH` or fix permissions on `~/.local/share/gitinbox/bin`. |
| `mcp tools` lists no `list_notifications` | The token lacks the `notifications` scope. |
| GitLab account add fails with 404 on `/personal_access_tokens/self` | Instance older than 15.5; the account is still added with scopes "unknown". |
| Judge reports `no provider configured` | Store a Jev key (`jev-key`) or set `ollama.model`; check `settings show`. |
| Jev returns 401 / 422 | Bad key / malformed request. 401 aborts the run; 422 is a bug worth reporting with the field named in the message. 429/529 are retried with backoff automatically. |
| Ollama: `model "x" not found (pull it first)` | `ollama pull x` on the Ollama machine; check the name with **List models** in Settings. |
| Ollama judgments or summaries take minutes | The model does not fit in VRAM or is a thinking model without `think` support. Use a smaller model; GitInbox already sends `think: false`. |
| `sync already running` | A sync for that account is in progress (app + CLI at the same time). Wait for it to finish. |
| No tray icon on GNOME | GNOME needs a StatusNotifier/AppIndicator extension for tray icons; the window and notifications work without it. |
| Agent: `pi not found` | Install pi globally or `cd agent && npm install`; or set the path in Settings → Agent. `agent status` shows what the app would use. |
| Agent: `pi did not answer get_state` / exits immediately | Run `GITINBOX_DEBUG=1 bin/gitinbox-cli agent chat hi` — pi's stderr goes to the log (`gitinbox.log`); typical causes are an old Node (< 22.19) or an unreachable Ollama URL. |
| Agent answers are truncated or the model ignores tools | Use a tool-capable model (`qwen3.5:9b`, `gpt-oss:20b`, `gemma4:12b`) and raise the Ollama context length (`OLLAMA_CONTEXT_LENGTH=32768`). |
| Desktop notifications never appear | Run **Test notification** in Settings; make sure a notification daemon is on the session D-Bus. The CLI never sends notifications. |
| I want to start over | Quit the app, delete `~/.local/share/gitinbox/`, remove the keyring entries (`account remove` deletes each account's token; the Jev key is removed by `jev-key --token-file /dev/null`). |

Logs: `~/.local/share/gitinbox/gitinbox.log`; `GITINBOX_DEBUG=1` adds MCP server stderr.
