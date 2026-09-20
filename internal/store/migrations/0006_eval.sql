-- M5: ground-truth labels and evaluation-only judgments.

CREATE TABLE labels (
  account_id       INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id        TEXT NOT NULL,
  category         TEXT NOT NULL DEFAULT '',   -- triage.v1 category id or ''
  requires_action  INTEGER,                    -- NULL unset, 0/1
  urgency          INTEGER NOT NULL DEFAULT -1, -- -1 unset, 0..3
  relevance        INTEGER NOT NULL DEFAULT -1, -- -1 unset, 0..2
  priority         INTEGER NOT NULL DEFAULT -1, -- -1 unset, 0 ignore .. 3 top (ranking ground truth)
  resolved         INTEGER,                    -- NULL unset, 0/1
  noise            INTEGER,                    -- NULL unset, 0/1 (should the filter hide it?)
  note             TEXT NOT NULL DEFAULT '',
  updated_at       TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id)
);

CREATE TABLE pr_labels (
  account_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  repo          TEXT NOT NULL,
  number        INTEGER NOT NULL,
  impact_level  INTEGER NOT NULL DEFAULT -1,   -- -1 unset, 0..3
  change_kind   TEXT NOT NULL DEFAULT '',
  note          TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL,
  PRIMARY KEY (account_id, repo, number)
);

-- Judgments produced only for evaluation (e.g. Ollama over the labeled set),
-- keyed by provider so several providers can be compared on the same threads.
CREATE TABLE eval_judgments (
  account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id       TEXT NOT NULL,
  provider        TEXT NOT NULL,
  model           TEXT NOT NULL DEFAULT '',
  calibrated      INTEGER NOT NULL DEFAULT 0,
  thread_version  TEXT NOT NULL DEFAULT '',
  answers_json    TEXT NOT NULL,
  usage_json      TEXT NOT NULL DEFAULT '{}',
  latency_ms      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id, provider)
);
