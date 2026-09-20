-- M1 schema: accounts, notification threads, "mine" items, sync state, settings.
-- Times are RFC3339 UTC strings. Local triage state (local_read_at, done_at,
-- snoozed_until) lives beside GitHub's fields and is authoritative for display.

CREATE TABLE accounts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  login       TEXT NOT NULL,
  host        TEXT NOT NULL DEFAULT 'github.com',
  write_mode  TEXT NOT NULL DEFAULT 'readonly',   -- readonly | notifications (PLAN.md §4.9)
  created_at  TEXT NOT NULL,
  UNIQUE (login, host)
);

CREATE TABLE threads (
  account_id          INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id           TEXT NOT NULL,
  repo                TEXT NOT NULL,
  subject_type        TEXT NOT NULL,
  subject_url         TEXT NOT NULL DEFAULT '',
  subject_number      INTEGER NOT NULL DEFAULT 0,
  html_url            TEXT NOT NULL DEFAULT '',
  title               TEXT NOT NULL,
  reason              TEXT NOT NULL,
  unread              INTEGER NOT NULL DEFAULT 1,
  updated_at          TEXT NOT NULL,
  last_read_at        TEXT,
  latest_comment_url  TEXT NOT NULL DEFAULT '',
  activity_kind       TEXT NOT NULL DEFAULT 'other',
  relation_tags       TEXT NOT NULL DEFAULT '[]',   -- JSON array
  filter_verdict      TEXT NOT NULL DEFAULT 'keep', -- keep | noise
  filter_reason       TEXT NOT NULL DEFAULT '',
  local_read_at       TEXT,
  done_at             TEXT,
  snoozed_until       TEXT,
  first_seen_at       TEXT NOT NULL,
  last_synced_at      TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id)
);
CREATE INDEX threads_by_updated ON threads (account_id, updated_at DESC);

CREATE TABLE items (
  account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  repo           TEXT NOT NULL,
  number         INTEGER NOT NULL,
  kind           TEXT NOT NULL,                 -- issue | pr
  title          TEXT NOT NULL,
  state          TEXT NOT NULL,
  html_url       TEXT NOT NULL DEFAULT '',
  author         TEXT NOT NULL DEFAULT '',
  assignees      TEXT NOT NULL DEFAULT '[]',    -- JSON array
  labels         TEXT NOT NULL DEFAULT '[]',    -- JSON array
  draft          INTEGER NOT NULL DEFAULT 0,
  comments       INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  relations      TEXT NOT NULL DEFAULT '[]',    -- JSON array: assigned | mentioned | review_requested | author
  last_seen_run  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, repo, number)
);

CREATE TABLE sync_state (
  account_id         INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  last_since         TEXT,
  last_full_at       TEXT,
  last_sync_at       TEXT,
  last_mine_at       TEXT,
  mine_run           INTEGER NOT NULL DEFAULT 0,
  poll_interval_sec  INTEGER NOT NULL DEFAULT 180,
  last_error         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE settings (
  key         TEXT PRIMARY KEY,
  value_json  TEXT NOT NULL
);
