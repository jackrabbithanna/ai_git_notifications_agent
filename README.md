# GitInbox

Desktop dashboard that pulls GitHub and GitLab notifications for one or more accounts — GitHub
through the official GitHub MCP server, GitLab through the official REST client — triages them
with typed, calibrated AI judgments (TypeSafe Jev), writes summaries, impact notes and digests with
a self-hosted Ollama model, and surfaces what needs you: a prioritized inbox, "Mine" lists, and
pull-request impact analysis for the upstream projects you build on (CiviCRM first, repo-agnostic
by design). Read/Done/Snooze/Mute are local unless you explicitly enable mirroring to the forge.

| Document | Contents |
|---|---|
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | Prerequisites, building, tokens, first run, AI provider setup, unattended use, troubleshooting |
| [docs/USER-DOCUMENTATION.md](docs/USER-DOCUMENTATION.md) | Concepts, every view and setting, CLI reference, schedules and budgets, what leaves your machine |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Process model, packages, data flow, data model, forge seam, AI layer, impact engine, eval, security, build |
| [PLAN.md](PLAN.md) | Design decisions, milestone history with live measurements, roadmap |
| [docs/typesafeai.md](docs/typesafeai.md), [docs/typesafeapi.md](docs/typesafeapi.md) | Local copies of the TypeSafe build guide and API reference |

## What it does

- **Sync** notifications (GitHub) and to-dos + watched-project activity (GitLab) every 3 minutes;
  classify each thread deterministically (new PR/issue vs reply, review, CI, release, mention…;
  your relation: author, assignee, reviewer, mentioned) and drop noise by rule before any AI.
- **Mine**: issues and PRs/MRs where you are assigned, mentioned, review-requested or the author,
  built from searches so it is complete even for quiet threads.
- **Triage**: six typed questions per thread (category, needs me, urgency, relevance, resolved,
  next action) answered by Jev or, as an uncalibrated fallback, an Ollama model; a weighted score
  orders the inbox and re-ranks instantly when weights change; an explain panel shows probabilities
  and the state the judge saw.
- **PR impact**: repo-agnostic impact profiles (built-in CiviCRM + generic, editable YAML) map
  changed files to layers, detect signal keywords and public-surface changes, then judge change
  kind and downstream impact (none / possible / likely / certain) with backward-compatibility and
  "look now" flags; optional Ollama impact note; proposed vs landed views and a scan of recently
  merged PRs.
- **Prose**: thread summaries with "asks of me" and "changed since last read", a period digest,
  desktop notifications for threads entering *Needs me* and for high-impact PRs, usage/latency
  accounting.
- **Eval**: label threads and PRs, measure category accuracy, needs-action precision/recall +
  Brier, ordinal error, NDCG/Spearman ranking agreement and filter recall per provider, and tune
  the weights on your own labels.
- **Agent**: chat with the inbox and deep-dive threads through a [pi](https://pi.dev) sidecar
  whose only tools are GitInbox's own data and read-only forge access; drafts replies, never posts.
- **Headless CLI** sharing the same database and keyring — everything above works over SSH.

Milestones M0–M6 are complete (see PLAN.md §8 for live numbers).

## Stack

- [Wails v3](https://v3.wails.io) v3.0.0-beta.23 (Go backend, GTK4 + WebKitGTK 6.0 on Linux)
- React 18 + TypeScript + Tailwind v4, Vite (`frontend/`)
- [github-mcp-server](https://github.com/github/github-mcp-server) v1.12.2, pinned as a Go
  `tool` dependency and embedded into the app binary (`internal/mcpbin`); official
  [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)
- [GitLab client-go v3](https://gitlab.com/gitlab-org/api/client-go)
- SQLite via `modernc.org/sqlite` (CGO-free); OS keyring via `zalando/go-keyring`
- TypeSafe Jev (System One) for judgments; Ollama for generation
- [pi](https://pi.dev) (`@earendil-works/pi-coding-agent`, bring-your-own) as the agent sidecar

## Quick start

```sh
sudo apt install build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
wails3 build                                    # bin/gitinbox (≈3 min on a Raspberry Pi 5)
go build -o bin/gitinbox-cli ./cmd/gitinbox     # headless CLI
bin/gitinbox-cli account add --token-file ~/.config/gitinbox/pat.txt        # GitHub classic PAT, scope: notifications
bin/gitinbox-cli account add --forge gitlab --host lab.civicrm.org --token-file ~/.config/gitinbox/pat-lab.civicrm.org.txt
bin/gitinbox-cli jev-key --token-file ~/.config/gitinbox/jev-key.txt        # optional: calibrated judge
bin/gitinbox-cli settings set ollama.url http://gpu-box:11434 && bin/gitinbox-cli settings set ollama.model qwen3.5:9b
bin/gitinbox-cli sync --full && bin/gitinbox-cli judge && bin/gitinbox-cli inbox
./bin/gitinbox                                  # the desktop app
```

Go 1.26+ and Node 22+ are required. Full details, GHES/self-hosted GitLab, systemd timers and
troubleshooting: [docs/INSTALLATION.md](docs/INSTALLATION.md).

## Develop

```sh
wails3 dev            # hot-reloading app; also stages the MCP server binary
wails3 build          # production binary in bin/gitinbox (use this or `go build .`; `go build ./...` trips on the template's build/ios files)
go test ./...         # Go unit tests (no network, no tokens)
go run ./cmd/gitinbox mcp path   # headless check: which github-mcp-server would be used
```

The CLI and the desktop app share the same database (`$XDG_DATA_HOME/gitinbox/gitinbox.db`)
and keyring entries. `GITINBOX_DEBUG=1` shows server stderr and debug logs; `GITINBOX_DB` and
`GITINBOX_MCP_PATH` override the database path and the MCP server binary.

The app was called `ghinbox` until 2026-09-20. On first start the renamed binaries move
`$XDG_DATA_HOME/ghinbox/` to `$XDG_DATA_HOME/gitinbox/` (renaming `ghinbox.db`/`.log`) and copy
keyring entries from service `ghinbox` to `gitinbox` on first read; token files you keep under
`~/.config/` can stay wherever they are — the CLI only takes their path.

---

## How TypeSafe Jev is used

[TypeSafe](https://typesafe.ai) Jev ("System One") is the **calibrated judge**. It is not a chat
model: one request carries a JSON *state* and a map of typed *questions*, and the answer to each
question is a typed value with an honest probability. GitInbox uses it for exactly two things and
never for prose.

**Contract** (pinned locally in `docs/typesafeapi.md`): `POST https://api.typesafe.ai/v1/systemone`
with `{state, model: "jev-latest", questions{id: {type, instructions, criteria}}}`. Question types:
`noul` (a probability with `true`/`false` criteria), `choice` (a distribution over named options,
each with a description), `score` (an ordered list of level descriptions; the answer is a
fractional level index with a distribution). Question ids are never shown to the model, so every
option and level carries a standalone description — see `internal/judge/triage.go` and
`impact.go` for the full rubric text.

**Triage — `triage.v1`** (`internal/judge/triage.go`). After each sync, every unread, non-noise
thread without a judgment for its current version is *enriched* (item body, state, labels, author,
and the exact latest comment fetched through the forge) and sent as
`{thread, me{login, relation_tags, is_author}, latest_activity, profile{interests}}` with six
questions asked together:

| id | type | what it decides |
|---|---|---|
| `category` | choice | needs_my_review · needs_my_reply · blocking_or_failing · awaiting_others · fyi_progress · release_or_announcement · resolved_no_action |
| `requires_action_from_me` | noul | is something expected from *you* specifically |
| `urgency` | score 0–3 | no pressure · this week · today · blocking right now |
| `relevance` | score 0–2 | vs your *interests* text: unrelated · tangential · directly my area |
| `resolved` | noul | already settled, nothing left for you |
| `next_action` | choice | review · reply · rebase_or_fix · merge · read_only · nothing |

**PR impact — `impact.v1`** (`internal/judge/impact.go`). For a PR/MR in a profile repository
(or on demand), code first fetches the changed files, maps them to the profile's layers, scans
signal keywords and applies surface regexes to the diff; Jev then receives `{pr, layers_touched,
signals, surface_hits, changed_files (≤12, trimmed patches), downstream_description}` and answers
`change_kind` (bugfix … api_change, data_model_change, deprecation_or_removal …),
`affects_downstream`, `downstream_impact` (none / possible / likely / certain),
`backward_compatible` and `needs_my_attention_now`.

**How the answers are used.** They are stored per thread version / PR head commit in SQLite
(`judgments`, `pr_analysis`) with provider, model, token usage and latency, and consumed by pure
code: the scoring formula (`internal/scoring`) turns them into a 0–100 priority and a bucket
(*Needs me* / normal / *Resolved*), pins `blocking_or_failing` on your own items, badges a category
confidence below 0.5 as *unsure*, and forces *certain* impact into *Needs me*. Because the
probabilities are calibrated, thresholds mean what they say (`P(resolved) > 0.8` → Resolved) and
hedged answers show as such rather than being hidden. Weights are sliders; changing them re-ranks
from stored answers with zero new calls.

**Cost and safety controls.** Budget of 200 threads per run at concurrency 3 (both settings); one
request per thread (~1.4k input tokens measured); judgments never repeat for the same thread
version; 401 and 422 abort the run immediately (a 422 names the offending field); 429/529 back
off with jitter. The key is stored in the keyring (`jev-key --token-file`), the endpoint and model
are settings (`jev.url`, `jev.model`). What is sent is listed in USER-DOCUMENTATION §13. The Eval
view measures Jev against your own labels (category accuracy, needs-action precision/recall +
Brier, ordinal error, NDCG) and can re-judge the labeled set to compare with Ollama.

Measured: ~0.6 s per triage, 1.8 s per impact judgment, well-hedged distributions (PLAN.md §8, M2/M3).

---

## How Ollama LLMs are used

Ollama is the **generative** side and the **fallback judge**, always on a server you control
(`ollama.url`, default `http://localhost:11434`; the reference setup is a LAN GPU box). Every call
is `/api/chat` with a JSON-schema `format`, so outputs are structured and validated, never free
text pasted into the UI. `keep_alive: 30m` keeps the model resident across a run, and every
request sends `think: false` (with an automatic retry without it for models that reject the
field) — skipping hidden reasoning is what brought a summary from 53 s to 2.5 s and a fallback
judgment from ~2 min to 6 s on `qwen3.5:9b`.

**Thread summaries** (`internal/pipeline/prose.go`, schema `llm.ThreadSummary`): the same enriched
state the judge sees, plus the previous summary if one exists → `{summary (≤2 sentences),
key_points (≤4), asks_of_me, changed_since_last_read}`. Generated on demand (Details panel,
`summarize`) and automatically for the top-3 priority threads every 15 minutes; cached per thread
version so nothing is re-summarised without new activity. Model: `summary.model` → note model →
judge model.

**Impact notes** (`internal/pipeline/impact.go`, schema `llm.ImpactNote`): PR metadata, the layer
report, surface hits, top files, the profile's downstream description and Jev's answers →
`{what_changed, why_it_matters_for_downstream, surfaces_changed, recommended_checks,
migration_hints, confidence_note}`. On demand by default (`impact.note-level 4`); set the level
to 2 or 3 to write notes automatically for *likely*/*certain* PRs. Model: `impact.note-model` →
judge model.

**Digest** (schema `llm.Digest`): the period's top 40 scored threads (with summaries or trimmed
latest activity, category, bucket, relation, impact level) and impact analyses ≥ likely →
`{headline, sections[{title, items[{ref, title, why}]}], suggested_actions (≤5)}`. Daily by
default while the app runs, or `digest --generate --hours N`. Model: `digest.model` → summary
chain.

**Fallback judge** (`internal/judge/ollama`): the same `triage.v1` / `impact.v1` question sets
rendered as a system + user prompt with a generated JSON schema (`{probability}` for noul,
`{choice, probabilities}` for choice, `{probabilities per level}` for score), `temperature 0`. Used
when `provider` is `ollama`, or `auto` with no Jev key. Its probabilities are the model's own, so
the provider reports `Calibrated() = false`: scoring multiplies its judgment terms by
`uncalibratedDiscount` (0.7), the UI badges it, and the Eval view lets you measure whether a given
model is trustworthy on your labels. Small resident models (`qwen3.5:9b`) answer in seconds; a 27B
model that spills to CPU takes minutes per thread and is not recommended.

**Controls.** Model names live in Settings, not code (`ollama.model`, `summary.model`,
`digest.model`, `impact.note-model`; *List models* queries `/api/tags`). Budgets: top-N summaries
(`summary.top`, `summary.every`), digest cadence (`digest.hours`, `digest.auto`), notes level,
judge `max`/`concurrency`. All generations record tokens and latency (`usage`, Diagnostics →
Model usage). A 404 from Ollama means the model is not pulled; an unreachable server is logged once
per tick and skipped.

Measured with `qwen3.5:9b` and `think:false`: summary 2.5 s, 40-thread digest 13 s, impact note
seconds (69 s before the flag), triage judgment 6 s (PLAN.md §8, M4).

---

## How the pi agent is used

[pi](https://pi.dev) is the **open-ended** side: "chat with my inbox" and "deep-dive this thread".
Triage stays a fixed pipeline; the agent is for questions the pipeline cannot anticipate. It runs as
a **sidecar** — `pi --mode rpc`, JSONL over stdin/stdout, spawned and supervised by
`internal/agent` — because pi is TypeScript and embedding it would need a Node host.

**Bring your own pi.** GitInbox looks for the binary at the path set in Settings → Agent, then
`pi` on `PATH`, then the repository's `agent/node_modules/.bin/pi` (`cd agent && npm install`
pins `@earendil-works/pi-coding-agent` 0.86.1 for development; Node 22.19+). Nothing is bundled.

**Isolation.** Every start (re)writes an app-owned agent directory,
`$XDG_DATA_HOME/gitinbox/agent/`, and points pi at it with `PI_CODING_AGENT_DIR`: `models.json`
(provider `ollama` → `<ollama.url>/v1`, OpenAI-compatible, every model the Ollama server lists),
`settings.json` (project trust never, telemetry off, compaction on), sessions, and the embedded
extension. pi is started with `--no-builtin-tools --no-extensions -e gitinbox.ts --no-skills
--no-prompt-templates --no-themes --no-context-files --no-approve --offline`, so it has **no
shell, no file access and no tools but GitInbox's**, and never phones home for update checks.

**Tools = the app's own data.** The extension (`internal/agent/ext/gitinbox.ts`) turns a loopback
JSON API (`internal/localapi`, 127.0.0.1, random port, per-process bearer token) into pi tools:
`list_top_priority`, `search_threads`, `get_thread` (item, latest activity, judged answers with
probabilities, score, summary, impact, draft), `find_thread`, `get_thread_summary`,
`get_thread_comments` (live discussion: issue comments, PR reviews and review comments, GitLab
notes), `list_mine`, `list_impact`, `get_pr_analysis` (runs the analysis / note on request),
`get_pr_changes`, `mark_done` / `mark_read` / `undo_done` / `snooze` (local triage state, mirrored
only as the account's write mode already allows), `draft_reply` (stored in the `drafts` table and
shown in the inbox Details panel — never posted), and `github_read` (raw pass-through to the
account's GitHub MCP client restricted to the **read** allowlist regardless of write mode). The
agent therefore sees what the pipeline already computed instead of re-deriving it, and cannot reach
a forge write through any path.

**Model.** The same Ollama server as the prose features; `agent.model` → summary → note → judge
model precedence, switchable from the Agent view. The model must support tool calling; `thinking`
defaults to off. The system prompt (`internal/agent/prompt.md`) carries the account refs, your
interests text and the enabled impact profiles, and defines a "deep-dive" as get_thread →
get_thread_comments → (PRs) get_pr_analysis / get_pr_changes → what / asked of me / state / next
action.

**Surfaces.** The **Agent** view streams the transcript (text deltas, collapsible tool calls),
with model select, Abort, New session, Start/Stop and suggestion chips; every inbox row has a
**Deep-dive** button that pre-fills and sends the prompt; Settings → Agent (enable, autostart,
pi path, model, thinking) and Diagnostics → Agent (binary, version, agent dir, API port); the CLI
has `agent status | chat "…" | models` for headless use and `settings set agent.*`.

Measured with pi 0.86.1 and `qwen3.5:9b`: sidecar up in 1.8 s; "what needs me most" answered in
16 s with one tool call; a full deep-dive (thread + live comments + impact analysis) in 29 s;
a stored draft reply in 14 s (PLAN.md §8, M6).

---

## Headless CLI

```sh
go build -o bin/gitinbox-cli ./cmd/gitinbox
bin/gitinbox-cli account add --token-file ~/.config/gitinbox/pat.txt   # validates with get_me, stores in keyring
bin/gitinbox-cli mcp tools            # what the server registered (read-only by default) + our allowlist
bin/gitinbox-cli sync --full          # pull the last 7 days incl. read; later runs are incremental
bin/gitinbox-cli inbox                # grouped by activity kind; --all/--noise/--done widen it
bin/gitinbox-cli mine                 # assigned / mentioned / review-requested / authored
bin/gitinbox-cli done <thread-id>     # local state; mirrored to the forge only in write mode 'notifications'
bin/gitinbox-cli account add --forge gitlab --host lab.civicrm.org --token-file ~/.config/gitinbox/pat-lab.civicrm.org.txt
bin/gitinbox-cli watch add --account <login@host> dev/core   # GitLab: poll a project's activity each sync
bin/gitinbox-cli jev-key --token-file ~/.config/gitinbox/jev-key.txt   # TypeSafe Jev key → keyring
bin/gitinbox-cli settings set ollama.url http://gpu-box:11434 && bin/gitinbox-cli settings set ollama.model qwen3.5:9b
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

`--account` accepts an id, `LOGIN@HOST`, a host, or an unambiguous login. Full reference:
USER-DOCUMENTATION §11.

## Writes are off by default

Each account has a write mode. `readonly` (default) starts `github-mcp-server` with
`--read-only`, so no write tool even exists; mark read/done/snooze/mute are local state.
`notifications` restarts the server without that flag and allows exactly two tools —
`dismiss_notification` and `manage_notification_subscription` — through a client-side
allowlist; nothing touching issues, PRs, comments, labels or repos is ever callable.
With a classic token the server additionally registers only tools its scopes permit, so a
`notifications`-only token never exposes issue/PR writes at all. See PLAN.md §4.9 and
ARCHITECTURE §11.

GitLab accounts have no MCP server: reads use the official REST client with a `read_api` token;
in `notifications` mode the only writes are marking a to-do done and unsubscribing from an
issue/MR (needs an `api`-scoped token). "Read" is local-only on GitLab.

`wails3 build` runs `common:build:mcp`, which compiles `github-mcp-server` for the target
OS/arch into `internal/mcpbin/bin/` (git-ignored) so `//go:embed` bundles it. At runtime
`mcpbin.Locate` extracts it once to `$XDG_DATA_HOME/gitinbox/bin/` and prefers, in order:
an explicit override path → the bundled copy → `github-mcp-server` on `PATH`.

## Layout

```
main.go                  Wails application entry point (services, window, tray, scheduler)
cmd/gitinbox/            headless CLI (accounts, mcp tools/call, sync, inbox, mine, judge, analyze, prose, eval, agent)
internal/app/            wiring: db + secrets + MCP binary + pipeline; legacy data-dir migration
internal/ghmcp/          MCP client for github-mcp-server, per-write-mode tool allowlist
internal/mcpbin/         locate/extract the bundled GitHub MCP server
internal/store/          SQLite (pure Go) + embedded migrations 0001–0007
internal/secrets/        OS keyring with 0600-file fallback
internal/classify/       activity kind + relation tags from notification fields
internal/filter/         rule-based noise filter (bots, green CI, releases, mutes)
internal/source/         forge seam; source/github (ghmcp) and source/gitlab (client-go REST)
internal/judge/          typed judgments: triage.v1 + impact.v1 rubrics, jev (TypeSafe) and ollama providers
internal/scoring/        composite priority from stored judgments + user weights (+ impact term)
internal/profiles/       impact profiles (YAML, built-ins embedded), layer/signal/surface analysis
internal/llm/            schema-constrained generation (Ollama): impact notes, summaries, digests
internal/eval/           metrics (binary/ordinal/categorical/NDCG) and the weight tuner
internal/localapi/       loopback JSON API (bearer token) the agent's tools call
internal/agent/          pi sidecar: locate, agent dir, JSONL RPC client, transcript; embedded extension + prompt
internal/pipeline/       sync → classify → filter → store; mine; judge; impact; prose; eval; scheduler
internal/services/       Wails services: Accounts, Inbox, Mine, Diagnostics, Watches, Judge, Impact, Profiles, Prose, Eval, Agent
frontend/                React + TS + Tailwind (Vite); views Inbox, Mine, Impact, Digest, Agent, Eval, Profiles, Settings, Diagnostics
agent/                   package.json pinning pi for development (node_modules git-ignored)
docs/                    ARCHITECTURE, USER-DOCUMENTATION, INSTALLATION, TypeSafe reference copies
build/                   Wails build config, platform Taskfiles, nfpm packaging
.github/workflows/       Linux amd64 + arm64 CI builds
```
