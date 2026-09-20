-- M4: generated prose (thread summaries, digests) and notification de-duplication.

CREATE TABLE summaries (
  account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id       TEXT NOT NULL,
  thread_version  TEXT NOT NULL,          -- Thread.Version() the summary describes
  model           TEXT NOT NULL DEFAULT '',
  content_json    TEXT NOT NULL,          -- llm.ThreadSummary
  usage_json      TEXT NOT NULL DEFAULT '{}',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id)
);

CREATE TABLE digests (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  period_start  TEXT NOT NULL,
  period_end    TEXT NOT NULL,
  model         TEXT NOT NULL DEFAULT '',
  content_json  TEXT NOT NULL,            -- llm.Digest
  usage_json    TEXT NOT NULL DEFAULT '{}',
  latency_ms    INTEGER NOT NULL DEFAULT 0,
  thread_count  INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
);

-- One row per notification already delivered (thread:<acct>:<id>:<version>,
-- impact:<acct>:<repo>#<n>:<sha>) so a re-sync never repeats it.
CREATE TABLE notified (
  key         TEXT PRIMARY KEY,
  created_at  TEXT NOT NULL
);
