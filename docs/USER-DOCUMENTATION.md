# GitInbox — User Documentation

GitInbox is a desktop inbox for GitHub and GitLab notifications that does the reading before you
do. It pulls every notification (and every to-do, on GitLab) for one or more accounts, works out
deterministically what kind of activity each one is, filters the noise, asks a calibrated judge
(TypeSafe Jev) what needs you and how urgently, analyses pull requests in the upstream projects you
build on for changes that would affect your code, and — with a local Ollama model — writes
summaries, impact notes and a digest — and lets you chat with all of that through a [pi](https://pi.dev)
agent that can only read. Read, Done, Snooze and Mute are local state unless you opt in to mirroring
them to the forge.

This document explains every concept, view, setting and CLI command. Installation is covered in
[INSTALLATION.md](INSTALLATION.md); internals in [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 1. Concepts

### Accounts and forges

An **account** is one login on one forge: `jackrabbithanna@github.com`,
`jackrabbithanna@lab.civicrm.org`. GitHub accounts talk to a bundled copy of the official
`github-mcp-server` (one subprocess per account); GitLab accounts (gitlab.com or self-hosted) use
the official REST client. Everything downstream — inbox, judgments, impact, digest — is
forge-agnostic and merged across accounts.

### Threads

A **thread** is one notification: a GitHub notification thread, a GitLab to-do (`todo:<id>`), or a
bundle of GitLab project events about one issue/MR (`gl:<project>:<Issue|MergeRequest>:<iid>`).
Each thread carries the repository, the subject (issue, PR/MR, release, commit, discussion, alert),
a title, the forge's `reason`, who acted last, and a timestamp.

Every thread has a **version** — a fingerprint of `updated_at`, the latest comment URL and the
unread flag. Judgments, enrichment, summaries and analyses are keyed by it, so anything the model
concluded is automatically stale the moment there is new activity.

### Activity kind and relation

Before any AI runs, code derives two facts from the notification fields alone:

- **Activity kind** — what the latest event is: `new_issue`, `new_pr` (new *or pushed*; GitHub
  does not distinguish until the item is enriched), `comment`, `review`, `review_requested`,
  `state_change` (merged/closed/reopened), `ci`, `release`, `mention`, `assignment`, `security`,
  `discussion`, `commit`, `other`.
- **Relation tags** — how *you* relate to the item: `author`, `assignee`, `reviewer`,
  `mentioned`, `participant`, `subscriber`.

These drive the Inbox tabs, the fallback priority when nothing is judged, and part of what the
judge is told.

### Noise filter

Rule-based and free: dependency-bot titles, green CI, releases from repos you did not list, muted
repos, muted title keywords. Filtered threads are kept (marked `noise`) and can be shown with the
*noise* toggle; they are never judged or summarised. Rules live in **Settings → Noise filter**.

### Judgments (triage)

For each unread, non-noise thread the **judge** answers six typed questions
(`triage.v1`) over a JSON state describing the thread, you, the latest activity and your stated
interests:

| Question | Type | Answer |
|---|---|---|
| category | choice | `needs_my_review` · `needs_my_reply` · `blocking_or_failing` · `awaiting_others` · `fyi_progress` · `release_or_announcement` · `resolved_no_action` |
| requires_action_from_me | probability | does the latest activity expect something from you |
| urgency | 0–3 | no time pressure · this week · today · blocking right now |
| relevance | 0–2 | unrelated · tangential · directly my area (vs. your *interests*) |
| resolved | probability | already settled, nothing left for you |
| next_action | choice | `review` · `reply` · `rebase_or_fix` · `merge` · `read_only` · `nothing` |

Two providers exist. **Jev** (TypeSafe System One) is *calibrated*: its probabilities mean what they
say, so a 0.35 vs 0.34 split is honest hedging and shows as *unsure*. **Ollama** is the *uncalibrated*
fallback: a local model asked the same questions with a JSON schema; its self-reported probabilities
are discounted in scoring (×0.7 by default) and badged. Judgments are cached per thread version, so
re-ranking never costs another call.

### Priority score and buckets

A weighted sum over the answers, the deterministic priors and recency (formula in §9). It yields a
0–100 **priority %** and a **bucket**:

- **Needs me** — `requires_action ≥ 0.5`, or category is needs-my-review/reply/blocking, or a PR
  with *certain* downstream impact.
- **Resolved** — `P(resolved) > 0.8` (unless pinned).
- **Normal** — everything else, shown under its activity kind.

Two hard rules: `blocking_or_failing` on something you authored is **pinned** to 100 %; a category
confidence below 0.5 shows an **unsure** badge and is never hidden.

### Impact profiles and analysis

An **impact profile** describes an upstream project you depend on: which repositories, which paths
form which *layers* (data & API, hooks, UI, packaging…), which keywords in a title/body are
*signals*, which regexes over diff lines are *surface* changes (a public function signature, a
hook, a schema field), and a free-text *downstream description* of what you build on it. The
built-in **civicrm** profile covers CiviCRM core and its CMS integrations; **generic** is the
fallback for any other repository.

**Analysis** of a PR/MR is code first — fetch metadata and changed files, map files to layers,
scan signals, apply surface regexes, keep the top 12 files with trimmed patches — then five typed
questions (`impact.v1`): change kind (bugfix … architectural, api_change, data_model_change,
deprecation_or_removal …), affects downstream, **downstream impact** (none / possible / likely /
certain), backward compatible, needs my attention now. An optional Ollama **impact note** turns the
result into prose: what changed, why it matters to you, surfaces, checks to run, migration hints.

### Summaries, digest, notifications

With an Ollama model configured, a thread **summary** (two sentences, key points, *asks of me*, and
*changed since last read* against the previous summary) is written on demand and, automatically,
for the top-3 priority threads every 15 minutes. A **digest** groups a period's activity into
*Needs you / Impact on your projects / Worth knowing / Resolved* with suggested actions, daily by
default. **Desktop notifications** fire once per thread version when a thread enters *Needs me* and
once per head commit for impact analyses at the configured level.

### Local state vs. mirrored writes

**Read**, **Done**, **Snooze** and **Mute** always work and are stored locally. A done or snoozed
thread stays hidden until *new activity* moves its `updated_at` forward, at which point it
re-surfaces. Only when an account's **write mode** is `notifications` are Done and Mute also sent to
the forge (GitHub: dismiss / unsubscribe; GitLab: to-do done / unsubscribe). "Read" is local-only
on GitLab, which has no read state short of done. Nothing else is ever written.

---

## 2. The application window

The left sidebar switches between **Inbox**, **Mine**, **Impact**, **Digest**, **Agent**, **Eval**,
**Profiles**, **Settings** and **Diagnostics**, and holds an **account selector** (all accounts, or
one). The header shows the current view and the last sync error, if any. The window opens on
Settings until the first account exists.

The **tray icon** shows the unread count and offers *Open GitInbox*, *Sync now* and *Quit*. Closing
the window keeps the app (and its scheduler) running in the tray.

While the app runs, every **3 minutes** it syncs all accounts, judges new threads, analyses pending
PRs in profile repos and runs the prose step (notifications, top-N summaries, digest when due).
Views refresh live on the resulting events.

---

## 3. Inbox

The Inbox is the main working surface.

**Toolbar.** *Sync* (incremental) and *Full sync* (re-reads the last 7 days including read
notifications, reconciling changes made elsewhere); *Judge* (enrich + judge unread threads without
a fresh judgment); toggles to include **read**, **noise** and **done** threads; the notice line
reports what the last action did.

**Tabs.** One per group, in this order: *Needs me* · *Review requested* · *Mentions* · *Assigned* ·
*PRs new/pushed* · *Issues* · *Reviews* · *Replies & comments* · *Merged/closed* · *CI* · *Security* ·
*Releases* · *Discussions* · *Commits* · *Other* · *Resolved* (judged as resolved). Only non-empty
groups appear. Your last tab is remembered.

**Sort.** *Newest first* (default), *Rated priority* (pinned first, then priority %, then recency —
the order the groups are computed in), *Oldest first*.

**Tag filters.** Every chip a row can show is also a filter; pick any combination:

| Group | Tags |
|---|---|
| Score | blocking (pinned) · unsure · judged · not judged |
| Judged category | the seven triage categories |
| Impact | impact: none · possible · likely · certain |
| Item | new (created < 30 min before the notification) · updated · pull/merge request · issue |
| My relation | author · assignee · reviewer · mentioned · participant · subscriber |
| Forge | GitHub · GitLab |
| State | unread · read · done · snoozed · noise |
| Account | one chip per configured account |

**Row anatomy.** Priority % (or "—" when unjudged), forge chip, repository and number (`#123` on
GitHub, `!123` for GitLab MRs), title (opens the item in your browser), category chip, *unsure* /
*pinned* / *brand new* chips, impact chip when analysed, relation tags, who acted and when, and the
forge `reason`. A one-line summary appears under the title once one exists (click it to open the
details panel; stale summaries are marked).

**Actions per row.**

| Action | What it does |
|---|---|
| Open (title link / external icon) | Opens the item in your browser. |
| **Read** | Marks read locally (GitHub also when mirrored; GitLab never). |
| **Done** | Hides the thread until new activity. Mirrored as *dismiss* / *to-do done* in `notifications` write mode. *Undo* is available under the done toggle. |
| **Snooze** | Hides for 24 h (the CLI takes any duration). |
| **Mute** | Unsubscribes from the thread. Local-only unless mirrored. |
| **Analyze / Re-analyze** | Runs impact analysis against the matching profile (generic if none). Shows progress notices; the result appears as an impact chip and in the Impact view. Re-analyze forces a new run even for the same head commit. |
| **Deep-dive** | Switches to the Agent view and sends a prompt asking the agent to read the thread, its live discussion and (for PRs) the impact analysis, then report what it is about, what is asked of you, the state and a next action. |
| **Details** | Expands a panel with the full summary (generate / regenerate), the agent's draft reply when one exists (Copy / Delete), and the full impact analysis (layers, signals, surface hits, judgment table, note) — the same content as the Impact view, in place. |
| **Why** | Expands the explain panel: the judged answers with probabilities, the exact state the judge saw, the score breakdown, the provider and model, staleness, and a *Judge now* button. |

---

## 4. Mine

Issues and PRs/MRs where you are **assigned**, **mentioned**, **review-requested** or the
**author**, built from searches (GitHub: `assignee:@me`, `mentions:@me`, `review-requested:@me`,
`author:@me`; GitLab: `scope=assigned_to_me|created_by_me`, `reviewer_id`, plus mention to-dos) so
it is complete even for threads that never produced a notification. Refreshed every 10 minutes
during sync and on **Refresh**. Filter by relation chip, include **closed** items, one account at a
time (the selector defaults to the first account).

---

## 5. Impact

Analysed pull/merge requests, **Proposed** (open) or **Landed** (merged), filtered by minimum
impact level and account. **Analyse pending** processes unread PR threads in profile repositories
without a fresh analysis and then scans profile repos for recently merged PRs (throttled to once
every 30 minutes, 7-day look-back by default). Any PR can be analysed by reference (`owner/repo#123`)
from the CLI.

Each row expands to: layers touched (with files and +/−), signals found, surface hits (pattern,
file, line), the five judged answers with probabilities, the impact note (or **Write note** to
generate one with the Ollama model), and **Your label** — a ground-truth impact level and change
kind used by the Eval view.

---

## 6. Digest

**Generate** writes a digest for the last N hours with the digest model (falls back to the summary
model, then the judge model). The digest lists a headline, sections with one-line items and a
concrete "why", and up to five suggested actions. Previous digests are kept and selectable. With
*auto digest* on (default) one is generated every 24 hours while the app runs.

---

## 6a. Agent

The Agent view is a chat with a [pi](https://pi.dev) sidecar whose **only tools are GitInbox's own
data and read-only forge access**. It has no shell and no file access; it cannot post, comment,
approve, merge or label anything. What it can do:

- Answer questions over the inbox: top priorities, searches, what a thread is about, who is
  waiting on whom, which merged PRs affect your extensions, what your open review requests are
  waiting on. Its tools return the judged category and probabilities, the score, the summary,
  the impact analysis and the item's live discussion, so answers are grounded in what the
  pipeline already computed.
- **Deep-dive** a thread (from the inbox row button or by asking): `get_thread` →
  `get_thread_comments` (fetched live: issue comments, PR reviews and review comments, GitLab
  notes) → for PRs `get_pr_analysis` / `get_pr_changes` → what / asked of you / state / next action.
- Change local triage state **when you ask** (mark done / read, undo, snooze) — mirrored to the
  forge only as the account's write mode already allows.
- **Draft a reply** (`draft_reply`): stored locally, shown in the thread's Details panel with a
  Copy button. Nothing is ever sent.
- Reach GitHub's read tools directly (`github_read`: `issue_read`, `pull_request_read` with any
  read method, searches, `list_pull_requests`) for things the higher-level tools lack.

**Controls.** Status chip (stopped / ready / working), pi version and where it was found, a
**model** select (every model your Ollama server lists; the choice is remembered), **Abort** while
it works, **New session** (forget the conversation), **Start** / **Stop** (it also starts on the
first message and keeps running until you stop it or quit). The transcript streams as the model
writes; tool calls appear as collapsible cards with their arguments and results. Suggestion chips
cover the common questions. Enter sends, Shift+Enter inserts a newline; a message sent while the
agent is still working is queued as a follow-up.

**Requirements.** pi installed (Settings → Agent shows whether it was found; see INSTALLATION §6.4)
and an Ollama model that supports tool calling (`qwen3.5:9b` is the reference). Everything the
agent sees goes to that Ollama server, nowhere else.

**Headless.** `gitinbox-cli agent chat "What needs me most right now?"` streams the same answer to
the terminal (tool calls on stderr); `agent status` and `agent models` inspect the setup.

---

## 7. Eval

The Eval view turns your own judgement into numbers so the judge and the weights can be measured
instead of guessed.

1. **Label queue** — threads to label, unlabeled and judged ones first. For each row set: category,
   needs action (y/n), urgency 0–3, relevance 0–2, priority 0–3 (ignore / low / medium / top),
   resolved (y/n), noise (y/n), and a note. Labels save immediately.
2. **Evaluate** — for the primary judgments or any provider re-judged into the eval table:
   category accuracy with confusion and sure/unsure split, needs-action and resolved
   precision/recall/F1 + Brier score, urgency and relevance exact/±1/MAE, ranking NDCG@10/@25 and
   Spearman against your priority labels, filter recall with the noise it missed, and the
   *Needs me* bucket's F1 against your needs-action labels.
3. **Re-judge** — judge every labeled thread with Jev or the Ollama judge model into an eval-only
   table, so providers can be compared on the same threads without touching what the Inbox uses.
4. **Tune** — a seeded coordinate search over the scalar weights and thresholds maximising
   NDCG@25 (+0.25 × needs-me F1); dry run first, then *apply* to store the weights.
5. **Report** — the same metrics as Markdown, written to `eval/report-<timestamp>.md`.

PR impact labels are set in the Impact view; **EvaluateImpact** compares them with stored analyses.
Labels export/import as JSON lines (`eval export|import`) and are portable across machines.

---

## 8. Profiles

Built-in profiles (`civicrm`, `generic`) can be enabled/disabled; user profiles are YAML you write,
validate and save in the editor (or `profiles import FILE`). Schema:

```yaml
id: my-project                 # required, unique
name: My Project
repos:                         # owner/name (GitHub), github:owner/name, gitlab:<host>/<group>/<project>
  - example/my-project
  - gitlab:lab.civicrm.org/dev/core
downstream_description: >      # becomes the judge's reference for "would this affect me"
  I build extensions that use the public API, the hooks and the settings schema…
layers:                        # at least one; first match wins, ordered as written
  - id: api
    label: Public API
    weight: 1.0                # 0–1; drives which files are shown to the judge first
    paths: ["src/api/**", "schema/**"]        # doublestar globs
  - id: ui
    label: UI
    weight: 0.5
    paths: ["templates/**", "**/*.tsx"]
ignore_paths: ["tests/**", "**/*.md"]        # never counted, never shown
signals: ["BREAKING", "deprecat", "schema", "signature"]     # title/body/label keywords
surface_patterns:              # regexes over added/removed diff lines
  - id: public_sig
    pattern: '^[+-]\s*public\s+function\s+\w+\s*\('
  - id: deprecation
    pattern: '^[+-].*@deprecated'
```

A profile applies to a PR when its repository is listed (forge-qualified entries match only that
forge/host; bare `owner/name` matches GitHub). Only *enabled, non-generic* profiles trigger
automatic analysis and landed scans; `generic` is used on demand for anything else.

---

## 9. Settings

### Accounts
Add (forge selector, host, token), remove, and set the **write mode** per account. GitLab accounts
show their token scopes and a **Watched projects** editor (project paths polled for activity each
sync, since GitLab has no notifications feed).

### Noise filter
Drop bot updates · drop CI success · drop releases (except *repos whose releases to keep*) ·
muted repos (`owner/name` or `owner/*`) · muted title keywords. Rules apply at sync time.

### Triage judge
Provider (**auto** / Jev / Ollama / off), Jev API key (paste; empty removes), Jev model and URL,
Ollama URL with **List models**, Ollama judge model, max threads per run (200), concurrency (3),
and **interests** — a sentence or two about what you work on, used by the relevance question and
the digest.

### Summaries, digest & notifications
Summary model (blank = note model → judge model), top-N automatic summaries (3) every N minutes
(15), digest model, auto digest every N hours (24), max threads in a digest (40), notify on
*Needs me*, notify for impact at level ≥ (likely / certain / never), max notifications per run (5),
**Test notification**.

### PR impact analysis
Auto-analyse PR threads in profile repos (on), max analyses per run (20), note model, auto-note at
level ≥ (default: never — notes on demand), scan landed PRs (on) every N minutes (30), look-back
days (7).

### Agent (pi sidecar)
Enable (on), start with the app (off), pi binary path (blank = auto-detect: `PATH`, then the
repository's `agent/node_modules`), Ollama model (blank = summary → note → judge model), thinking
level (off / low / medium / high; off is fastest). The card shows whether pi was found and its
version. Disabling stops a running sidecar; other changes apply on its next message.

### Priority weights
Sliders for the scoring weights (§9 formula) plus recency half-life (48 h), resolved threshold
(0.8), unsure-below (0.5) and the uncalibrated discount (0.7); per-kind and per-relation priors.
Changing a weight re-ranks instantly from stored answers — no inference. **Defaults** restores the
shipped values; the Eval tuner can store better ones.

### The score formula

```
priority = w_action·P(requires_action) + w_urgency·urgency/3 + w_relevance·relevance/2
         − w_resolved·P(resolved)                                  (× 0.7 when uncalibrated)
         + w_kind[activity_kind] + Σ w_relation[tag] + w_recency·2^(−age/48h)
         + w_impact·impact_level/3                                  (analysed PRs)
percent  = priority / maximum attainable × 100;  pinned ⇒ 100
```

Defaults: action 1.0, urgency 0.8, relevance 0.6, recency 0.3, resolved 0.8, impact 0.7; kind
priors from review_requested 0.6 down to release 0.05; relation priors reviewer 0.4, author /
assignee / mentioned 0.3, participant 0.1.

---

## 10. Diagnostics

**GitHub MCP server** (source: bundled/override/PATH, version, path) · **Environment** (secrets
backend, database path, log path) · **GitLab accounts** (host and token scopes; REST, no MCP
server) ·
**Server tools** per GitHub account — every tool the server registered under the current write
mode, with the ones the client allowlist admits highlighted (25 read-only tools by default; 29 in
`notifications` mode with exactly 2 admitted) · **Agent** — state, pi binary/version, model, agent
directory, local API address, transcript length, Start/Stop · **Triage judge** — which provider resolves, whether
it is calibrated, why not, and judgment counts · **Model usage** — tokens and latency per provider
and model across judgments, analyses, notes, summaries and digests · **MCP call counters** for the
session (calls per tool, restarts).

---

## 11. Command-line interface

`gitinbox-cli` shares the database, keyring and pipeline with the app; anything done on one side is
visible on the other. Flags come *before* positional arguments. `--account` accepts a numeric id,
`LOGIN@HOST`, a host, or a login when it is unambiguous (the same login on GitHub and GitLab must
be qualified).

```
mcp path | tools [--account L] | call [--account L] TOOL [JSON]
account add --token-file F [--forge github|gitlab] [--host H] | list | remove LOGIN | write-mode LOGIN readonly|notifications
sync [--account L] [--full]
inbox [--account L] [--all] [--noise] [--done] [--limit N]
mine [--account L] [--closed] [--refresh]
watch add|remove [--account L] PATH | watch list [--account L]
judge [--account L] [--limit N]
explain [--account L] [--now] THREAD_ID
settings show | set KEY VALUE
jev-key --token-file F
analyze [--account L] [--profile ID] [--force] [--note] REPO#N | analyze --pending
impact [--landed] [--min 0-3] [--account L]
profiles list | show ID | enable ID | disable ID | import FILE | delete ID
summarize [--account L] [--force] THREAD_ID | summarize --top N
digest [--hours 24] [--generate]
usage
eval queue [--limit N] | label THREAD_ID KEY=VALUE… | pr-label REPO#N impact=0-3 [kind=…]
eval judge --provider jev|ollama [--model M] [--force] | report [--out DIR] | tune [--iters N] [--apply] | export FILE | import FILE
read|done|undone [--account L] THREAD_ID | snooze [--account L] --for 2h THREAD_ID
agent status | chat [--model M] PROMPT… | models
version
```

Settings keys for `settings set`:

| Key | Meaning |
|---|---|
| `provider` | `auto` · `jev` · `ollama` · `off` |
| `jev.model`, `jev.url` | Jev model (`jev-latest`) and base URL |
| `ollama.url`, `ollama.model` | Ollama base URL and judge model |
| `max`, `concurrency` | threads judged per run, parallel requests |
| `interests` | your interests text |
| `impact.auto`, `impact.max`, `impact.note-model`, `impact.note-level` (2·3·4=never), `impact.scan-landed`, `impact.landed-days` | impact settings |
| `summary.model`, `summary.top`, `summary.every`, `digest.model`, `digest.auto`, `digest.hours`, `notify.needs-me`, `notify.impact-level` (3·4=never) | prose settings |
| `agent.enabled`, `agent.autostart`, `agent.pi-path`, `agent.model`, `agent.thinking` (off·low·medium·high) | agent settings |

Label keys for `eval label`: `category`, `requires_action`/`action` (y/n), `urgency` 0–3,
`relevance` 0–2, `priority` 0–3, `resolved` (y/n), `noise` (y/n), `note`.

Typical headless session:

```sh
gitinbox-cli sync && gitinbox-cli judge && gitinbox-cli analyze --pending
gitinbox-cli inbox | head -40
gitinbox-cli explain --now 25702457595
gitinbox-cli summarize 25702457595
gitinbox-cli done 25702457595
```

---

## 12. Schedules and budgets

| Step | When | Budget / throttle |
|---|---|---|
| Notification sync | every 3 min (app), or `sync` | incremental since last sync − 10 min; full = 7 days incl. read; GitHub 50/page up to 40 pages; GitLab 20 pages of to-dos |
| Mine searches | every 10 min during sync, `mine --refresh` | 4 searches per GitHub account, 5 queries per GitLab account |
| Watched-project events | every sync | 5 pages × 100 events per project |
| Judge | after each sync, `judge` | unread non-noise threads lacking a judgment for their version; max 200 per run, concurrency 3; aborts the run on 401/422 |
| Impact analysis | after judge, `analyze --pending` | PR/MR threads in enabled profile repos without an analysis for their version; 20 per run; landed scan every 30 min, 7-day look-back, 30 PRs per repo |
| Notifications | after analysis | once per thread version / head sha; max 5 per run then "N more" |
| Top-N summaries | every 15 min | 3 highest-priority threads lacking a current summary |
| Digest | every 24 h | top 40 threads of the period + impact analyses ≥ likely |

---

## 13. What leaves your machine

| Destination | Data |
|---|---|
| GitHub / GitLab | Only API reads (and the two mirrored writes in `notifications` mode). |
| TypeSafe Jev | Per judged thread: forge, repo, subject type/number, title, item body (trimmed to 1,500 bytes), state, draft flag, labels, author, activity kind and reason, timestamp; your login and relation tags; the latest comment (trimmed to 1,500 bytes) with its author and time; your interests text. Per analysed PR: title, body (2,000 bytes), labels, base, state, author, counts; layers, signals, surface hits; up to 12 files with patches trimmed to 2,500 bytes each (60 KB total); the profile's downstream description. |
| Ollama server | The same triage state (plus the previous summary) for summaries; the analysis report and judged answers for notes; scored thread lists with summaries and impact rows for digests; the triage/impact state when it acts as judge; and, through the agent, your chat messages plus every tool result the agent fetched (thread views, live comments, PR files/diffs when asked). |
| Nowhere else | Tokens and keys stay in the keyring; nothing is sent to Anthropic, GitHub or anyone for telemetry. |

Locally stored: everything above plus judgments, analyses, summaries, digests, labels and usage
counters in `~/.local/share/gitinbox/gitinbox.db`.

---

## 14. FAQ

**Why is a thread "not judged"?** It is read, done, noise, or the judge budget ran out this tick —
press *Judge* or wait for the next sync. With no provider configured nothing is ever judged; the
inbox still sorts by activity kind, relation and recency.

**Why did a done thread come back?** New activity moved its `updated_at`; that is by design.

**Why do two rows show the same CiviCRM change?** One is the GitHub PR notification, the other the
GitLab issue to-do. They are different threads on different forges; the impact analysis is on the
PR.

**Why is "Analyze" greyed / slow?** Analysis fetches the PR's files through the forge and then
calls the judge; a note additionally calls Ollama (seconds with a resident 9B model, minutes if the
model spills to CPU). The row shows notices while it runs.

**Can I use GitInbox without any AI?** Yes: sync, classification, filtering, Mine, local actions
and the Inbox tabs need no provider.

**Is anything ever written to GitHub/GitLab?** Not in the default `readonly` mode. See §1 *Local
state vs. mirrored writes* and INSTALLATION §8. The agent cannot write to a forge in any mode: its
`github_read` pass-through accepts only read tools, and `draft_reply` stores text locally.

**The Agent view says pi was not found.** Install it (`npm install -g @earendil-works/pi-coding-agent`,
Node 22.19+) or run `npm install` inside the repository's `agent/` directory, or set the binary
path in Settings → Agent.
