# GitHub Notifications Triage Dashboard — Architecture & Plan

Wails v3 desktop app that pulls GitHub notifications for one or more accounts (via the official GitHub MCP server), uses TypeSafe Jev for typed judgments and a self-hosted Ollama for prose, and presents a prioritized, categorized dashboard with **Mine** lists (assigned / mentioned / review-requested / authored), **New vs Replies** grouping, and **PR impact analysis** for upstream projects you build on (CiviCRM first, repo-agnostic by design). A pi (pi.dev) agent is a phase-2 sidecar for chat-with-inbox and thread deep-dives.

Status: planning (2026-09-19). Nothing scaffolded yet — M0 is the first implementation step.

---

## 0. Decisions & constraints

| Topic | Decision |
|---|---|
| Shell | Wails v3 (beta since 2026-08-02; Go backend + services model). React + TypeScript + Tailwind frontend. Linux (incl. ARM64) only for v1; dev machine is a Raspberry Pi. mac/Windows later via CI matrix. |
| GitHub connection | **MCP only**: official `github/github-mcp-server` (Go) run locally over **stdio**, one subprocess per account; Go MCP client (`modelcontextprotocol/go-sdk`). Thin `Source` interface so a REST path (or a GitLab MCP source) can be added later. **Server runs `--read-only` by default; GitHub writes only with an explicit per-account setting (§4.9).** |
| Action scope (v1) | Read + triage: fetch, judge, summarize, open in browser. Mark read/done/snooze are **local state**; mirroring them to GitHub (mark read, dismiss, unsubscribe) requires the per-account write setting. Never writes to issues/PRs. |
| TypeSafe Jev | API key available. Used for typed judgments (triage + PR impact). |
| Ollama | Separate LAN GPU box (configurable base URL). Generative work (summaries, impact notes, digest) + fallback judge. |
| Volume | 300+ notifications/day, possibly multiple accounts → deterministic noise filtering before any AI, lazy summarization, aggressive caching. |
| PR impact analysis | Automatic only for repos covered by an impact profile; on-demand "Analyze" for any PR. |
| CiviCRM profile | Repos: civicrm-core, civicrm-packages, civicrm-drupal-8, civicrm-wordpress, civicrm-backdrop, civicrm-joomla, civicrm-buildkit. Layers: data & API (Api4/BAO/DAO/schema/sql), hooks & core services, UI (Form/templates/Angular/Afform/SearchKit), packaging (lower weight). |
| pi (pi.dev) | Wanted for "chat with my inbox" and "deep-dive a thread" → phase 2, as an RPC sidecar; not bundled in v1. |

Research facts relied on (verify at implementation; TypeSafe live docs were not fetched during planning):
- `github-mcp-server`: official, Go, MIT; toolsets incl. `notifications`, `issues`, `pull_requests`, `repos`; tools for listing/detailing/dismissing notifications, PR files/diff/reviews, issue & PR search; PAT via `GITHUB_PERSONAL_ACCESS_TOKEN` env; `GITHUB_HOST` for GHES; `--toolsets`, `--read-only` flags. Tool responses are text content containing JSON. Exact tool names/shapes → verify with `ghinbox mcp tools` in M1.
- Official Go MCP SDK supports stdio, streamable-HTTP, and in-memory client transports.
- Jev: HTTP contract pinned locally in `typesafeapi.md` (mirror of `docs.typesafe.ai/api.md`) — see §4.8. Python & JS SDKs only → small Go HTTP client. Still read `confidence.md` and `patterns/composite-scoring.md` before M2.
- Ollama: JSON-schema-constrained output via `format`; official Go client `github.com/ollama/ollama/api`.
- pi: headless RPC mode (`pi --mode rpc`, JSONL over stdio) intended for embedding; Ollama via OpenAI-compatible `models.json` provider; TS extensions add tools; can attach MCP servers.

---

## 1. Recommendation summary

1. **Wails v3 + Go backend.** Go owns polling, storage, and the AI pipeline; the webview renders. Single Linux binary now; mac/Windows via CI later.
2. **GitHub through MCP, deliberately.** The app is an MCP *client* of the official GitHub MCP server. Benefits: one integration surface shared by the deterministic pipeline and by agents (pi in phase 2 attaches the same server); GHES support for free; and adding a second forge later (e.g. a GitLab MCP server — CiviCRM issues live on lab.civicrm.org) is a new `Source`, not a new API client. Costs, accepted: no ETag/conditional polling (use `since` + modest intervals), responses are JSON-in-text parsed into typed structs, and rate-limit headers aren't visible (we count calls ourselves). The server is started `--read-only` unless a per-account setting explicitly enables notification writes (§4.9).
3. **Own the workflow in Go; use Jev only for semantic judgments.** Everything derivable from data — activity kind (new item vs reply), "mine" lists, layers touched by a PR, bot/CI noise — is computed in code. Jev answers what code can't: does this need me, how urgent, what kind of change is this PR, will it affect downstream extensions. Judgments are typed, cached per thread version, and re-weightable without re-inference.
4. **Ollama for prose**: thread summaries, "what changed since I looked", PR impact notes for extension developers, daily digest — lazily, with JSON-schema outputs. Also a flagged, uncalibrated fallback judge.
5. **Repo-agnostic impact engine + shipped CiviCRM profile.** A profile maps paths→layers, lists signal keywords, and carries a free-text "what I build on this project" description that becomes Jev state. Users add profiles for any project.
6. **pi in phase 2, not v1.** Triage is a fixed workflow; pi earns its place for open-ended chat/deep-dive. It gets our local JSON API tools + the same GitHub MCP server.

## 2. Architecture

```
┌────────────────────────────── Wails v3 app (single Go binary) ──────────────────────────────┐
│  React/TS/Tailwind webview  ◄── generated bindings + events ──►  Wails services (Go)         │
│   Inbox · Mine · Impact · Thread · Digest · Profiles · Settings · Diagnostics                 │
│                                                                    │                          │
│                                                     ┌──────────────▼──────────────┐           │
│                                                     │ pipeline: sync → classify → │           │
│                                                     │ filter → enrich → judge →   │           │
│                                                     │ analyze(PR) → score → prose │           │
│                                                     └─┬───────┬───────┬───────┬───┘           │
│  ┌─────────┐ ┌──────────┐ ┌──────────────┐ ┌────────▼──┐ ┌──▼────┐ ┌▼─────┐ ┌▼────────────┐ │
│  │ secrets │ │ localapi │ │ store SQLite │ │ ghmcp     │ │profiles│ │judge │ │ llm         │ │
│  │ keyring │ │ loopback │ │ (modernc)    │ │ MCP client│ │+analysis│ │ jev  │ │ ollama      │ │
│  └─────────┘ └──────────┘ └──────────────┘ └─────┬─────┘ └───────┘ │ollama│ └─────────────┘ │
│                                                   │ stdio JSON-RPC  └──────┘                  │
│                                     ┌─────────────▼──────────────┐  (one per account, PAT     │
│                                     │ github-mcp-server (bundled)│   via env)                 │
│                                     └────────────────────────────┘                            │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
   phase 2: agent service spawns `pi --mode rpc`; pi's tools = localapi (inbox) + the same MCP server
```

### Go packages (`internal/`)

| Package | Responsibility | Key libs |
|---|---|---|
| `ghmcp` | Manages one `github-mcp-server` subprocess per account (stdio), MCP client session, typed wrappers over the tools we use (`list_notifications`, `get_notification_details`, `dismiss_notification`, `manage_notification_subscription`, `get_issue`, `get_issue_comments`, `get_pull_request`, `get_pull_request_files`, `get_pull_request_diff`, `get_pull_request_reviews`, `search_issues`, `search_pull_requests`, `list_pull_requests`), JSON-in-text parsing, call counters, retry/backoff, restart on crash. Spawns the server with `--read-only` unless `accounts.write_mode` says otherwise, and enforces a client-side tool allowlist derived from that mode (§4.9). Implements `Source`. | `modelcontextprotocol/go-sdk` |
| `mcpbin` | Locates/provisions the server binary: bundled (built by Taskfile via Go 1.24 `tool` directive, `go:embed`ded, extracted to the app data dir) or user-supplied path (Settings). | — |
| `store` | SQLite schema + embedded migrations; inbox/mine/impact queries; judgment/summary caches. | `modernc.org/sqlite` (CGO-free) |
| `classify` | Deterministic activity kind + relationship tags from notification/thread data (§4.1). | — |
| `filter` | Rule-based noise removal before any AI (bots, green CI, mutes, watch-only releases). User-editable rules. | — |
| `profiles` | Impact profile schema, loader (built-in `civicrm.yaml` + user dir), path→layer matching, signal keyword scan, surface heuristics (regex set per profile). | `doublestar` globs |
| `analysis` | PR impact step: fetch files/diff (capped), compute layer report, build Jev state, run `impact.v1` questions, request Ollama impact note. | — |
| `judge` | `Judge` interface + versioned question sets (`triage.v1`, `impact.v1`). Impl `jev` (HTTP, calibrated) and `ollama` (structured output, `Calibrated=false`). | `net/http`, `ollama/api` |
| `llm` | Text generation with JSON-schema output; impl `ollama` (optional cloud provider later). | `ollama/api` |
| `pipeline` | Orchestration + scheduler (notifications every 3 min default, adaptive; "mine" searches every 10 min); emits Wails events (`inbox:updated`, `mine:updated`, `impact:updated`, `pipeline:progress`, `pipeline:error`). | — |
| `scoring` | Composite priority from stored judgments + weights; instant re-rank. | — |
| `secrets` | PATs/keys in OS keyring; 0600-file fallback with warning (Pi may lack Secret Service). | `zalando/go-keyring` |
| `localapi` | Loopback HTTP JSON API (random port, session token) over inbox/mine/impact/thread/actions; used by `cmd/ghinbox` and phase-2 pi tools. | `net/http` |
| `services` | Wails services: `InboxService`, `MineService`, `ImpactService`, `ProfilesService`, `SettingsService`, `DiagnosticsService`, (phase 2) `AgentService`. | Wails v3 |

### Frontend views (React + TS + Tailwind; TanStack Query over bindings; Wails `Events.On` for live updates)
- **Inbox** — grouped by category: *New PRs* · *New issues* · *Replies & activity* · *Reviews & state changes* · *CI* · *Releases* · *Noise* (collapsed); each group sorted by priority; account chips; filters by repo/profile/kind.
- **Mine** — issues/PRs where the account is **assigned**, **mentioned**, **review-requested**, or **author** (open by default, toggle closed), per account, with notification state merged in. Built from search, not notifications, so it's complete even for quiet threads.
- **Impact** — PRs (open and recently merged) in profile repos with `downstream_impact ≥ likely`, grouped by profile and layer; "landed" vs "proposed" tabs; each row shows layers touched, change kind, and the impact note.
- **Thread** — summary, asks-of-me, activity timeline, judgment explain panel (probabilities, provider, version), raw payload; "Analyze PR" button.
- **Digest** — daily / since-last-open.
- **Profiles** — editor for impact profiles (repos, layers/globs, keywords, downstream description).
- **Settings** — accounts (PAT, host, **GitHub write mode** — default *read-only*, with a confirmation dialog listing the exact tools enabled), MCP binary path, Jev key, Ollama URL/models, weights sliders, mute rules, poll intervals.
- **Diagnostics** — connection tests (MCP server handshake + tool list, Jev, Ollama), per-account mode (read-only / writes: which tools) and the write tools the server actually exposed, MCP call counters per account, pipeline log, latency/cost counters.
- Tray icon with count of "needs me now"; desktop notifications for that bucket and for new `certain`-impact PRs (Wails v3 `notifications`/`badge` services — verify names).

## 3. Data model (SQLite)

- `accounts(id, login, host, write_mode[readonly|notifications] DEFAULT 'readonly', created_at)` — PAT in keyring `ghinbox/<id>`.
- `threads(account_id, thread_id, repo, subject_type, subject_url, subject_number, title, reason, unread, updated_at, last_read_at, latest_comment_url, activity_kind, relation_tags, enriched_json, filter_verdict, snoozed_until, done_at, PK(account_id, thread_id))`
- `items(account_id, repo, number, kind[issue|pr], title, state, author, assignees, labels, updated_at, relations[assigned|mentioned|review_requested|author], last_seen_in_search, PK(account_id, repo, number))` — the **Mine** set.
- `pr_analysis(account_id, repo, number, head_sha, profile_id, layers_json, files_json, signals_json, questions_version, provider, calibrated, answers_json, impact_note_json, created_at, PK(account_id, repo, number, head_sha, questions_version))`
- `judgments(account_id, thread_id, thread_version, questions_version, provider, calibrated, answers_json, usage_json, latency_ms, created_at)` — `thread_version = hash(updated_at, latest_comment_url, state)`; never apply a stale judgment.
- `summaries(account_id, thread_id, thread_version, kind[thread|delta|digest], model, content_json, created_at)`
- `profiles(id, source[builtin|user], yaml, enabled)` · `settings(key, value_json)` · `sync_state(account_id, last_since, poll_interval, last_sync_at, last_error, call_count_hour)`

## 4. Classification & AI design

### 4.1 Deterministic classification (code, `classify`)

| Output | Derivation |
|---|---|
| `activity_kind` ∈ `new_issue`, `new_pr`, `comment`, `review`, `review_requested`, `state_change`, `ci`, `release`, `mention`, `assignment`, `security`, `discussion`, `commit` | `reason` (`assign`, `mention`, `review_requested`, `ci_activity`, `state_change`, `security_alert`…) + `subject.type` + **"no new comment" test**: `latest_comment_url` empty or equal to `subject.url` ⇒ the newest event is not a comment (item created *or* PR pushed — M1 finding: on a real inbox most `new_pr` rows are pushes to existing PRs; distinguishing needs the item's `created_at` from `pull_request_read get`, added with M3 enrichment); otherwise `comment` vs `review` from the latest comment URL shape. |
| `relation_tags` ⊆ {`author`, `assignee`, `reviewer`, `mentioned`, `subscriber`} | `reason` + item fields (assignees, requested reviewers, author) + Mine search results. |
| `in_profile_repo`, `profile_id` | repo ∈ any enabled profile. |

### 4.2 Mine lists (search, code)
Per account, every 10 min (search API is 30 req/min): `search_issues` / `search_pull_requests` with `assignee:@me`, `mentions:@me`, `review-requested:@me`, `author:@me` (+ `state:open` unless the closed toggle is on). Upsert into `items` with relation tags; join to `threads` for unread/priority.

### 4.3 Triage judgments — Jev `triage.v1`
One request per surviving thread; all questions run in parallel over the same state.

State: `thread{title, repo, subject_type, activity_kind, reason, state, is_draft, ci_status, author, labels, updated_at}`, `me{login, relation_tags}`, `latest_activity{author, body, created_at}`, `recent_activity[]` (≤5 trimmed), `profile{interests, downstream_description}`.

| ID | Primitive | Judgment |
|---|---|---|
| `category` | Choice | `needs_my_review` · `needs_my_reply` · `blocking_or_failing` · `awaiting_others` · `fyi_progress` · `release_or_announcement` · `resolved_no_action` |
| `requires_action_from_me` | Noul | Latest activity asks/expects something from `me`? |
| `urgency` | Score | `no time pressure` / `this week` / `today` / `blocking someone right now` |
| `relevance` | Score | vs. `profile.interests`: `unrelated` / `tangential` / `directly my area` |
| `resolved` | Noul | Already resolved by others since my last read? |
| `next_action` | Choice | `review` · `reply` · `rebase_or_fix` · `merge` · `read_only` · `nothing` |

### 4.4 PR impact analysis — code + Jev `impact.v1`
Only for `in_profile_repo` PRs, or on demand.

Code first: `get_pull_request` (meta, labels, base, mergeable, merged), `get_pull_request_files` (paths, +/−, patch snippets), `get_pull_request_diff` capped (e.g. 64 KB, prioritising files in high-weight layers). Compute `layers_touched[{layer, files, weight}]`, `signals` (title/body/label keywords from the profile: e.g. `BREAKING`, `deprecat`, `remove`, `schema`, `upgrade`, `hook_`), and `surface_hits` from profile regexes (e.g. changed `public function` signatures, new/removed APIv4 actions/fields, DAO field additions, `hook_civicrm_*` additions, schema XML changes).

State to Jev: `pr{title, body (trimmed), labels, base, merged}`, `layers_touched`, `signals`, `surface_hits`, `changed_files[]` (top N with trimmed patches), `downstream_description` (from the profile).

| ID | Primitive | Judgment |
|---|---|---|
| `change_kind` | Choice | `bugfix` · `refactor_internal` · `new_feature` · `architectural` · `api_change` · `data_model_change` · `deprecation_or_removal` · `dependency_or_packaging` · `docs_tests_only` |
| `affects_downstream` | Noul | Would code built on this project (per `downstream_description`) need changes or re-testing? |
| `downstream_impact` | Score | `none` / `possible (new optional capability)` / `likely (behavior change in a used surface)` / `certain (signature, schema or removal)` |
| `backward_compatible` | Noul | Existing callers keep working unchanged? |
| `needs_my_attention_now` | Noul | Given `downstream_description`, should the user look at this before it lands / now that it landed? |

Ollama impact note (structured): `{what_changed, why_it_matters_for_downstream, surfaces_changed[], recommended_checks[], migration_hints, confidence_note}` — generated for `downstream_impact ≥ likely` automatically, on demand otherwise.

### 4.5 Impact profile schema (repo-agnostic)

```yaml
id: civicrm
name: CiviCRM
repos: [civicrm/civicrm-core, civicrm/civicrm-packages, civicrm/civicrm-drupal-8,
        civicrm/civicrm-wordpress, civicrm/civicrm-backdrop, civicrm/civicrm-joomla, civicrm/civicrm-buildkit]
downstream_description: >
  I develop CiviCRM extensions that use APIv4, BAO/DAO entities and schema, core hooks and
  services, QuickForm/Smarty forms, Angular/Afform and SearchKit UI. Flag changes that alter
  signatures, fields, hooks, schema, or UI extension points, and packaging changes that affect builds.
layers:
  - {id: data_api,  label: "Data & API",       weight: 1.0, paths: ["Civi/Api4/**", "CRM/**/BAO/**", "CRM/**/DAO/**", "xml/schema/**", "sql/**", "mixin/**"]}
  - {id: hooks_core,label: "Hooks & core",     weight: 1.0, paths: ["CRM/Utils/Hook.php", "Civi/Core/**", "Civi/**/Event/**", "settings/**"]}
  - {id: ui,        label: "UI",               weight: 0.8, paths: ["CRM/**/Form/**", "templates/**", "ang/**", "ext/afform/**", "ext/search_kit/**"]}
  - {id: packaging, label: "Packaging & build",weight: 0.4, paths: ["composer.json", "composer.lock", "packages/**", "distmaker/**", "tools/**", ".github/**", "bin/**"]}
ignore_paths: ["tests/**", "**/*.md"]
signals: ["BREAKING", "deprecat", "remove", "rename", "schema", "upgrade", "hook_civicrm", "APIv4", "signature"]
surface_patterns:            # regexes applied to added/removed diff lines
  - {id: php_public_sig, pattern: "^[+-]\\s*public (static )?function \\w+\\("}
  - {id: hook_def,       pattern: "^[+-].*function \\w*hook_civicrm_\\w+|^[+-].*public static function \\w+\\(&?\\$"}
  - {id: schema_xml,     pattern: "^[+-]\\s*<(field|table|name|type|index)>"}
```
A generic default profile (no repos; layers `api`, `core`, `ui`, `build`) documents the format; users add profiles in the Profiles view.

### 4.6 Scoring (code; re-runnable without inference)

```
priority = w_action·P(requires_action) + w_urgency·urgency + w_relevance·relevance
         + w_kind[activity_kind] + w_relation[relation_tags] + w_recency·decay(age)
         + w_impact·downstream_impact (PRs with analysis) − w_resolved·P(resolved)
hard rules: P(resolved) > 0.8 → Resolved bucket; blocking_or_failing ∧ author → pin;
            downstream_impact = certain ∧ merged → Impact "landed" alert;
            low judgment confidence → "unsure" badge, never hidden.
```
`urgency`/`relevance`/`downstream_impact` are Score answers normalised to 0..1 (§4.8). Weights live in `settings` and are sliders in the UI; changing them re-ranks instantly from stored judgments. Thresholds are tuned in M5 against labeled data, not guessed.

### 4.7 Ollama usage & cost controls
- Summaries lazily: on thread open, top-N (default 25) after each sync, digest daily. Impact notes only for `≥ likely`. Concurrency limit (2); requests cancellable.
- Fallback judge produces the same `answers` shape with `calibrated=false`; UI badges it; weights are not shared between calibrated and uncalibrated rankings without re-tuning.
- Judgments cached by `(thread_version | head_sha, questions_version, provider)`; re-judge only on new activity.
- Per-sync Jev budget (default 200 threads + 50 analyses), overflow queued by recency. MCP call budget per account/hour tracked in `sync_state`; polling backs off when near the budget.
- Starting models (LAN GPU box): 8B–14B instruct for summaries/impact notes, ~4B for the fallback judge; model IDs live in Settings, not code.

### 4.8 Jev API contract (pinned from `typesafeapi.md`)

One request = one `state` + a map of typed questions; answers come back under the same ids. Ids are not sent to the model, so each question's `instructions`/`criteria` must carry its full meaning.

```
POST https://api.typesafe.ai/v1/systemone      Authorization: Bearer <key>
{ "state": string|object|array, "model": "jev-latest", "questions": { "<id>": Question } }

Question (by "type"):
  noul   { instructions, criteria?: { "true": string, "false": string } }
  choice { instructions, criteria: { "<option>": string|null, ... } }          # required
  score  { instructions, criteria: [ "<level 0>", "<level 1>", ... ] }         # ordered, ≥ 2 levels

Response: { "model", "answers": { "<id>": Answer }, "usage": { input_tokens, output_tokens } }
  noul   { type, noul: 0..1 }
  choice { type, choice, probabilities: { option: p }, confidence: 0..1 }
  score  { type, score: weighted level index (0..len-1, fractional), legend: { "0": "...", ... },
           probabilities: { "0": p, ... }, confidence: 0..1 }
Errors: 401 (key), 422 (validation; body names the field), 429 / 529 → exponential backoff.
```

Implications for the design:
- `judge.Question{ID, Kind, Instructions, Criteria}` and `judge.Answer` map 1:1 onto this; the Ollama fallback must emit the same answer shapes (a `noul` float; `choice` + a probability map; `score` + level probabilities) so `scoring` is provider-agnostic.
- **Score normalisation:** `score` is a level index, so `scoring` uses `score / (len(levels)-1)` to get 0..1 (e.g. `urgency` with 4 levels: 2.4 → 0.8). Store the raw answer, normalise at scoring time.
- `criteria` for every Choice option and Score level must be a concrete, standalone description (the tables in §4.3/§4.4 are the option names; write the rubric text in `questions.go`, versioned as `triage.v1` / `impact.v1`).
- `usage.input_tokens/output_tokens` are recorded per call in `judgments.usage_json` and surfaced in Diagnostics as the Jev cost counter.
- Client: `net/http` + `encoding/json`, timeout ~30 s, retry with jittered exponential backoff on 429/529 only, never on 422 (log the offending field). 422s in tests catch malformed question sets before they reach production.

Sample `triage.v1` question as sent (one of six in the same request):
```json
"urgency": {
  "type": "score",
  "instructions": "How time-sensitive is the latest activity in `thread` for `me`, given `me.relation_tags`?",
  "criteria": [
    "No time pressure: informational, or nothing is waiting on me",
    "This week: someone expects a response or action from me within days",
    "Today: a reviewer, release, or teammate is waiting on me now",
    "Blocking right now: CI is red on my PR, a merge/release is held on my action, or I am explicitly pinged as blocking"
  ]
}
```

### 4.9 Read-only by default (write gating)

The GitHub connection can never write unless a setting is explicitly turned on, and even then only a named, narrow set of tools. Three independent layers enforce this:

1. **Server flag.** `ghmcp` starts every `github-mcp-server` with `--read-only` unless the account's `write_mode` is not `readonly`. The server then does not even register write tools, so a bug in our code cannot call one. Flipping the setting restarts that account's subprocess; Diagnostics shows the resulting `tools/list`.
2. **Client allowlist.** `ghmcp` only invokes tools on a per-mode allowlist, regardless of what the server exposes:
   - `readonly` (default): `list_notifications`, `get_notification_details`, `get_issue`, `get_issue_comments`, `get_pull_request`, `get_pull_request_files`, `get_pull_request_diff`, `get_pull_request_reviews`, `search_issues`, `search_pull_requests`, `list_pull_requests`.
   - `notifications`: the above plus `dismiss_notification` (mark read / done) and `manage_notification_subscription` (unsubscribe/ignore a thread). Nothing that touches issues, PRs, comments, labels, or repos. Future levels (e.g. reactions/labels) are new enum values with their own allowlist, never a blanket "writes on".
   A call outside the allowlist is rejected in code with a logged error, never forwarded.
3. **Token permissions.** Setup guidance recommends the narrowest token: a classic PAT with only the `notifications` scope (public-repo reads need no scope; `repo` only for private repos). A 403 from the server is surfaced in Diagnostics, not retried.
4. **Server scope filtering (classic tokens only).** github-mcp-server reads the token's `X-OAuth-Scopes` header and registers only tools the scopes permit, so with a `notifications`-only token the issue/PR write tools never exist even without `--read-only` (verified in M1: 25 tools read-only, 29 with writes enabled). Fine-grained PATs don't send that header, so this layer is absent for them.

Semantics under the default (`readonly`):
- Mark read, done, snooze, and mute are **local state** in `threads` (`last_read_at`, `done_at`, `snoozed_until`) and the UI treats local state as authoritative: a thread GitHub still reports as unread stays hidden while `done_at`/`snoozed_until` covers it, and re-surfaces only when `updated_at` moves past `done_at` (new activity). Unsubscribe is offered as a local mute rule.
- Enabling `notifications` mode adds "mirror to GitHub" behaviour for those same actions (and a one-time "sync existing local done/read to GitHub?" prompt); local state remains the source of truth for display so the app behaves identically if the write later fails.
- `cmd/ghinbox` honours the same per-account setting; there is no CLI flag that bypasses it.
- `localapi` exposes GitHub-mirroring actions only when the account's mode allows; local-state actions are always available.
- Phase-2 pi attaches the MCP server **always** with `--read-only`, independent of the app's setting; its write-like tools (`mark_done`, `snooze`) are local-state calls through `localapi`.

## 5. Multi-account & volume
- One `github-mcp-server` subprocess per account (token is per process); supervised (restart on exit, health via `tools/list`).
- Unified Inbox/Mine/Impact with account chips; per-account mute rules inherit global.
- Funnel at 300+/day: `filter` rules remove bots/green CI/watch-only releases before any AI; measure the ratio in M5 and tune rules first — it's the free lever.
- Snooze/done/read are local state (mirrored to GitHub only under `write_mode=notifications`, §4.9), so the inbox works even if the AI providers are down or the token is read-only.

## 6. pi integration (phase 2)
- Go `AgentService` spawns `pi --mode rpc` (JSONL over stdio), forwards events to the UI; BYO pi first (PATH / `~/.pi`), bundling a runtime only if needed.
- Isolation: app-owned agent dir (`PI_CODING_AGENT_DIR` or equivalent — verify) with `models.json` (Ollama via OpenAI-compatible endpoint), the extension, and a system prompt; built-in coding tools disabled via pi's tool-restriction flag (verify name).
- Extension `pi-gh-inbox` (TS): tools `search_threads`, `get_thread`, `get_thread_summary`, `list_top_priority`, `list_mine`, `get_pr_analysis`, `mark_done`, `snooze` → thin calls to `localapi`; plus pi attaches the **same** `github-mcp-server` binary (always `--read-only`, independent of the app's write setting) for deep-dives (diffs, comments, history) and `draft_reply` (draft only, never posts).
- Why sidecar: pi is TypeScript; in-process embedding would require a Node host (Electron). RPC mode exists for this shape.

## 7. Project layout
```
ai_github_notifications_agent/
  PLAN.md  README.md  Taskfile.yml  go.mod (tool github.com/github/github-mcp-server/cmd/github-mcp-server)  main.go
  cmd/ghinbox/              # headless CLI: mcp tools|call, sync, mine, analyze <repo#pr>, judge, summarize, eval
  internal/{ghmcp,mcpbin,store,classify,filter,profiles,analysis,judge,llm,pipeline,scoring,secrets,localapi,services}/
  internal/judge/{jev,ollama}/  internal/llm/ollama/  internal/store/migrations/*.sql
  profiles/builtin/{generic.yaml,civicrm.yaml}
  frontend/                 # Wails v3 react-ts + Tailwind
  eval/                     # labeled threads/PRs (jsonl) + eval reports
  agent/                    # phase 2: pi extension, models.json, system prompt
  build/                    # Wails platform config; bin/ for the built github-mcp-server; CI workflows
```

## 8. Milestones
- **M0 – Scaffold — done 2026-09-19.** Wails v3.0.0-beta.23 `react` template (module `ghinbox`), Tailwind v4 via `@tailwindcss/vite`; `github-mcp-server` v1.12.2 pinned as a Go `tool` dependency, built by `common:build:mcp` (CGO off, target GOOS/GOARCH) into `internal/mcpbin/bin/` and `//go:embed`ded; `mcpbin.Locate` (override → bundled, extracted to `$XDG_DATA_HOME/ghinbox/bin` → PATH) surfaced through `DiagnosticsService` and `ghinbox mcp path`; `.github/workflows/build.yml` builds on `ubuntu-24.04` + `ubuntu-24.04-arm`. Verified on the Pi: `wails3 build`, `go test`, app launches. Note: beta.23 links **GTK4 + WebKitGTK 6.0** by default (`libgtk-4-dev libwebkitgtk-6.0-dev libx11-dev`); GTK3/WebKit2GTK-4.1 is available behind the `gtk3` build tag.
- **M1 – MCP inbox, no AI — done 2026-09-19.** `ghmcp` client (official Go MCP SDK, stdio, one server per account, restart-and-retry, per-mode allowlist) with typed wrappers; `store` (SQLite, re-surface rule in SQL), `secrets` (gnome-keyring in use), `classify`, `filter`, `pipeline` (full/incremental sync, Mine searches, local actions + mirroring, scheduler), Wails services + Inbox/Mine/Settings/Diagnostics views, tray badge, `ghinbox` CLI. Live on the user's account (classic PAT, `notifications` scope only): full sync 219 threads in 10 s, incremental 0.7 s, Mine 20 items; server exposes 25 read-only tools by default, 29 in `notifications` mode with exactly 2 admitted by the allowlist. Tool-name facts: reads are `issue_read` / `pull_request_read` (+`method`), search results are go-github JSON trimmed by `fields`, notification tools as expected.
- **M1.5 (fold into M3 enrichment):** distinguish brand-new PRs from pushes (`pull_request_read get` → `created_at`), and re-evaluate filter rules on existing threads when rules change.
- **M2 – Triage judgments:** `judge` interface, `jev` client (contract in §4.8 / `typesafeapi.md`), `ollama` fallback, `triage.v1`, scoring + weight sliders, judgment explain panel.
- **M3 – PR impact engine:** `profiles` (built-in CiviCRM + generic, editor UI), `analysis` (files/diff, layers, signals, surface regexes), `impact.v1`, Ollama impact notes, **Impact** view (proposed / landed), desktop alerts for `certain` impact. Optional: periodic scan of merged PRs in profile repos (`list_pull_requests`) so landed changes are tracked even without a notification.
- **M4 – Prose & digest:** thread/delta summaries (lazy policy), daily digest, notifications for "needs me now", cost/latency counters in Diagnostics.
- **M5 – Evaluate & tune:** label ~150 threads + ~50 CiviCRM PRs via `ghinbox eval label`; report category accuracy, needs-action precision/recall, impact-level agreement, rank agreement (NDCG) for Jev vs Ollama-judge; tune filter rules, weights, thresholds from data.
- **M6 – pi sidecar (phase 2):** `AgentService`, `pi-gh-inbox` extension, chat panel + deep-dive drawer.
- **Later:** macOS/Windows CI, OAuth device flow, GitLab source via a GitLab MCP server (CiviCRM issues), low-risk writes (reactions/labels), optional cloud LLM.

## 9. Risks / verify before coding each area
- `github-mcp-server` (v1.12.2 bundled): tool names/args/shapes and the read-only classification of the four notification write tools are **verified (M1)**; still open: `pull_request_read get_diff` size limits and `content-window-size` behaviour for the M3 impact step. Go `tool` directive build across arm64.
- Search rate limit (30/min) with many accounts → stagger Mine refreshes.
- Jev: per-request question/state size limits, rate limits (429/529 backoff), pricing; impact state size (diff excerpts) vs. `usage` tokens — measure in M2/M3.
- Wails v3 beta: service/event API names, `notifications`/`badge` services (ARM64 GTK4/WebKitGTK build verified in M0).
- pi: tool-restriction flag, custom agent dir env, RPC schema version.
- Keyring availability on the Pi (fallback path must work).

## 10. Verification
- `go test ./...`: `ghmcp` against recorded MCP fixtures (fake server over in-memory transport); `classify`/`filter`/`profiles` table tests (activity-kind derivation, glob→layer, surface regexes on real CiviCRM diffs); `judge` fake provider + Ollama schema round-trip; `scoring` monotonicity tests.
- `ghinbox mcp tools` lists the server's tools; `ghinbox sync --account X` prints the funnel (fetched → filtered → judged → analyzed → summarized) with timings and call counts; `ghinbox analyze civicrm/civicrm-core#<n>` prints layers, signals, judgments, impact note.
- `wails3 dev`: add an account → Inbox groups populate; Mine shows assigned/mentioned/review-requested items; open a CiviCRM PR → Analyze → Impact view row appears; move a weight slider → instant re-rank with Jev call counter unchanged.
- Write gating: with default settings, `ghinbox mcp tools` shows no write tools and `ghmcp` rejects a `dismiss_notification` call in a unit test; switching an account to `notifications` restarts its subprocess, exposes exactly the two allowed write tools, and a call to any other write tool is still rejected client-side.
- M5 eval report committed under `eval/`, tuned weights recorded here.

## References
- Wails v3 beta: https://v3.wails.io/blog/wails-v3-beta/ · roadmap: https://v3.wails.io/status/
- GitHub MCP server: https://github.com/github/github-mcp-server · Go MCP SDK: https://github.com/modelcontextprotocol/go-sdk
- TypeSafe docs index: https://docs.typesafe.ai/llms.txt · local copies: `typesafeai.md` (build guide), `typesafeapi.md` (API reference)
- Ollama structured outputs: https://docs.ollama.com/capabilities/structured-outputs
- pi: https://pi.dev/docs/latest · RPC mode: https://pi.dev/docs/latest/rpc · custom models: https://pi.dev/docs/latest/models
