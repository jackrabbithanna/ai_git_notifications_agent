# GitInbox — Architecture

This document describes how GitInbox is built: process model, packages, data flow, data model,
the forge seam, the AI layer, the impact engine, evaluation, security, frontend, build and the
extension points. It is written against the code as of 2026-09-20 (milestones M0–M5 complete);
`PLAN.md` holds the decision history and live measurements per milestone.

---

## 1. Principles

1. **Code first, models second.** Anything derivable from data — activity kind, who you are to a
   thread, noise, which layers a PR touches, Mine lists — is computed deterministically. Models
   answer only what code cannot: does this need me, how urgent, what kind of change is this, will
   it break what I build.
2. **Typed judgments, cached by version.** Every model answer is a typed value (probability,
   choice with distribution, ordinal score) stored against a thread version or PR head commit. The
   priority score is pure code over stored answers, so weights change without re-inference.
3. **Calibrated by default, uncalibrated when flagged.** TypeSafe Jev's probabilities are treated
   as calibrated. Any other provider (Ollama) is marked `Calibrated=false`, badged in the UI and
   discounted in scoring.
4. **Prose is lazy.** Summaries, notes and digests are generated on demand or in small throttled
   batches, never for every thread.
5. **Read-only by default.** Three independent layers keep the app from writing to a forge unless
   an explicit per-account setting says otherwise, and even then only two narrow tools.
6. **Forge-agnostic core.** GitHub and GitLab are `Source` implementations; everything from the
   store onward is forge-neutral.

---

## 2. Process model

```
┌──────────────── gitinbox (Wails v3 desktop app, one Go binary) ────────────────┐
│  WebKitGTK webview: React/TS/Tailwind  ◄─ generated TS bindings + events ─►     │
│  Wails services (Go): Accounts Inbox Mine Diagnostics Watches Judge Impact      │
│                       Profiles Prose Eval + notifications (D-Bus)               │
│                                    │                                             │
│                             internal/app.App  (db, secrets, MCP binary, pipeline)│
│                                    │                                             │
│  pipeline: scheduler ─ sync ─ classify ─ filter ─ store ─ mine ─ judge ─ analyze │
│            ─ score ─ prose ─ notify                                              │
│      │              │               │                 │              │           │
│  store (SQLite)  secrets (keyring)  source/github     source/gitlab  judge/llm   │
│                                     │ stdio MCP        │ REST         │ HTTPS     │
│                              github-mcp-server   GitLab API    api.typesafe.ai │
│                              (one subprocess      (client-go)   Ollama /api/chat│
│                               per account)                                      │
└─────────────────────────────────────────────────────────────────────────────────┘
       gitinbox-cli (cmd/gitinbox): same internal/app + pipeline, no GUI, no D-Bus
```

- **Desktop app** (`main.go`): builds `app.App`, registers the services, opens the window, sets up
  the tray (unread count label; Open / Sync now / Quit), starts the scheduler with a 3-minute
  interval, and runs a warm-up `SyncAndJudge` two seconds after launch.
- **CLI** (`cmd/gitinbox`): opens the same `app` core with a nil notifier and calls pipeline methods
  directly. Both can run concurrently; per-account `beginSync` guards prevent overlapping syncs.
- **GitHub MCP server**: `github/github-mcp-server` v1.12.2, compiled at build time from the Go
  `tool` dependency and embedded (`internal/mcpbin`), extracted once per content hash to
  `$XDG_DATA_HOME/gitinbox/bin/`. One process per GitHub account, launched with the account token
  in `GITHUB_PERSONAL_ACCESS_TOKEN`, `GITHUB_HOST` for GHES, `--toolsets context,notifications,
  issues,pull_requests,repos`, and `--read-only` unless the account's write mode says otherwise.
- **GitLab**: no subprocess; `gitlab.com/gitlab-org/api/client-go/v3` against `https://<host>/api/v4`.
- **Jev**: HTTPS to `https://api.typesafe.ai/v1/systemone` (45 s timeout, 4 retries with jittered
  backoff on 429/529; 401/422 are fatal for the run).
- **Ollama**: `/api/chat` on a configurable base URL (15-minute client timeout, `keep_alive: 30m`).

---

## 3. Package map

| Package | Responsibility |
|---|---|
| `internal/app` | Wiring: resolve DB path (`GITINBOX_DB` or `$XDG_DATA_HOME/gitinbox/gitinbox.db`), run the legacy `ghinbox` data-dir migration, open the store, log to `gitinbox.log` + stderr, locate the MCP binary, build the pipeline with a GitLab factory; `StartScheduler`, `Close`. |
| `internal/store` | SQLite (`modernc.org/sqlite`, CGO-free) with embedded migrations `0001`–`0006`; typed accessors for every table; thread upsert with local-state preservation; queries for inbox, mine, analyses, summaries, labels, usage. |
| `internal/secrets` | `Store{Get,Set,Delete,Backend}`: OS keyring (Secret Service via `zalando/go-keyring`, service `gitinbox`, legacy fallback to `ghinbox` with copy-on-read) or a `0600` JSON file under `~/.config/gitinbox/`. Keys: `account:<id>`, `jev.api_key`. |
| `internal/mcpbin` | `Locate(Options)`: override path → bundled (extracted) → `PATH`; `BundledVersion`. |
| `internal/ghmcp` | MCP client for one `github-mcp-server` process: spawn with flags/env, `CommandTransport`, per-mode tool allowlist, `CallRaw` with one restart-and-retry on transport failure, call/restart counters, typed wrappers (`GetMe`, `ListNotifications`, `GetNotificationDetails`, `DismissNotification`, `ManageNotificationSubscription`, `IssueRead`, `PullRequestRead`, `SearchIssues`, `SearchPullRequests`, `ListPullRequests`), JSON-in-text parsing, `NewForTest` over in-memory transports. |
| `internal/classify` | Activity kind + relation tags + `IsNewItem` from notification fields; HTML URL derivation. |
| `internal/filter` | `Rules` (settings key `filter.rules`) and `Apply` → keep or noise with a reason (`bot`, `ci_success`, `release`, `muted_repo`, `keyword`). |
| `internal/source` | The forge seam (§5): `Source` plus optional `Watcher`, `Enricher`, `Changer`, `LandedLister`, `ScopeReporter`; `ChangeSet`; `ErrWritesDisabled`, `ErrNotMirrorable`; `TrimText`. |
| `internal/source/github` | `Source` over `ghmcp`: paged `list_notifications` → classified threads; Mine via four searches; enrichment via `issue_read`/`pull_request_read` (+ latest comment by anchor id); `Changes` (PR get + paged files, unified-diff split fallback); `RecentlyMerged` via `list_pull_requests`; writes wrapped into `ErrWritesDisabled` when the allowlist refuses. |
| `internal/source/gitlab` | `Source` over client-go: to-dos → `todo:<id>` threads with `ReadExcept`; watched-project events grouped per issue/MR with to-do precedence; Mine from five queries; enrichment; `Changes` (MR + paged diffs); `RecentlyMerged`; gated writes; token scopes. |
| `internal/judge` | `Question`/`Answer`/`Response`/`Judge` types mirroring the Jev contract; question sets `Triage()` (`triage.v1`) and `Impact()` (`impact.v1`) with their JSON state structs; probability normalisation; `Fake` for tests. `judge/jev` (HTTP client, calibrated) and `judge/ollama` (structured output, uncalibrated). |
| `internal/scoring` | `Weights` (settings key `scoring.weights`), `Inputs`, `Score()` → priority, percent, bucket, pinned, unsure. |
| `internal/profiles` | Profile YAML schema, built-ins (`civicrm`, `generic`) embedded, validation and regex/glob compilation, repo matching, `Analyze()` → `Report` (layers, signals, surface hits, top files with trimmed patches, caps). |
| `internal/llm` | `Generator` interface, `Result`/`Usage`, JSON schemas for `ImpactNote`, `ThreadSummary`, `Digest`; `llm/ollama` generator and the shared `PostChat` (adds `think:false`, retries without it on rejection). |
| `internal/pipeline` | Orchestration: sources cache, sync, mine, mirror, scheduler (`pipeline.go`); judge settings/provider/candidates/explain/score (`judge.go`); profiles, analysis, landed scan, notes, impact views (`impact.go`); summaries, digest, notifications, prose scheduling (`prose.go`); labels, samples, eval, tune, report, import/export (`eval.go`). Emits UI events. |
| `internal/eval` | Metrics (`Evaluate` → `Report`), `NDCG`, Spearman, `Tune` (coordinate descent) and `Objective`. |
| `internal/services` | Thin Wails services exposing the pipeline to the frontend (§10). |
| `cmd/gitinbox` | The CLI. |
| `frontend/` | React 18 + TypeScript + Tailwind v4, Vite; views under `src/views`, helpers under `src/lib`; generated bindings under `frontend/bindings/gitinbox/...` (git-ignored). |

---

## 4. Data flow

### 4.1 Scheduler tick (`RunScheduler`, every 3 min) / `SyncAndJudge`

```
SyncAll ──► judgeAfterSync ──► analyzeAfterJudge ──► proseAfterAnalyze
```

Each stage is independent: a missing provider (`ErrJudgeOff`) or an unreachable Ollama
(`llm.ErrUnavailable`) is logged once and the tick continues; 401/422 from a judge abort that
stage for the tick.

### 4.2 Sync (`SyncAccount`)

1. Build or reuse the account's `Source` (token from secrets; sources are cached per account and
   dropped on write-mode change or removal).
2. Decide the window: first sync ever or `--full` → `Since = now − 7 days` with read notifications
   included; otherwise `Since = last since − 10 min`.
3. `Source.Notifications(opts)` returns classified, unfiltered `store.Thread` rows.
4. For each thread `storeThread`: apply `filter.Apply` (verdict + reason), then `UpsertThread`.
   The upsert keeps local state (`local_read_at`, `done_at`, `snoozed_until`) unless the incoming
   `updated_at` is newer than the stored one, in which case `done_at`/`snoozed_until` are cleared —
   the "re-surface on new activity" rule lives in SQL. Enrichment and judgments stay attached but
   become stale through the version change.
5. `Result.ReadExcept` (GitLab): every `todo:` thread not in the pending list is marked read locally,
   reconciling to-dos completed elsewhere.
6. If the source is a `Watcher` and the account has watched projects: `SyncWatched` → more threads,
   per-project cursor updates (`last_event_at`, `last_error`).
7. Mine, when due (10 min): `Source.Mine` → `items` upsert with merged relations.
8. Persist `sync_state`, emit `sync:report` / `sync:error` and `inbox:updated`.

### 4.3 Judge (`JudgeAccount`)

1. Resolve the provider: explicit `provider`, or `auto` = Jev if a key is stored, else Ollama if a
   judge model is set, else `ErrJudgeOff`.
2. Candidates: unread, non-noise, not-done threads whose stored judgment is missing or keyed to an
   older version; capped by `MaxPerRun` (200).
3. Worker pool (`Concurrency` 3). Per thread (`judgeThread`): if `EnrichedVersion ≠ Version()` and
   the source is an `Enricher`, fetch item body/state/labels/author/created_at and the exact latest
   comment (one or two requests) and store the enrichment; `BuildTriageState`; `Judge.Ask(state,
   Triage().Questions)`; `PutJudgment` keyed `(account, thread, version, questions_version)` with
   provider, model, calibrated flag, answers, usage, latency.
4. Emit `judgments:updated`.

### 4.4 Impact (`AnalyzePending`, `AnalyzePR`, `ScanLanded`)

- **Candidates**: unread PR/MR threads whose repo matches an enabled non-generic profile and that
  have no analysis with `ImpactLevel ≥ 0` for the thread's current version; capped by `MaxPerRun`
  (20).
- **`AnalyzePR(acct, repo, number, profileID, force)`**: pick the profile (explicit → matching →
  generic); `Changer.Changes` (metadata + up to 300 files with patches); reuse the stored analysis
  when the head SHA is unchanged unless `force`; `profiles.Analyze` → report; `BuildImpactState`;
  `Judge.Ask(state, Impact().Questions)`; store `pr_analysis` (level = rounded
  `downstream_impact`, change kind, answers, report JSON, head SHA, state, merged_at). Returns
  whether the judge actually ran.
- **`ScanLanded`**: throttled by `ScanLandedEveryMin` (30) via `sync_state.last_landed_scan`; for
  every enabled profile repo on this account's forge, `LandedLister.RecentlyMerged(since, 30)` and
  analyse the ones never seen. Look-back `LandedLookbackDays` (7) or one hour before the last scan.
- **`ImpactNote`**: model = `impact.note-model` → judge model; user payload = PR meta, downstream
  description, layers, signals, surface hits, top files, judged answers; system prompt
  `noteSystemPrompt`; JSON-schema output → `SetAnalysisNote`. Automatic at
  `AutoNoteMinLevel` (default 4 = never).
- Emit `impact:updated`.

### 4.5 Prose (`proseAfterAnalyze`)

1. `NotifyNew`: score up to 500 threads; for each judged thread that is pinned or in *Needs me*,
   `MarkNotified("thread:<acct>:<id>:<version>")` de-duplicates; impact analyses at
   `NotifyImpactMinLevel`+ are keyed by head SHA; at most `NotifyMaxPerRun` (5) notifications then
   one "N more" notification. Delivered through the Wails `notifications` service (D-Bus).
2. `SummarizeTop(TopN=3)` when `SummarizeEveryMin` (15) has elapsed: highest-priority threads
   lacking a summary for their version; `Summarize` enriches first, feeds the previous summary for
   "changed since last read", and stores `summaries` keyed by version.
3. `GenerateDigest(since)` when `DigestEveryH` (24) has elapsed: threads updated or first seen
   since `since`, scored and capped to `DigestMaxThreads` (40), with their summaries or trimmed
   latest activity, plus impact analyses ≥ likely analysed in the period; stored in `digests`.
4. Events `summary:updated`, `digest:updated`, `inbox:updated`.

### 4.6 Scoring (`ScoreThreads`)

Loads weights, the latest judgment per thread (with staleness by version), the latest analysis per
PR (any head SHA), and summaries; runs `scoring.Score` per thread; returns `Scored{Thread, Score,
Summary, SummaryStale, Impact *ImpactBrief}`. The Inbox service groups: pinned/needs-me first,
then by activity kind in a fixed order, then a *Resolved* group; each group sorted pinned →
priority → recency.

---

## 5. The forge seam (`internal/source`)

```go
type Source interface {
    Forge() string
    Login(ctx) (string, error)                    // account-add probe
    Notifications(ctx, Opts) (Result, error)      // classified store.Thread rows for the window
    Mine(ctx) ([]store.Item, error)
    MarkRead(ctx, threadID) error                 // ErrNotMirrorable when the forge has no such state
    MarkDone(ctx, threadID) error                 // ErrWritesDisabled in readonly mode
    Unsubscribe(ctx, threadID) error
    Close() error
}
// optional capabilities, discovered by type assertion:
type Watcher       interface { SyncWatched(ctx, watched, accountID) (threads, updates, warnings, err) }
type Enricher      interface { Enrich(ctx, store.Thread) (store.Enrichment, error) }
type Changer       interface { Changes(ctx, repo, number) (ChangeSet, error) }
type LandedLister  interface { RecentlyMerged(ctx, repo, since, limit) ([]ChangeRef, error) }
type ScopeReporter interface { TokenScopes(ctx) (string, error) }
```

**GitHub** (`source/github`): `ListNotifications` 50/page, up to 40 pages, `include_read_notifications`
on full syncs; `classify.Classify` per row; Mine = `assignee:@me`, `mentions:@me`,
`review-requested:@me`, `author:@me` (`is:open`), relations merged per item; enrichment through
`issue_read`/`pull_request_read` with the latest comment matched by `#issuecomment-`,
`#discussion_r`, `#pullrequestreview-` anchors; `Changes` = `pull_request_read get` + paged
`get_files`, falling back to splitting `get_diff` when patches are omitted; `RecentlyMerged` =
`list_pull_requests` sorted by update with `merged_at` in the requested fields.

**GitLab** (`source/gitlab`): to-dos (pending, ≤20 pages; on full syncs also done to-dos of the last
7 days) → `todo:<id>` threads (`reason` = action, `actor` = author, `latest_comment_url` when the
target URL has `#note_`), plus `ReadExcept` for the pending set; `classifyTodo` maps actions to
kinds/relations (`assigned`→assignment, `mentioned|directly_addressed`→mention,
`review_requested|approval_required|added_approver`→review_requested, `review_submitted`→review,
`build_failed`→ci, `unmergeable|merge_train_removed`→state_change …). Watched projects:
`ListProjectVisibleEvents` since the cursor (5 pages × 100, ascending), grouped per
`(project, Issue|MergeRequest, iid)` into `gl:<pid>:<Kind>:<iid>` threads — skipped when a pending
to-do targets the same item. Mine: issues/MRs `assigned_to_me`, `created_by_me`, MRs by
`reviewer_id`, plus mention to-dos. Writes: only in `notifications` mode; `MarkDone` →
`Todos.MarkTodoAsDone` for `todo:` threads (event threads: `ErrNotMirrorable`), `Unsubscribe` →
issue/MR unsubscribe, `MarkRead` → `ErrNotMirrorable`. In readonly mode only `GET`s are issued.

**Adding a forge**: implement `Source` (+ whichever capabilities exist), register a factory on the
pipeline (`SetGitLabFactory` is the pattern), add the forge constant in `store`, and extend the
account-add UI/CLI. Thread ids must be prefixed so they cannot collide with other forges.

---

## 6. Data model (SQLite)

Migrations are embedded SQL files applied in order at open; the schema version is tracked in the
database.

| Migration | Tables / columns |
|---|---|
| `0001_init` | `accounts(id, login, host, write_mode, created_at)`, `threads(...)`, `items(...)`, `sync_state(account_id, last_since, last_full_at, last_mine_at, last_sync_at, last_error, …)`, `settings(key, value_json)` |
| `0002_forges` | `accounts.forge`, `accounts.token_scopes`, `threads.actor`, `watched_projects(account_id, path, project_id, last_event_at, last_error, created_at)` |
| `0003_judgments` | `judgments(account_id, thread_id, thread_version, questions_version, provider, model, calibrated, answers_json, usage_json, latency_ms, created_at)`; enrichment columns on `threads` (`item_author, item_state, item_draft, item_labels, item_body, latest_author, latest_body, latest_at, enriched_version`) |
| `0004_impact` | `profiles(id, yaml, …)`, `profile_flags(id, enabled)`, `pr_analysis(account_id, forge, repo, number, kind, head_sha, thread_version, profile_id, title, html_url, author, state, merged_at, updated_at, report_json, questions_version, provider, model, calibrated, answers_json, change_kind, impact_level, note_json, note_model, analysed_at, …)`, `threads.item_created_at`, `sync_state.last_landed_scan` |
| `0005_prose` | `summaries(account_id, thread_id, thread_version, model, content_json, usage_json, latency_ms, created_at)`, `digests(id, period_start, period_end, model, content_json, usage_json, latency_ms, thread_count, created_at)`, `notified(key, at)` |
| `0006_eval` | `labels(account_id, thread_id, category, requires_action, urgency, relevance, priority, resolved, noise, note, …)`, `pr_labels(account_id, repo, number, impact_level, change_kind, note)`, `eval_judgments` (same shape as `judgments`, keyed additionally by provider) |

Key semantics:

- `threads` primary key `(account_id, thread_id)`. `Version()` = first 16 hex chars of
  `sha1(updated_at | latest_comment_url | unread)`. `IsRead()` = forge-unread false **or**
  `local_read_at` set. `IsBrandNew()` = enriched and created less than 30 minutes before the
  notification's `updated_at`.
- Inbox queries exclude `done_at IS NOT NULL`, active snoozes, noise and read threads unless the
  respective include flag is set.
- `items` is the Mine set: `(account_id, repo, number)` with `kind` issue|pr|mr and merged
  `relations`.
- `settings` holds JSON blobs: `filter.rules`, `judge.settings`, `scoring.weights`,
  `impact.settings`, `prose.settings`, `prose.state` (last top-N / digest timestamps).
- `UsageStats` aggregates tokens and latency across `judgments`, `pr_analysis`, notes,
  `summaries` and `digests` by provider/model.

Secrets are never in the database.

---

## 7. AI layer

### 7.1 Judge abstraction (`internal/judge`)

```go
type Question struct { ID string; Kind Kind /* noul|choice|score */; Instructions string
                       NoulTrue, NoulFalse string; Options []Option; Levels []string }
type Answer   struct { Kind; Noul float64; Choice string; Probabilities map[string]float64
                       Confidence float64; Score float64 /* level index, fractional */ ; Legend map[string]string }
type Judge interface { Name() string; Calibrated() bool; Ask(ctx, state any, qs []Question) (Response, error) }
```

`Answer.Normalized()` maps a score to 0..1 by `score / (levels−1)`. Question sets are versioned
(`triage.v1`, `impact.v1`); changing rubric text means bumping the version, which invalidates cached
judgments for that set. Ids are not sent to the model, so every option and level carries a
standalone description.

**Jev provider** (`judge/jev`): builds the wire request `{state, model, questions{id: {type,
instructions, criteria}}}` — `criteria` is `{true,false}` for noul, `{option: description}` for
choice, `[level descriptions]` for score — and parses `{answers, usage}`; probabilities are
normalised and the top probability becomes `Confidence` when the API omits it. Errors: 401 →
`ErrUnauthorized` (fatal), 422 → `ErrInvalidRequest` (fatal, names the field), 429/529 → retry with
jittered exponential backoff (4 attempts, 1 s base), other → `ErrUnavailable`.

**Ollama provider** (`judge/ollama`): one `/api/chat` call per thread with `format` set to a JSON
schema generated from the question set (noul → `{probability}`, choice → `{choice,
probabilities{option…}}`, score → `{probabilities{"0"…"n"}}`; the choice and the weighted level
index are derived from the distributions), `temperature 0`, `keep_alive 30m`,
`think:false`; the system prompt explains the answer contract and the user message is
`STATE:` (JSON) + `QUESTIONS:` (rendered instructions, options, levels). Probabilities are the
model's own and therefore `Calibrated() == false`. A 404 means the model is not pulled.

### 7.2 State builders

- `BuildTriageState(acct, thread, interests)` → `{thread{forge, repo, subject_type, number, title,
  body≤1500, state, is_draft, labels, author, activity_kind, reason, updated_at}, me{login,
  relation_tags, is_author}, latest_activity{author, body≤1500, created_at, kind}, profile{interests}}`.
- `BuildImpactState(acct, changeset, report, profile)` → `{pr{…counts…}, layers_touched[],
  signals[], surface_hits[], changed_files[≤12 with patch≤2500], downstream_description}`.

### 7.3 Scoring (`internal/scoring`)

See USER-DOCUMENTATION §9 for the formula. Implementation notes: the maximum attainable priority is
computed alongside (kind max + relation max + recency, plus judgment weights when judged, plus
impact when analysed) so `Percent` is comparable across judged and unjudged rows; uncalibrated
answers scale the judgment term by `UncalibratedDiscount`; `ImpactLevel ≥ 3` forces the *Needs me*
bucket; `Pinned` forces 100 %.

### 7.4 Generation (`internal/llm`)

`Generator.Generate(ctx, system, user, schema) (Result, error)` with JSON-schema constrained
output. Schemas: `ImpactNoteSchema`, `ThreadSummarySchema`, `DigestSchema`. The Ollama generator
posts `{model, stream:false, format, keep_alive, options{temperature 0.2}, messages}` through the
shared `PostChat`, which injects `think:false` and retries without it when the server rejects the
field. Usage (`prompt_eval_count`, `eval_count`) and latency are recorded with every stored
artefact.

Model precedence: summaries use `summary.model` → `impact.note-model` → `ollama.model`; digests
`digest.model` → summary chain; notes `impact.note-model` → `ollama.model`. All calls go to
`ollama.url`.

### 7.5 Measured behaviour (from PLAN.md)

Jev: ~0.6 s and ~1.4k input tokens per triage; impact judgment on a 6-file PR 1.8 s. Ollama
`qwen3.5:9b` with `think:false`: summary 2.5 s, 40-thread digest 13 s, triage judgment 6 s. A
27B model spilling to CPU: ~5 min per judgment — hence the guidance to use fully resident models.

---

## 8. Impact engine (`internal/profiles` + `pipeline/impact.go`)

- **Schema**: `Profile{ID, Name, Repos, DownstreamDescription, Layers[{ID, Label, Weight, Paths}],
  IgnorePaths, Signals, SurfacePatterns[{ID, Pattern}]}` (+ loader-set `Source` builtin|user,
  `Enabled`, `YAML`). Globs are doublestar; regexes are compiled at parse time; weight defaults to
  0.5; the first matching layer wins.
- **Repo matching**: `owner/name` = GitHub; `github:owner/name`; `gitlab:<host>/<path>`.
  `ProfileFor(acct, repo)` finds the enabled, non-generic profile covering the repo on the account's
  forge/host.
- **`Analyze(profile, title, body, labels, files)`**: ignore paths first; each file to a layer (or
  *unclassified*, ≤30 listed); per-layer files (≤25) with +/− counts, sorted by weight; signal
  keywords found in title/body/labels; surface regexes over `+`/`−` diff lines (≤40 hits, lines
  trimmed to 200); top files = highest layer weight then size, ≤12, patches trimmed to 2,500 bytes
  and 60 KB total. `MaxWeight` summarises the heaviest layer touched.
- **Persistence**: `pr_analysis` keyed `(account, repo, number)` storing the head SHA and the thread
  version the analysis was made for; `AnalyzePR` reuses when the SHA matches; `AnalyzePending`
  re-runs when the thread version moves.
- **Views**: `ListImpact(AnalysisQuery{AccountID, MinLevel, Landed, Any, Limit})` and `GetImpact`
  return `ImpactView{Analysis, Report, Answers, Note}` for the Impact view, the inbox details panel
  and the explain panel.

---

## 9. Evaluation (`internal/eval` + `pipeline/eval.go`)

- **Labels**: per thread (`category`, `requires_action`, `urgency`, `relevance`, `priority`,
  `resolved`, `noise`, `note`) and per PR (`impact_level`, `change_kind`). `LabelQueue` offers
  unlabeled threads, judged first, with the app's current view for context.
- **Samples**: `Samples(provider)` joins labels with either the primary `judgments` table or the
  `eval_judgments` rows of a provider, plus filter verdict and impact level.
- **Metrics** (`Evaluate`): category accuracy + confusion + sure/unsure accuracy; binary
  precision/recall/F1 and Brier for requires-action (threshold 0.5) and resolved; ordinal exact,
  ±1 and MAE for urgency and relevance; ranking NDCG@10/@25 and Spearman between priority score and
  priority label; filter recall with the list of missed noise; *Needs me* bucket F1 vs
  requires-action labels.
- **Tuner** (`Tune`): seeded coordinate descent over the scalar weights and thresholds (kind and
  relation priors are left to hand editing); objective `NDCG@25 + 0.25 × needs-me F1`; returns
  before/after weights and objective values; `apply` stores them under `scoring.weights`.
- **Report**: Markdown per provider under `eval/report-<timestamp>.md` (the `eval/` directory is
  created on first report). Labels export/import as JSON lines keyed by login/host + thread id.

---

## 10. Frontend and services

**Services** (Go, `internal/services`; bindings generated in interface mode, so TS types are plain
interfaces): `AccountsService{List, Add, Remove, SetWriteMode}`, `InboxService{List, Sync,
MarkRead, MarkDone, UndoDone, Snooze, Unsnooze, Unsubscribe}`, `MineService{List, Refresh}`,
`DiagnosticsService{MCPServerInfo, Environment, Tools, ClientStats, FilterRules, SetFilterRules,
Usage}`, `WatchesService{List, Add, Remove}`, `JudgeService{Status, SaveSettings, SetJevKey,
OllamaModels, Weights, SaveWeights, DefaultWeights, Run, Explain}`, `ImpactService{List, Get,
Analyze, Note, RunPending, Settings, SaveSettings}`, `ProfilesService{List, Save, Validate,
Delete, SetEnabled}`, `ProseService{Summary, Summarize, SummarizeTop, GenerateDigest, Digests,
Settings, SaveSettings, TestNotification}`, `EvalService{Queue, SetLabel, SetPRLabel, PRLabels,
Overview, Evaluate, EvaluateImpact, EvalJudge, Tune, Report}`.

**Events** (pipeline → UI): `inbox:updated`, `sync:report`, `sync:error`, `judgments:updated`,
`impact:updated`, `summary:updated`, `digest:updated`, `labels:updated`. `App.tsx` subscribes once
and bumps a refresh key; the tray listens to `inbox:updated` to refresh its count.

**Views** (`frontend/src/views`): `Inbox` (tabs from group kinds, sort, tag filters incl. dynamic
account tags, row actions; `ThreadDetails` fieldset and `Explain` panel), `Mine`, `Impact`
(exports `ImpactDetails`, `levelName`), `Digest`, `Eval`, `Profiles`, `Settings`, `Diagnostics`.
`lib/ui.tsx` (Button, Chip, Card, ErrorText), `lib/format.ts` (durations, relative time, reason
labels), `lib/browser.ts` (open URLs). Per-viewer preferences (`inbox.tab`, `inbox.sort`) live in
`localStorage`.

---

## 11. Security model

**Write gating** — three layers, all must agree before a write reaches GitHub:

1. *Server flag*: `--read-only` unless `accounts.write_mode = notifications`; the server does not
   register write tools at all. Changing the mode restarts the subprocess.
2. *Client allowlist*: `ghmcp.AllowedTools(mode)` — reads always; `notifications` adds exactly
   `dismiss_notification` and `manage_notification_subscription`. Calls outside the list are
   rejected in code with a logged error.
3. *Token scopes*: a classic PAT with only `notifications` makes the server register 25 read tools;
   with writes enabled 29, of which the allowlist admits 2. Fine-grained tokens skip this layer.

GitLab mirrors the policy in code: the source holds a `writes` flag from the account mode; the
only non-GET requests possible are `mark_as_done` and unsubscribe.

**Secrets**: tokens/keys only in the keyring (or a `0600` file); the CLI reads them from files the
user creates and never echoes them; logs never include tokens (`GITINBOX_DEBUG` shows server
stderr, which the MCP server keeps token-free).

**Data egress**: forge APIs (reads), Jev (trimmed thread/PR state), Ollama (same + prose inputs).
No telemetry.

**Local data**: `$XDG_DATA_HOME/gitinbox/` (database, log, extracted server binary) and
`~/.config/gitinbox/secrets.json` only as fallback. The legacy `ghinbox` directory is moved, not
copied, on first run of the renamed app; keyring entries are copied on first read and the old ones
left for the user to delete.

---

## 12. Build, packaging, CI

- `Taskfile.yml` + `build/Taskfile.yml` (`common:`) + `build/<os>/Taskfile.yml`. `wails3 build` →
  `linux:build` → `build:native`: `go:mod:tidy` → `build:mcp` (compiles the pinned
  `github-mcp-server` with `CGO_ENABLED=0`, `-X main.version=<module version>`, into
  `internal/mcpbin/bin/` + `VERSION`; re-run only when `go.mod`/`go.sum` change) →
  `build:frontend` (`npm install`, `tsc`, Vite) → `generate:bindings` → `generate:icons` →
  `generate:dotdesktop` → `go build` into `bin/gitinbox`.
- `wails3 dev` uses `build/config.yml`: `wails3 build DEV=true`, Vite in the background, a
  `common:wait:frontend` curl poll (Vite can exceed Wails' 5-second retry window on slow hosts),
  then run.
- `wails3 package` → AppImage, `.deb`, `.rpm`, Arch package via `build/linux/nfpm/nfpm.yaml`
  (installs `/usr/local/bin/gitinbox`, icon, `.desktop`; depends on GTK4/WebKitGTK 6.0 per distro).
- `go build ./...` fails on the template's `build/ios` package (a `main` without `main()`); use
  `wails3 build` or `go build .` and `go build ./internal/... ./cmd/...`.
- Git-ignored build products: `bin/`, `frontend/dist/*` (a `.gitkeep` mirrored from
  `frontend/public` keeps the `go:embed` directive valid), `frontend/bindings/`,
  `internal/mcpbin/bin/*`.
- CI (`.github/workflows/build.yml`): `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64):
  install GTK/WebKit dev packages, Wails CLI, `wails3 build`, `go vet`, `go test`, build the CLI
  and run `mcp path`, upload `bin/gitinbox` as `gitinbox-linux-<arch>`.

---

## 13. Testing

- `internal/ghmcp`: fake MCP server over in-memory transports (`NewForTest`), allowlist rejection,
  restart-and-retry.
- `internal/mcpbin`, `internal/secrets`: binary resolution order; keyring/file store behaviour.
- `internal/source/github`: notification → thread mapping on fixture data; enrichment parsing
  (string labels, GraphQL-shaped review comments matched by anchor).
- `internal/source/gitlab`: `httptest` fixtures for to-dos (incl. `#note_` links), events grouping
  with to-do precedence, Mine relation merging, readonly refusing writes with zero non-GET requests,
  token scopes.
- `internal/classify`, `internal/filter`, `internal/profiles`: table tests (kind derivation, noise
  rules, glob→layer, surface regexes on real CiviCRM diffs, caps).
- `internal/judge`: question-set validation and fake judge; Jev wire round-trip, retry and error
  mapping; Ollama schema generation and answer parsing.
- `internal/scoring`: monotonicity and bucket/pinned/unsure rules.
- `internal/eval`: NDCG/Spearman, metrics, tuner improves-or-keeps.
- `internal/pipeline`: store-backed tests with fake sources/judges/generators for judge candidates
  and explain, impact pending/landed throttle and notes, prose scheduling and notification de-dup,
  eval label queue, samples and tuning.
- `internal/store`: migrations on a fresh database, thread upsert rules (local state preserved,
  cleared on new activity), queries.

`go test ./...` needs no network or tokens.

---

## 14. Extension points and roadmap

- **pi (pi.dev) agent sidecar — M6, not in v1.** Planned as an `AgentService` that spawns
  `pi --mode rpc` (JSONL over stdio) with an app-owned agent directory (`models.json` pointing at
  Ollama's OpenAI-compatible endpoint, a system prompt, coding tools disabled), a TypeScript
  extension exposing `search_threads`, `get_thread`, `get_thread_summary`, `list_top_priority`,
  `list_mine`, `get_pr_analysis`, `mark_done`, `snooze` over a loopback JSON API (`localapi`,
  also not yet built), and the same `github-mcp-server` binary attached **always read-only** for
  deep-dives; `draft_reply` drafts only. Chat-with-inbox panel and thread deep-dive drawer in the
  UI. Rationale for the sidecar shape: pi is TypeScript; embedding would need a Node host.
- **New judge provider**: implement `judge.Judge`, return `Calibrated()` honestly, wire into
  `Pipeline.Judge` and the provider setting.
- **New question set version**: copy the set, bump the version constant, adjust consumers that
  read answer ids; old judgments are ignored automatically.
- **New forge**: §5.
- **New profile**: YAML in the Profiles view; built-ins live in `internal/profiles/builtin`.
- **Later** (PLAN.md §8): macOS/Windows CI, OAuth device flow instead of PATs, GitLab accounts for
  gitlab.com/third instances via the UI (supported by the code; needs tokens), low-risk writes
  (reactions/labels) as new write-mode enum values with their own allowlists, optional cloud LLM
  behind `llm.Generator`.
